package app

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// A step blocked only because other items have not landed waits for them:
// no question to the owner, and the step starts again once they are in.
func TestBlockedStepWaitsForItems(t *testing.T) {
	e := newEnv(t)
	dep := e.ready("simple", "Dawn at the twelfth horde", "combat")
	it := e.ready("simple", "Chronicle shows the first dawn", "lodge")
	e.start(it.ID)
	got := e.mustDeliver(it.ID, domain.Delivery{Status: domain.DeliveryBlocked, Reason: "main has no tests/dawn.gd yet", WaitsFor: []string{dep.ID, it.ID, "I-999"}})
	if got.Status != domain.StatusInProgress || got.WaitingQuestion != "" || !slices.Equal(got.Deps, []string{dep.ID}) {
		t.Fatalf("status %s question %q deps %v", got.Status, got.WaitingQuestion, got.Deps)
	}
	if qs := e.p.Snapshot().Questions; len(qs) != 0 {
		t.Fatalf("the owner was asked: %+v", qs)
	}
	if n := lastNote(t, e, domain.EvItemBlocked); !strings.HasPrefix(n, "waits for "+dep.ID) {
		t.Fatalf("blocked note %q", n)
	}
	p := e.p
	p.mu.Lock()
	held := e.svc.heldWorkLocked(p, slot("implementer"), nil)
	p.mu.Unlock()
	if held != nil && held.Item.ID == it.ID {
		t.Fatal("picked while what it waits for has not landed")
	}
	// The board shows it as planned, and says what it waits for.
	io := overviewItem(t, e, it.ID)
	if io.Activity != ActBlocked || io.Detail != "waits for "+dep.ID || io.Column != ColBacklog {
		t.Fatalf("overview %s %q %s", io.Activity, io.Detail, io.Column)
	}
	e.integrateFake(dep, 0, "")
	if io := overviewItem(t, e, it.ID); io.Activity == ActBlocked || io.Column == ColBacklog {
		t.Fatalf("once it landed: %s %q %s", io.Activity, io.Detail, io.Column)
	}
	p.mu.Lock()
	held = e.svc.heldWorkLocked(p, slot("implementer"), nil)
	p.mu.Unlock()
	if held == nil || held.Item.ID != it.ID || held.Step.Name != got.Step {
		t.Fatalf("not picked again once it landed: %+v", held)
	}
}

// Without items to wait for, or with items that would wait for each other,
// a block still asks the owner.
func TestBlockedStepWithoutItemsAsks(t *testing.T) {
	e := newEnv(t)
	a := e.ready("simple", "First", "x")
	b := e.ready("simple", "Second", "y")
	e.start(a.ID)
	e.mustDeliver(a.ID, domain.Delivery{Status: domain.DeliveryBlocked, Reason: "waits for Second", WaitsFor: []string{b.ID}})
	e.start(b.ID)
	got := e.mustDeliver(b.ID, domain.Delivery{Status: domain.DeliveryBlocked, Reason: "waits for First", WaitsFor: []string{a.ID}})
	if got.Status != domain.StatusBlocked || got.WaitingQuestion == "" || slices.Contains(got.Deps, a.ID) {
		t.Fatalf("two items waiting for each other: %s %q %v", got.Status, got.WaitingQuestion, got.Deps)
	}
}

func overviewItem(t *testing.T, e *env, id string) *ItemOverview {
	t.Helper()
	o, err := e.svc.Overview(e.ctx, owner(), OverviewQuery{Project: "demo", Item: id})
	if err != nil || len(o.Projects) != 1 || len(o.Projects[0].Items) != 1 {
		t.Fatalf("overview of %s: %+v %v", id, o, err)
	}
	return o.Projects[0].Items[0]
}

// An idle item says whether it waits for an account, for a busy agent, or
// starts at the next pass.
func TestIdleSaysWhy(t *testing.T) {
	e := newEnv(t)
	it := e.ready("simple", "Cards screen", "x")
	e.start(it.ID)
	cur := e.item(it.ID)
	cur.Claim = &domain.Claim{Machine: "m1"}
	now := e.clock.Now()
	snap := &domain.State{Accounts: map[string]*domain.Account{}}
	if got := idleWhy(e.p, snap, cur, now); got.Kind != "next_pass" {
		t.Fatalf("no accounts known: %+v", got)
	}
	reset := now.Add(90 * time.Minute)
	snap.Accounts["a"] = &domain.Account{ID: "a", Status: domain.AccountLimited, Usage: domain.Usage{FiveHourPct: 100, FiveHourResets: reset}}
	if got := idleWhy(e.p, snap, cur, now); got.Kind != "no_account" || !strings.Contains(got.Arg, reset.Local().Format("15:04")) {
		t.Fatalf("every account limited: %+v", got)
	}
	snap.Accounts["b"] = &domain.Account{ID: "b", Status: domain.AccountOK}
	role := e.p.Cfg().Roles["implementer"]
	snap.Sessions = map[string]*domain.SessionInfo{}
	for i := range role.Count {
		snap.Sessions[strconv.Itoa(i)] = &domain.SessionInfo{Agent: strconv.Itoa(i), Role: "implementer", Machine: "m1", State: "working", At: now}
	}
	if got := idleWhy(e.p, snap, cur, now); got.Kind != "slots_busy" {
		t.Fatalf("every implementer busy: %+v", got)
	}
}
