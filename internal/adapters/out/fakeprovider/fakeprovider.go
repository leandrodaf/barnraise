// Package fakeprovider is a deterministic ports.Provider for end-to-end tests.
// A test registers a behavior with Handle (it may edit files in the workdir
// or call back into the system) or replays a recorded Claude stream-json
// session with Replay. Every SessionSpec received is kept for assertions.
package fakeprovider

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/claude"
	"github.com/leandrodaf/barnraise/internal/adapters/out/provcommon"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// HandlerFunc is the behavior of one session. emit sends an event to the
// session's Events channel; it never blocks. When the handler emits no
// PEDone, one is added from the returned result.
type HandlerFunc func(ctx context.Context, spec ports.SessionSpec, emit func(ports.ProviderEvent)) (ports.SessionResult, error)

// Provider is the fake. The zero value is not usable; call New.
type Provider struct {
	name, family string

	mu      sync.Mutex
	handler HandlerFunc
	replay  string
	caps    ports.Capabilities
	openErr error
	specs   []ports.SessionSpec
	opened  int
}

// New returns a fake provider; Detect reports every capability.
func New(name, family string) *Provider {
	return &Provider{
		name:   name,
		family: family,
		caps: ports.Capabilities{
			Version: "fake", Headless: true, Stream: true, Resume: true, Fork: true,
			Hooks: true, HookBlock: true, Subagents: true, Skills: true, MCP: true,
			Sandbox: true, Messaging: true, SessionState: true, UsageLimits: true,
			ModelSwitch: true, JSONSchema: true, Images: true,
		},
	}
}

// Handle sets the behavior of the next sessions (it wins over Replay).
func (p *Provider) Handle(fn HandlerFunc) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.handler = fn
	return p
}

// Replay makes sessions without a handler replay a Claude stream-json
// recording through the Claude parser.
func (p *Provider) Replay(path string) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.replay = path
	return p
}

// SetCapabilities overrides what Detect reports.
func (p *Provider) SetCapabilities(c ports.Capabilities) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.caps = c
	return p
}

// FailOpen makes Open return err (nil restores normal behavior).
func (p *Provider) FailOpen(err error) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.openErr = err
	return p
}

// Specs returns a copy of every SessionSpec Open received, in order.
func (p *Provider) Specs() []ports.SessionSpec {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ports.SessionSpec(nil), p.specs...)
}

// Name implements ports.Provider.
func (p *Provider) Name() string { return p.name }

// Family implements ports.Provider.
func (p *Provider) Family() string { return p.family }

// Detect implements ports.Provider.
func (p *Provider) Detect(context.Context) (ports.Capabilities, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.caps, nil
}

// Open implements ports.Provider.
func (p *Provider) Open(ctx context.Context, spec ports.SessionSpec) (ports.Session, error) {
	p.mu.Lock()
	p.specs = append(p.specs, spec)
	if p.openErr != nil {
		err := p.openErr
		p.mu.Unlock()
		return nil, err
	}
	p.opened++
	n := p.opened
	handler, replay := p.handler, p.replay
	p.mu.Unlock()

	if handler == nil {
		if replay != "" {
			handler = replayHandler(replay)
		} else {
			handler = defaultHandler
		}
	}

	var cancel context.CancelFunc
	if spec.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	s := &session{
		events: make(chan ports.ProviderEvent),
		queue:  provcommon.NewEventQueue(),
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go s.queue.Forward(s.events)
	go s.run(ctx, spec, handler, fmt.Sprintf("fake-%s-%d", p.name, n))
	return s, nil
}

type session struct {
	events chan ports.ProviderEvent
	queue  *provcommon.EventQueue
	cancel context.CancelFunc

	mu      sync.Mutex
	stopped bool

	done chan struct{}
	res  ports.SessionResult
	err  error
}

func (s *session) run(ctx context.Context, spec ports.SessionSpec, fn HandlerFunc, defaultID string) {
	defer s.cancel()
	start := time.Now()
	sawDone := false
	var emitMu sync.Mutex
	emit := func(ev ports.ProviderEvent) {
		emitMu.Lock()
		if ev.Kind == ports.PEDone {
			sawDone = true
		}
		emitMu.Unlock()
		s.queue.Push(ev)
	}
	res, err := safeCall(ctx, spec, fn, emit)
	if res.SessionID == "" {
		res.SessionID = defaultID
	}
	res.Duration = time.Since(start)
	if res.Usage.Duration == 0 {
		res.Usage.Duration = res.Duration
	}
	emitMu.Lock()
	needDone := !sawDone
	emitMu.Unlock()
	if needDone {
		u := res.Usage
		s.queue.Push(ports.ProviderEvent{Kind: ports.PEDone, SessionID: res.SessionID, Text: res.Text, Usage: &u, IsError: err != nil || res.ExitCode != 0})
	}

	s.mu.Lock()
	stopped := s.stopped
	s.mu.Unlock()
	switch {
	case stopped:
		err = provcommon.ErrStopped
	case errors.Is(ctx.Err(), context.DeadlineExceeded) && (err == nil || errors.Is(err, context.DeadlineExceeded)):
		err = fmt.Errorf("%w after %s", provcommon.ErrTimeout, spec.Timeout)
	}
	s.res, s.err = res, err
	s.queue.Close()
	close(s.done)
}

func safeCall(ctx context.Context, spec ports.SessionSpec, fn HandlerFunc, emit func(ports.ProviderEvent)) (res ports.SessionResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("fakeprovider: handler panic: %v", r)
		}
	}()
	return fn(ctx, spec, emit)
}

func (s *session) Events() <-chan ports.ProviderEvent { return s.events }

func (s *session) Wait() (ports.SessionResult, error) {
	<-s.done
	return s.res, s.err
}

func (s *session) Stop() error {
	s.mu.Lock()
	select {
	case <-s.done:
	default:
		s.stopped = true
	}
	s.mu.Unlock()
	s.cancel()
	return nil
}

func defaultHandler(_ context.Context, spec ports.SessionSpec, emit func(ports.ProviderEvent)) (ports.SessionResult, error) {
	emit(ports.ProviderEvent{Kind: ports.PEInit, Text: spec.Model})
	emit(ports.ProviderEvent{Kind: ports.PEMessage, Text: "ok"})
	return ports.SessionResult{Text: "ok"}, nil
}

// ReplayFile emits the events of a Claude stream-json recording and returns
// its result; handlers can call it to mix a recording with their own work.
func ReplayFile(ctx context.Context, path string, spec ports.SessionSpec, emit func(ports.ProviderEvent)) (ports.SessionResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return ports.SessionResult{}, err
	}
	defer f.Close()
	parser := claude.NewParser(domain.TokenUsage{
		Agent: spec.Agent, Account: spec.Account.ID, Model: spec.Model, Role: spec.Role,
	})
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return parser.Result(), err
		}
		line, rerr := r.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			for _, ev := range parser.Parse(line) {
				emit(ev)
			}
		}
		if rerr != nil {
			break
		}
	}
	for _, ev := range parser.Finish(0, "") {
		emit(ev)
	}
	return parser.Result(), nil
}

func replayHandler(path string) HandlerFunc {
	return func(ctx context.Context, spec ports.SessionSpec, emit func(ports.ProviderEvent)) (ports.SessionResult, error) {
		return ReplayFile(ctx, path, spec, emit)
	}
}

var _ ports.Provider = (*Provider)(nil)
