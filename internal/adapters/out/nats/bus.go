package nats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

const (
	// StreamName is the stream holding the events this machine produced.
	StreamName = "BR_EVENTS"
	// SubjectRoot is the root of all event subjects.
	SubjectRoot = "br"
	// DedupeWindow is how long JetStream remembers message ids.
	DedupeWindow = 2 * time.Minute
	// HeaderMachine carries the origin machine so replication can filter without decoding.
	HeaderMachine = "Br-Machine"
	// LiveStreamName is the stream of live state (machine heartbeats and
	// readings, session progress). It keeps the latest message of each
	// subject only, in memory, for a few minutes: a last-value cache, not a
	// log. Readings arrive every few seconds and nobody needs yesterday's.
	LiveStreamName = "BR_LIVE"
	// LiveSubjectRoot is the root of live subjects (outside "br.>", so live
	// state never enters the event stream).
	LiveSubjectRoot = "brlive"
	// LiveMaxAge is how long a live value stays without being replaced.
	LiveMaxAge = 5 * time.Minute
)

// LiveSubject maps a live event to its subject: one per project, type and
// subject (the machine, the agent), so a new value replaces the old one.
func LiveSubject(e domain.Event) string {
	who := e.Subject
	if who == "" {
		who = e.Machine
	}
	parts := append([]string{LiveSubjectRoot, Token(e.Project)}, typeTokens(e.Type)...)
	return strings.Join(append(parts, Token(who)), ".")
}

// Subject maps an event to its subject:
//
//	item.*    → br.<project>.item.<subject>.<rest of type>
//	machine.* → br.<project>.machine.<subject or machine>.<rest of type>
//	check.*   → br.<project>.check.<subject>.<rest of type>
//	other     → br.<project>.<type tokens>.<subject or "_">
func Subject(e domain.Event) string {
	parts := []string{SubjectRoot, Token(e.Project)}
	head, rest, _ := strings.Cut(e.Type, ".")
	switch head {
	case "item", "check":
		parts = append(parts, head, Token(e.Subject))
		parts = append(parts, typeTokens(rest)...)
	case "machine":
		who := e.Subject
		if who == "" {
			who = e.Machine
		}
		parts = append(parts, head, Token(who))
		parts = append(parts, typeTokens(rest)...)
	default:
		parts = append(parts, typeTokens(e.Type)...)
		parts = append(parts, Token(e.Subject))
	}
	return strings.Join(parts, ".")
}

func typeTokens(t string) []string {
	if t == "" {
		return []string{"_"}
	}
	raw := strings.Split(t, ".")
	out := make([]string, len(raw))
	for i, r := range raw {
		out[i] = Token(r)
	}
	return out
}

// ProjectFilter is the subject filter for one project, or all when empty.
func ProjectFilter(project string) string {
	if project == "" {
		return SubjectRoot + ".>"
	}
	return SubjectRoot + "." + Token(project) + ".>"
}

// HeartbeatSubject is where a machine announces it is alive.
func HeartbeatSubject(project, machine string) string {
	return Subject(domain.Event{Type: "machine.heartbeat", Project: project, Machine: machine})
}

// BusOptions tunes the event stream.
type BusOptions struct {
	// MaxAge drops events older than this from the stream (0 = keep forever).
	MaxAge time.Duration
	// MaxBytes bounds the stream size (0 = unlimited).
	MaxBytes int64
	// Memory keeps the stream in memory instead of files (tests).
	Memory bool
}

// Bus implements ports.EventBus over a connection to the local server.
type Bus struct {
	nc *nats.Conn
	js jetstream.JetStream
}

var _ ports.EventBus = (*Bus)(nil)

// NewBus creates (or updates) the event stream on the local server.
func NewBus(ctx context.Context, nc *nats.Conn, opts BusOptions) (*Bus, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, err
	}
	storage := jetstream.FileStorage
	if opts.Memory {
		storage = jetstream.MemoryStorage
	}
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        StreamName,
		Description: "events produced by this machine",
		Subjects:    []string{SubjectRoot + ".>"},
		Storage:     storage,
		Duplicates:  DedupeWindow,
		MaxAge:      opts.MaxAge,
		MaxBytes:    nonZero(opts.MaxBytes),
		Retention:   jetstream.LimitsPolicy,
		Discard:     jetstream.DiscardOld,
	})
	if err != nil {
		return nil, fmt.Errorf("nats: create stream: %w", err)
	}
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:              LiveStreamName,
		Description:       "latest live state of this machine: heartbeats, readings, sessions",
		Subjects:          []string{LiveSubjectRoot + ".>"},
		Storage:           jetstream.MemoryStorage,
		MaxMsgsPerSubject: 1,
		MaxAge:            LiveMaxAge,
		Retention:         jetstream.LimitsPolicy,
		Discard:           jetstream.DiscardOld,
	})
	if err != nil {
		return nil, fmt.Errorf("nats: create live stream: %w", err)
	}
	return &Bus{nc: nc, js: js}, nil
}

func nonZero(v int64) int64 {
	if v == 0 {
		return -1
	}
	return v
}

// Conn is the underlying connection.
func (b *Bus) Conn() *nats.Conn { return b.nc }

// JetStream is the underlying JetStream context.
func (b *Bus) JetStream() jetstream.JetStream { return b.js }

// Publish stores the event in the local stream and waits for the ack.
// The event id is the message id, so a repeat inside the window is dropped.
func (b *Bus) Publish(ctx context.Context, e domain.Event) error {
	if e.ID == "" {
		return errors.New("nats: event without id")
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	subject := Subject(e)
	if e.LiveOnly() {
		subject = LiveSubject(e) // replaces the previous value; never part of the log
	}
	m := nats.NewMsg(subject)
	m.Data = data
	m.Header.Set(nats.MsgIdHdr, e.ID)
	if e.Machine != "" {
		m.Header.Set(HeaderMachine, e.Machine)
	}
	if _, err := b.js.PublishMsg(ctx, m); err != nil {
		return fmt.Errorf("nats: publish %s: %w", e.Type, err)
	}
	return nil
}

// Subscribe delivers new events of a project (all projects when empty)
// to fn, one at a time and in stream order, until ctx ends. It returns
// once the subscription is active; delivery happens in the background.
// A slow fn only slows this subscriber: messages wait in the stream.
func (b *Bus) Subscribe(ctx context.Context, project string, fn func(domain.Event)) error {
	cons, err := b.js.OrderedConsumer(ctx, StreamName, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{ProjectFilter(project)},
		DeliverPolicy:  jetstream.DeliverNewPolicy,
	})
	if err != nil {
		return fmt.Errorf("nats: subscribe: %w", err)
	}
	return consumeEvents(ctx, cons, func(e domain.Event, _ jetstream.MsgMetadata) error {
		fn(e)
		return nil
	})
}

// consumeEvents runs fn sequentially for each event until ctx ends.
func consumeEvents(ctx context.Context, cons jetstream.Consumer, fn func(domain.Event, jetstream.MsgMetadata) error) error {
	it, err := cons.Messages(jetstream.PullMaxMessages(256))
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		it.Stop()
	}()
	go func() {
		for {
			msg, err := it.Next()
			if err != nil {
				if errors.Is(err, jetstream.ErrMsgIteratorClosed) || ctx.Err() != nil {
					return
				}
				time.Sleep(50 * time.Millisecond)
				continue
			}
			var e domain.Event
			if json.Unmarshal(msg.Data(), &e) != nil {
				continue
			}
			md, _ := msg.Metadata()
			var meta jetstream.MsgMetadata
			if md != nil {
				meta = *md
			}
			_ = fn(e, meta)
		}
	}()
	return nil
}

// StartHeartbeat publishes a machine.heartbeat event every interval until
// ctx ends. The app may instead publish its own machine events.
func StartHeartbeat(ctx context.Context, bus ports.EventBus, project string, self func() domain.Machine, every time.Duration, newID func() string) {
	if every <= 0 {
		every = 30 * time.Second
	}
	beat := func() {
		m := self()
		m.LastSeen = time.Now().UTC()
		e := domain.NewEvent(newID(), "machine.heartbeat", project, m.Name, m.Name, "", m.LastSeen, domain.Payload{MachineI: &m})
		pctx, cancel := context.WithTimeout(ctx, every)
		_ = bus.Publish(pctx, e)
		cancel()
	}
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		beat()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				beat()
			}
		}
	}()
}
