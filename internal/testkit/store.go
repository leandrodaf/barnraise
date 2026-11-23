package testkit

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// ErrClosed is returned by a closed Store.
var ErrClosed = errors.New("store closed")

// Store is an in-memory ports.EventStore with the semantics of the SQLite
// adapter: Seq is global and starts at 1, events whose id is already stored
// are skipped (events without an id are always stored), times come back in
// UTC with nanosecond precision, empty data comes back nil, and Query with a
// Limit takes the newest matches but returns them oldest first.
type Store struct {
	mu     sync.Mutex
	events []domain.Event
	ids    map[string]bool
	seq    int64
	closed bool

	// FailAppend, when set, makes Append return it (and store nothing).
	FailAppend error
	// FailQuery, when set, makes Load and Query return it.
	FailQuery error
}

var _ ports.EventStore = (*Store)(nil)

// NewStore returns an empty store.
func NewStore() *Store { return &Store{ids: map[string]bool{}} }

func normalize(e domain.Event) domain.Event {
	e.Time = time.Unix(0, e.Time.UTC().UnixNano()).UTC()
	if len(e.Data) == 0 {
		e.Data = nil
	} else {
		e.Data = append([]byte(nil), e.Data...)
	}
	return e
}

// Append implements ports.EventStore.
func (s *Store) Append(_ context.Context, events ...domain.Event) ([]domain.Event, error) {
	if len(events) == 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	if s.FailAppend != nil {
		return nil, s.FailAppend
	}
	out := make([]domain.Event, 0, len(events))
	for _, e := range events {
		if e.ID != "" && s.ids[e.ID] {
			continue
		}
		if e.ID != "" {
			s.ids[e.ID] = true
		}
		s.seq++
		stored := normalize(e)
		stored.Seq = s.seq
		s.events = append(s.events, stored)
		// The returned event keeps the caller's time and data, like the SQL adapter.
		e.Seq = s.seq
		out = append(out, e)
	}
	return out, nil
}

// Load implements ports.EventStore.
func (s *Store) Load(_ context.Context, project string, afterSeq int64) ([]domain.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	if s.FailQuery != nil {
		return nil, s.FailQuery
	}
	var out []domain.Event
	for _, e := range s.events {
		if e.Seq > afterSeq && (project == "" || e.Project == project) {
			out = append(out, copyEvent(e))
		}
	}
	return out, nil
}

// Query implements ports.EventStore.
func (s *Store) Query(_ context.Context, q ports.EventQuery) ([]domain.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	if s.FailQuery != nil {
		return nil, s.FailQuery
	}
	types := map[string]bool{}
	for _, t := range q.Types {
		types[t] = true
	}
	since := int64(0)
	if !q.Since.IsZero() {
		since = q.Since.UTC().UnixNano()
	}
	var out []domain.Event
	for _, e := range s.events {
		if q.Project != "" && e.Project != q.Project {
			continue
		}
		if q.Subject != "" && e.Subject != q.Subject {
			continue
		}
		if len(q.Types) > 0 && !types[e.Type] {
			continue
		}
		if !q.Since.IsZero() && e.Time.UnixNano() < since {
			continue
		}
		out = append(out, copyEvent(e))
	}
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[len(out)-q.Limit:]
	}
	return out, nil
}

// Close implements ports.EventStore.
func (s *Store) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

// Events returns a copy of every stored event, in seq order.
func (s *Store) Events() []domain.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Event, len(s.events))
	for i, e := range s.events {
		out[i] = copyEvent(e)
	}
	return out
}

// Types returns the types of the stored events of a subject ("" = all), in order.
func (s *Store) Types(subject string) []string {
	var out []string
	for _, e := range s.Events() {
		if subject == "" || e.Subject == subject {
			out = append(out, e.Type)
		}
	}
	return out
}

func copyEvent(e domain.Event) domain.Event {
	if e.Data != nil {
		e.Data = append([]byte(nil), e.Data...)
	}
	return e
}
