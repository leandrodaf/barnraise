package otlp

import (
	"crypto/sha256"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// Tracer turns events into spans. Each item is one trace whose id comes
// from the project and the item, so its spans join up across restarts of
// the plugin and across machines:
//
//   - item <id>: the root, from the item's creation to its integration or
//     closing;
//   - step <name>: each step, from its start to item.step.finished;
//   - agent <role>: each agent run of the ledger (agent.usage), with its
//     tokens, cost and duration, under its step;
//   - check <name>: each check result (check.completed), under its step.
//
// Ledger entries without an item (the analyst's triage) are traces of
// their own.
type Tracer struct {
	mu    sync.Mutex
	items map[string]*itemState
	emit  func(Span)
}

type itemState struct {
	created   time.Time
	step      string
	stepStart time.Time
}

// NewTracer returns a tracer that hands finished spans to emit.
func NewTracer(emit func(Span)) *Tracer {
	return &Tracer{items: map[string]*itemState{}, emit: emit}
}

func hash(parts ...string) [32]byte {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// TraceID is the trace of an item.
func TraceID(project, item string) [16]byte {
	var id [16]byte
	h := hash("trace", project, item)
	copy(id[:], h[:16])
	return id
}

func spanID(parts ...string) [8]byte {
	var id [8]byte
	h := hash(parts...)
	copy(id[:], h[:8])
	if id == ([8]byte{}) {
		id[7] = 1 // all zero is "no span"
	}
	return id
}

// ItemSpanID is the root span of an item.
func ItemSpanID(project, item string) [8]byte { return spanID("item", project, item) }

// StepSpanID is the span of one run of a step.
func StepSpanID(project, item, step string, start time.Time) [8]byte {
	return spanID("step", project, item, step, start.UTC().Format(time.RFC3339Nano))
}

// Handle reads one event.
func (t *Tracer) Handle(e domain.Event) {
	pl, err := e.DecodePayload()
	if err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	key := e.Project + "\x00" + e.Subject
	st := t.items[key]
	if st == nil && e.Subject != "" && pl.Item != nil {
		st = &itemState{}
		t.items[key] = st
	}
	base := []Attr{{"barnraise.project", e.Project}, {"barnraise.machine", e.Machine}, {"barnraise.event.id", e.ID}}
	switch e.Type {
	case domain.EvItemStepFinished:
		step, _ := pl.Extra["step"].(string)
		outcome, _ := pl.Extra["outcome"].(string)
		if step != "" {
			start, known := e.Time, false
			if st != nil && st.step == step && !st.stepStart.IsZero() {
				start, known = st.stepStart, true
			}
			s := Span{TraceID: TraceID(e.Project, e.Subject), SpanID: StepSpanID(e.Project, e.Subject, step, start),
				Parent: ItemSpanID(e.Project, e.Subject), Name: "step " + step, Start: start, End: e.Time,
				Attrs: append(base, Attr{"barnraise.item.id", e.Subject}, Attr{"barnraise.step", step}, Attr{"barnraise.outcome", outcome}, Attr{"barnraise.actor", e.Actor})}
			if !known {
				s.Attrs = append(s.Attrs, Attr{"barnraise.start_unknown", true})
			}
			if outcome == string(domain.OutcomeFail) || outcome == string(domain.OutcomeReject) || outcome == string(domain.OutcomeBlocked) {
				s.Error, s.Message = true, truncate(pl.Note, 300)
			}
			t.emit(s)
		}
	case domain.EvAgentUsage:
		if u := pl.Usage; u != nil {
			item := u.ItemID
			if item == "" {
				item = e.Subject
			}
			s := Span{Name: "agent " + def(u.Role, u.Agent), Start: e.Time.Add(-u.Duration), End: e.Time,
				Attrs: append(base,
					Attr{"barnraise.item.id", item}, Attr{"barnraise.step", u.Step}, Attr{"barnraise.agent", u.Agent}, Attr{"barnraise.role", u.Role},
					Attr{"barnraise.account", u.Account}, Attr{"gen_ai.system", u.Provider}, Attr{"gen_ai.request.model", u.Model},
					Attr{"gen_ai.usage.input_tokens", u.Input}, Attr{"gen_ai.usage.output_tokens", u.Output},
					Attr{"barnraise.tokens.cache_read", u.CacheRead}, Attr{"barnraise.tokens.cache_write", u.CacheWrite},
					Attr{"barnraise.cost_usd", u.CostUSD}, Attr{"barnraise.duration_ms", u.Duration.Milliseconds()},
					Attr{"barnraise.explore_reads", u.ExploreReads})}
			if item == "" {
				s.TraceID = TraceID(e.Project, "event:"+e.ID)
				s.SpanID = spanID("usage", e.Project, e.ID)
			} else {
				s.TraceID = TraceID(e.Project, item)
				s.SpanID = spanID("usage", e.Project, item, e.ID)
				s.Parent = t.parentOf(e.Project, item, u.Step)
			}
			t.emit(s)
		}
	case domain.EvCheckCompleted:
		if c := pl.Check; c != nil && e.Subject != "" {
			start := e.Time.Add(-c.Duration)
			s := Span{TraceID: TraceID(e.Project, e.Subject), SpanID: spanID("check", e.Project, e.Subject, e.ID),
				Parent: t.parentOf(e.Project, e.Subject, ""), Name: "check " + c.Check, Start: start, End: e.Time,
				Attrs: append(base, Attr{"barnraise.item.id", e.Subject}, Attr{"barnraise.check", c.Check}, Attr{"barnraise.check.passed", c.Passed},
					Attr{"barnraise.check.runs_on", c.RunsOn}, Attr{"barnraise.check.flaky", c.Flaky}, Attr{"barnraise.check.main_red", c.MainRed},
					Attr{"barnraise.commit", c.Commit}, Attr{"barnraise.duration_ms", c.Duration.Milliseconds()})}
			if !c.Passed {
				s.Error, s.Message = true, truncate(c.Summary, 300)
			}
			t.emit(s)
		}
	case domain.EvItemIntegrated, domain.EvItemClosed:
		if it := pl.Item; it != nil && e.Subject != "" {
			start := it.CreatedAt
			if st != nil && !st.created.IsZero() {
				start = st.created
			}
			if start.IsZero() || start.After(e.Time) {
				start = e.Time
			}
			s := Span{TraceID: TraceID(e.Project, e.Subject), SpanID: ItemSpanID(e.Project, e.Subject), Name: "item " + e.Subject, Start: start, End: e.Time,
				Attrs: append(base, Attr{"barnraise.item.id", e.Subject}, Attr{"barnraise.item.kind", it.Kind}, Attr{"barnraise.item.priority", it.Priority.String()},
					Attr{"barnraise.item.area", it.Area}, Attr{"barnraise.item.lane", string(it.Lane)}, Attr{"barnraise.item.status", string(it.Status)},
					Attr{"barnraise.item.rework", len(it.Rework)}, Attr{"barnraise.item.closed_reason", it.ClosedReason}, Attr{"barnraise.commit", it.MergeCommit})}
			if e.Type == domain.EvItemClosed && it.Status != domain.StatusIntegrated {
				s.Attrs = append(s.Attrs, Attr{"barnraise.item.closed", true})
			}
			t.emit(s)
			delete(t.items, key)
			return
		}
	}
	if st != nil && pl.Item != nil {
		if !pl.Item.CreatedAt.IsZero() {
			st.created = pl.Item.CreatedAt
		}
		if pl.Item.Step != "" && !pl.Item.StepStartedAt.IsZero() {
			st.step, st.stepStart = pl.Item.Step, pl.Item.StepStartedAt
		}
	}
}

// parentOf is the span a ledger entry or a check belongs to: the item's
// current step when it is known (and is that step), else the item.
func (t *Tracer) parentOf(project, item, step string) [8]byte {
	if st := t.items[project+"\x00"+item]; st != nil && st.step != "" && !st.stepStart.IsZero() && (step == "" || step == st.step) {
		return StepSpanID(project, item, st.step, st.stepStart)
	}
	return ItemSpanID(project, item)
}

// Tracked is the number of items with state (for tests).
func (t *Tracer) Tracked() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.items)
}

func def(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
