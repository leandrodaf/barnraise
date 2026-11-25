package app

import (
	"slices"
	"strings"
	"testing"

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
