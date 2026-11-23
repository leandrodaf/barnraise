package app

import (
	"strings"
	"sync"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// Hub fans live events out to SSE clients, webhooks and the runtime.
// Slow subscribers drop events instead of blocking the daemon (they can
// catch up from the log by sequence).
type Hub struct {
	mu   sync.RWMutex
	subs map[int]*hubSub
	next int
}

type hubSub struct {
	project string
	types   []string
	ch      chan domain.Event
	dropped int
}

// NewHub creates a hub.
func NewHub() *Hub { return &Hub{subs: map[int]*hubSub{}} }

// Subscribe returns a channel of events matching project ("" = all) and
// type filters (exact or prefix "item.*"), and a cancel function.
func (h *Hub) Subscribe(project string, types []string, buffer int) (<-chan domain.Event, func()) {
	if buffer <= 0 {
		buffer = 256
	}
	h.mu.Lock()
	id := h.next
	h.next++
	s := &hubSub{project: project, types: types, ch: make(chan domain.Event, buffer)}
	h.subs[id] = s
	h.mu.Unlock()
	return s.ch, func() {
		h.mu.Lock()
		if _, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(s.ch)
		}
		h.mu.Unlock()
	}
}

// Publish delivers to every matching subscriber without blocking.
func (h *Hub) Publish(e domain.Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, s := range h.subs {
		if s.project != "" && s.project != e.Project {
			continue
		}
		if !MatchType(s.types, e.Type) {
			continue
		}
		select {
		case s.ch <- e:
		default:
			s.dropped++
		}
	}
}

// MatchType matches event type filters: empty matches all, "item.*" is a prefix.
func MatchType(filters []string, typ string) bool {
	if len(filters) == 0 {
		return true
	}
	for _, f := range filters {
		if f == "*" || f == typ {
			return true
		}
		if strings.HasSuffix(f, ".*") && strings.HasPrefix(typ, strings.TrimSuffix(f, "*")) {
			return true
		}
	}
	return false
}
