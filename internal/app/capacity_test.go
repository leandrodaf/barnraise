package app

import (
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// A step that no account here may run says which limit stops it and when
// the first one frees up, and every screen shows it, instead of a bare
// "no account available" while the work stalls for hours.
func TestNoCapacitySaysWhyAndUntilWhen(t *testing.T) {
	e := newEnv(t)
	now := e.clock.Now()
	fiveHours := now.Add(2 * time.Hour)
	monday := now.Add(72 * time.Hour)
	accounts := []*domain.Account{
		{ID: "claude-3", Provider: "fake", CeilingPct: 100},
		{ID: "claude-4", Provider: "fake", CeilingPct: 100, Usage: domain.Usage{FiveHourPct: 100, FiveHourResets: fiveHours}},
		{ID: "elsewhere", Provider: "fake", Machine: "other-machine"},
	}
	e.svc.markModelLimited("claude-3", "fake-large", monday)
	why, until := e.svc.noCapacity([]domain.ModelChoice{{Provider: "fake", Model: "fake-large"}}, accounts, "demo", now)
	if !strings.Contains(why, "fake-large on claude-3 is at its own limit") || !strings.Contains(why, "claude-4 is at its 5-hour limit") || strings.Contains(why, "elsewhere") {
		t.Fatalf("why %q", why)
	}
	if !until.Equal(fiveHours) {
		t.Fatalf("the first to free up is claude-4 at %s, got %s", fiveHours, until)
	}

	e.svc.setWaiting(e.p, "I-59 review: "+why, until)
	w, u := e.svc.waitingOf(e.p)
	m := MachineView{Machine: domain.Machine{Name: "dma", Waiting: w, WaitingUntil: u}, Health: "green"}
	alerts := strings.Join(queueAlerts(e.p.Snapshot(), []MachineView{m}, now), "\n")
	if !strings.Contains(alerts, "work on dma waits for capacity: I-59 review") || !strings.Contains(alerts, "the first frees up at") {
		t.Fatalf("alerts %q", alerts)
	}
	// Once capacity is back the alert goes, even before the next heartbeat.
	if got := queueAlerts(e.p.Snapshot(), []MachineView{m}, until.Add(time.Minute)); len(got) != 0 {
		t.Fatalf("a wait that has passed still shows: %v", got)
	}
}

// When every model of the class is at its limit, the step runs one rung
// down the provider's ladder: never the model it must differ from, never
// the bottom rung.
func TestLadderFallback(t *testing.T) {
	e := newEnv(t)
	now := e.clock.Now()
	m := domain.ModelRouter{Ladders: map[string][]string{"claude": {"haiku", "sonnet", "opus", "fable"}}}
	accounts := []*domain.Account{{ID: "claude-1", Provider: "claude", CeilingPct: 100}}
	complex := []domain.ModelChoice{{Provider: "claude", Model: "fable", Effort: "high"}, {Provider: "claude", Model: "opus", Effort: "high"}}
	e.svc.markModelLimited("claude-1", "fable", now.Add(72*time.Hour))
	c, acc, ok := e.svc.ladderFallback(m, complex, "opus", accounts, "demo", now)
	if !ok || acc.ID != "claude-1" || c.Model != "sonnet" || c.Effort != "high" {
		t.Fatalf("a review that must differ from opus falls to sonnet: %+v %v", c, ok)
	}
	e.svc.markModelLimited("claude-1", "sonnet", now.Add(time.Hour))
	if c, _, ok := e.svc.ladderFallback(m, complex, "opus", accounts, "demo", now); ok {
		t.Fatalf("never down to the bottom rung: %+v", c)
	}
}
