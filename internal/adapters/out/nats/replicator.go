package nats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// CursorStore remembers, per peer, the last stream sequence applied from it,
// so a restart resumes where it stopped.
type CursorStore interface {
	GetCursor(peer string) uint64
	SetCursor(peer string, seq uint64) error
}

// MemCursorStore keeps cursors in memory.
type MemCursorStore struct {
	mu sync.Mutex
	m  map[string]uint64
}

// NewMemCursorStore creates an empty in-memory cursor store.
func NewMemCursorStore() *MemCursorStore { return &MemCursorStore{m: map[string]uint64{}} }

// GetCursor implements CursorStore.
func (s *MemCursorStore) GetCursor(peer string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[peer]
}

// SetCursor implements CursorStore.
func (s *MemCursorStore) SetCursor(peer string, seq uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[peer] = seq
	return nil
}

// FileCursorStore keeps cursors in a small JSON file, rewritten atomically.
type FileCursorStore struct {
	mu   sync.Mutex
	path string
	m    map[string]uint64
}

// NewFileCursorStore loads (or starts) the cursor file at path.
func NewFileCursorStore(path string) (*FileCursorStore, error) {
	s := &FileCursorStore{path: path, m: map[string]uint64{}}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(data, &s.m); err != nil {
			return nil, fmt.Errorf("nats: cursor file %s: %w", path, err)
		}
	}
	return s, nil
}

// GetCursor implements CursorStore.
func (s *FileCursorStore) GetCursor(peer string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[peer]
}

// SetCursor implements CursorStore.
func (s *FileCursorStore) SetCursor(peer string, seq uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m[peer] == seq {
		return nil
	}
	s.m[peer] = seq
	data, err := json.Marshal(s.m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// PeerStatus is what the replicator knows about one peer.
type PeerStatus struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Connected bool   `json:"connected"`
	Cursor    uint64 `json:"cursor"`
	LastError string `json:"last_error,omitempty"`
}

// Replicator pulls the events of every peer machine from the peer's own
// stream and hands them to apply. Only events produced by that peer are
// applied, so events never bounce between machines. apply must be
// idempotent: delivery is at least once (a crash between apply and the
// cursor write repeats one event).
type Replicator struct {
	machine string
	apply   func(domain.Event) error
	store   CursorStore

	// MaxBackoff caps the reconnect and retry delay (default 30s).
	MaxBackoff time.Duration
	// Logf receives diagnostics; nil discards them.
	Logf func(format string, args ...any)

	mu    sync.Mutex
	peers map[string]*peerLink
}

type peerLink struct {
	name, url string
	nc        *nats.Conn
	cancel    context.CancelFunc
	done      chan struct{}

	mu      sync.Mutex
	lastErr string
}

// NewReplicator creates a replicator for the local machine.
func NewReplicator(machine string, apply func(domain.Event) error, store CursorStore) *Replicator {
	if store == nil {
		store = NewMemCursorStore()
	}
	return &Replicator{machine: machine, apply: apply, store: store, MaxBackoff: 30 * time.Second, peers: map[string]*peerLink{}}
}

func (r *Replicator) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

// Backoff returns the exponential delay for attempt n (0-based) with jitter, capped at max.
func Backoff(n int, base, max time.Duration) time.Duration {
	if base <= 0 {
		base = 100 * time.Millisecond
	}
	d := base
	for i := 0; i < n && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	// up to 20% jitter so machines do not retry in lockstep
	return d - time.Duration(rand.Int64N(int64(d)/5+1))
}

// AddPeer starts replicating from a peer server. Extra options (for example
// NkeyOption) are passed to the connection. Adding a known peer replaces it.
func (r *Replicator) AddPeer(name, url string, opts ...nats.Option) error {
	if name == "" || name == r.machine {
		return fmt.Errorf("nats: bad peer name %q", name)
	}
	r.RemovePeer(name)
	max := r.MaxBackoff
	if max <= 0 {
		max = 30 * time.Second
	}
	p := &peerLink{name: name, url: url, done: make(chan struct{})}
	all := []nats.Option{
		nats.Name("barnraise-replica-" + Token(r.machine)),
		nats.MaxReconnects(-1),
		nats.RetryOnFailedConnect(true),
		nats.CustomReconnectDelay(func(attempts int) time.Duration { return Backoff(attempts, 250*time.Millisecond, max) }),
		nats.ConnectHandler(func(*nats.Conn) { r.logf("replica %s: connected", name) }),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) { r.logf("replica %s: disconnected: %v", name, err) }),
	}
	nc, err := nats.Connect(url, append(all, opts...)...)
	if err != nil {
		return fmt.Errorf("nats: connect peer %s: %w", name, err)
	}
	p.nc = nc
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	r.mu.Lock()
	r.peers[name] = p
	r.mu.Unlock()
	go r.run(ctx, p, max)
	return nil
}

// RemovePeer stops replicating from a peer. The cursor is kept.
func (r *Replicator) RemovePeer(name string) {
	r.mu.Lock()
	p := r.peers[name]
	delete(r.peers, name)
	r.mu.Unlock()
	if p == nil {
		return
	}
	p.cancel()
	<-p.done
	p.nc.Close()
}

// Close stops every peer.
func (r *Replicator) Close() {
	r.mu.Lock()
	names := make([]string, 0, len(r.peers))
	for n := range r.peers {
		names = append(names, n)
	}
	r.mu.Unlock()
	for _, n := range names {
		r.RemovePeer(n)
	}
}

// Peers reports the state of each peer, sorted by name.
func (r *Replicator) Peers() []PeerStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]PeerStatus, 0, len(r.peers))
	for _, p := range r.peers {
		p.mu.Lock()
		out = append(out, PeerStatus{Name: p.name, URL: p.url, Connected: p.nc.IsConnected(), Cursor: r.store.GetCursor(p.name), LastError: p.lastErr})
		p.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Conn returns the connection to a peer, for requests.
func (r *Replicator) Conn(peer string) (*nats.Conn, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.peers[peer]
	if p == nil {
		return nil, false
	}
	return p.nc, true
}

// Request calls a subject on a peer's server with timeout and retries.
func (r *Replicator) Request(ctx context.Context, peer, subject string, data []byte, timeout time.Duration, retries int) ([]byte, error) {
	nc, ok := r.Conn(peer)
	if !ok {
		return nil, fmt.Errorf("nats: unknown peer %q", peer)
	}
	return Request(ctx, nc, subject, data, timeout, retries)
}

func (r *Replicator) run(ctx context.Context, p *peerLink, max time.Duration) {
	defer close(p.done)
	js, err := jetstream.New(p.nc)
	if err != nil {
		p.setErr(err)
		return
	}
	var live sync.WaitGroup
	live.Go(func() { r.runLive(ctx, js, p, max) })
	defer live.Wait() // before done is closed: the connection outlives both loops
	for attempt := 0; ctx.Err() == nil; attempt++ {
		progressed, err := r.consume(ctx, js, p)
		if ctx.Err() != nil {
			return
		}
		if progressed {
			attempt = 0
		}
		if err != nil {
			p.setErr(err)
			r.logf("replica %s: %v", p.name, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(Backoff(attempt, 250*time.Millisecond, max)):
		}
	}
}

// runLive follows the peer's live state: the latest value of each subject
// first, then every change. No cursor is kept: after a restart the latest
// values are simply read again.
func (r *Replicator) runLive(ctx context.Context, js jetstream.JetStream, p *peerLink, max time.Duration) {
	for attempt := 0; ctx.Err() == nil; attempt++ {
		if err := r.consumeLive(ctx, js, p); err != nil && ctx.Err() == nil {
			r.logf("replica %s: live state: %v", p.name, err)
		} else {
			attempt = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(Backoff(attempt, 250*time.Millisecond, max)):
		}
	}
}

func (r *Replicator) consumeLive(ctx context.Context, js jetstream.JetStream, p *peerLink) error {
	ictx, cancel := context.WithTimeout(ctx, 10*time.Second)
	cons, err := js.OrderedConsumer(ictx, LiveStreamName, jetstream.OrderedConsumerConfig{
		DeliverPolicy: jetstream.DeliverLastPerSubjectPolicy,
		NamePrefix:    fmt.Sprintf("live_%s_%x", Token(r.machine), rand.Uint64()),
	})
	cancel()
	if err != nil {
		if errors.Is(err, jetstream.ErrStreamNotFound) {
			// An older peer without the live stream: nothing to follow, try again later.
			select {
			case <-ctx.Done():
			case <-time.After(time.Minute):
			}
			return nil
		}
		return fmt.Errorf("live consumer: %w", err)
	}
	it, err := cons.Messages(jetstream.PullMaxMessages(64))
	if err != nil {
		return fmt.Errorf("live messages: %w", err)
	}
	stop := context.AfterFunc(ctx, it.Stop)
	defer stop()
	defer it.Stop()
	for {
		msg, err := it.Next()
		if err != nil {
			if errors.Is(err, jetstream.ErrMsgIteratorClosed) {
				return ctx.Err()
			}
			return err
		}
		var e domain.Event
		if json.Unmarshal(msg.Data(), &e) == nil && e.Machine == p.name && e.LiveOnly() {
			_ = r.apply(e) // a lost reading is replaced by the next one
		}
	}
}

func (p *peerLink) setErr(err error) {
	p.mu.Lock()
	p.lastErr = err.Error()
	p.mu.Unlock()
}

// consume reads the peer stream from the cursor until an error or ctx ends.
func (r *Replicator) consume(ctx context.Context, js jetstream.JetStream, p *peerLink) (progressed bool, err error) {
	cursor := r.store.GetCursor(p.name)
	ictx, cancel := context.WithTimeout(ctx, 10*time.Second)
	st, err := js.Stream(ictx, StreamName)
	if err != nil {
		cancel()
		return false, fmt.Errorf("stream info: %w", err)
	}
	info, err := st.Info(ictx)
	if err != nil {
		cancel()
		return false, fmt.Errorf("stream info: %w", err)
	}
	if info.State.LastSeq < cursor {
		// The peer lost its store and restarted the numbering; read it all
		// again (apply is idempotent).
		r.logf("replica %s: stream reset (last %d < cursor %d)", p.name, info.State.LastSeq, cursor)
		cursor = 0
		if err := r.store.SetCursor(p.name, 0); err != nil {
			cancel()
			return false, err
		}
	}
	cfg := jetstream.OrderedConsumerConfig{DeliverPolicy: jetstream.DeliverAllPolicy, NamePrefix: fmt.Sprintf("replica_%s_%x", Token(r.machine), rand.Uint64())}
	if cursor > 0 {
		cfg.DeliverPolicy = jetstream.DeliverByStartSequencePolicy
		cfg.OptStartSeq = cursor + 1
	}
	cons, err := st.OrderedConsumer(ictx, cfg)
	cancel()
	if err != nil {
		return false, fmt.Errorf("consumer: %w", err)
	}
	it, err := cons.Messages(jetstream.PullMaxMessages(256))
	if err != nil {
		return false, fmt.Errorf("messages: %w", err)
	}
	stop := context.AfterFunc(ctx, it.Stop)
	defer stop()
	defer it.Stop()
	for {
		msg, err := it.Next()
		if err != nil {
			if errors.Is(err, jetstream.ErrMsgIteratorClosed) {
				return progressed, ctx.Err()
			}
			return progressed, err
		}
		md, err := msg.Metadata()
		if err != nil {
			continue
		}
		seq := md.Sequence.Stream
		if origin := msg.Headers().Get(HeaderMachine); origin == "" || origin == p.name {
			var e domain.Event
			if json.Unmarshal(msg.Data(), &e) == nil && e.Machine == p.name {
				if err := r.apply(e); err != nil {
					// Do not move the cursor: the event comes again after the backoff.
					return progressed, fmt.Errorf("apply %s: %w", e.ID, err)
				}
			}
		}
		if err := r.store.SetCursor(p.name, seq); err != nil {
			return progressed, fmt.Errorf("cursor: %w", err)
		}
		progressed = true
	}
}

// RPCSubject is the request subject for a method served by a machine.
// It lives outside "br.>" so requests are never stored as events.
func RPCSubject(machine, method string) string {
	if method == ">" {
		return "br-rpc." + Token(machine) + ".>"
	}
	return "br-rpc." + Token(machine) + "." + Token(method)
}

// Request sends a request and waits for the reply, retrying on timeout or
// when nobody answers, with exponential backoff between tries.
func Request(ctx context.Context, nc *nats.Conn, subject string, data []byte, timeout time.Duration, retries int) ([]byte, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	var last error
	for i := 0; i <= retries; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(Backoff(i-1, 100*time.Millisecond, 5*time.Second)):
			}
		}
		rctx, cancel := context.WithTimeout(ctx, timeout)
		msg, err := nc.RequestWithContext(rctx, subject, data)
		cancel()
		if err == nil {
			if e := msg.Header.Get(errHeader); e != "" {
				return nil, &RemoteError{Msg: e}
			}
			return msg.Data, nil
		}
		last = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !retryable(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("nats: request %s: %w", subject, last)
}

func retryable(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout) ||
		errors.Is(err, nats.ErrNoResponders) || errors.Is(err, nats.ErrConnectionClosed) ||
		errors.Is(err, nats.ErrConnectionReconnecting) || errors.Is(err, nats.ErrDisconnected)
}

const errHeader = "Br-Error"

// RemoteError is an error returned by the handler on the other side.
type RemoteError struct{ Msg string }

func (e *RemoteError) Error() string { return "remote: " + e.Msg }

// Serve answers requests on subject until ctx ends. A handler error is
// sent back and surfaces as *RemoteError at the caller.
func Serve(ctx context.Context, nc *nats.Conn, subject string, handler func(ctx context.Context, data []byte) ([]byte, error)) error {
	sub, err := nc.Subscribe(subject, func(m *nats.Msg) {
		out, err := handler(ctx, m.Data)
		reply := nats.NewMsg(m.Reply)
		if err != nil {
			reply.Header.Set(errHeader, err.Error())
		} else {
			reply.Data = out
		}
		_ = m.RespondMsg(reply)
	})
	if err != nil {
		return err
	}
	context.AfterFunc(ctx, func() { _ = sub.Unsubscribe() })
	return nc.Flush()
}
