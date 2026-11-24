package app

import (
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// overviewItem reads one item as the owner's board gets it.
func (e *env) overviewItem(id string) *ItemOverview {
	e.t.Helper()
	o, err := e.svc.Overview(e.ctx, owner(), OverviewQuery{Project: "demo", Item: id, Brief: true})
	if err != nil {
		e.t.Fatal(err)
	}
	for _, p := range o.Projects {
		for _, io := range p.Items {
			if io.ID == id {
				return io
			}
		}
	}
	e.t.Fatalf("%s is not in the overview", id)
	return nil
}

// The board's facts come from the flow of the project, not from the
// panel: the steps and where the item is, what the owner approves now, the
// column and the signal. They follow a planned item from ready to the
// owner's approval of its plan and back to work.
func TestBoardFollowsTheFlow(t *testing.T) {
	e := newEnv(t)
	e.account("fake-1")
	e.heartbeat("m1") // a machine that is up: its items are not stalled
	it := e.ready("planned", "Big change", "core")

	io := e.overviewItem(it.ID)
	if io.Column != ColBacklog || io.Signal.Kind != "ready" || io.NeedsOwner || io.Awaits != "" {
		t.Fatalf("a ready item: column %q signal %+v needs %v awaits %q", io.Column, io.Signal, io.NeedsOwner, io.Awaits)
	}
	kinds := ""
	for _, s := range io.Flow {
		kinds += s.Kind + ":" + s.State + " "
	}
	if kinds != "plan:next approval:next work:next merge:next " {
		t.Fatalf("flow of a ready item: %s", kinds)
	}

	e.next("implementer", it.ID)
	if io = e.overviewItem(it.ID); io.Column != ColWorking || io.Flow[0].State != "current" {
		t.Fatalf("while planning: column %q flow %+v", io.Column, io.Flow)
	}
	e.mustDeliver(it.ID, domain.Delivery{})
	e.drive(it.ID)
	io = e.overviewItem(it.ID)
	if io.Awaits != "plan" || !io.NeedsOwner || io.Column != ColNeeds || io.Signal != (Signal{Kind: "approve", Arg: "plan"}) {
		t.Fatalf("at the owner's approval: awaits %q needs %v column %q signal %+v", io.Awaits, io.NeedsOwner, io.Column, io.Signal)
	}
	if io.Flow[0].State != "done" || io.Flow[1].State != "current" || io.Flow[1].Kind != "approval" {
		t.Fatalf("flow at the approval: %+v", io.Flow)
	}
	// What the board says is what Approve accepts.
	wantErr(t, e.svc.Approve(e.ctx, "demo", owner(), it.ID, "delivery", true, ""), ErrConflict)
	if err := e.svc.Approve(e.ctx, "demo", owner(), it.ID, io.Awaits, true, ""); err != nil {
		t.Fatal(err)
	}
	if io = e.overviewItem(it.ID); io.NeedsOwner || io.Awaits != "" || io.Column == ColNeeds {
		t.Fatalf("after the approval: needs %v awaits %q column %q", io.NeedsOwner, io.Awaits, io.Column)
	}
}

// A review step has its own column; a failed check is the first thing said.
func TestBoardColumnsAndSignals(t *testing.T) {
	e := newEnv(t)
	e.account("fake-1")
	e.account("fake-2")
	e.heartbeat("m1")
	it := e.ready("reviewed", "Cards screen", "cards")
	e.next("implementer", it.ID)
	e.mustDeliver(it.ID, domain.Delivery{})
	e.drive(it.ID)
	if io := e.overviewItem(it.ID); io.Column != ColReview || io.Flow[1].Kind != "review" || io.Flow[1].State != "current" {
		t.Fatalf("at review: column %q flow %+v", io.Column, io.Flow)
	}

	io := &ItemOverview{Activity: ActIdle, Machine: "m1"}
	item := &domain.Item{Status: domain.StatusInProgress, Step: "implement"}
	il := &domain.ItemLedger{Checks: map[string]*domain.CheckMark{
		"unit": {Check: "unit", Passed: true},
		"lint": {Check: "lint", Passed: false},
		"e2e":  {Check: "e2e", Passed: false, Flaky: true},
	}}
	if s := itemSignal(item, io, il); s != (Signal{Kind: "check_failed", Arg: "lint"}) {
		t.Fatalf("a failed check: %+v", s)
	}
	il = &domain.ItemLedger{LastReview: &domain.ReviewMark{Step: "review", Decision: "REWORK", Findings: 3}}
	if s := itemSignal(item, io, il); s != (Signal{Kind: "changes_requested", Arg: "3"}) {
		t.Fatalf("back from review: %+v", s)
	}
	if s := itemSignal(item, io, nil); s != (Signal{Kind: "idle", Arg: "m1"}) {
		t.Fatalf("nothing runs: %+v", s)
	}
}

// The overview adds the projects' usage up, so a reader never sums it.
func TestOverviewAddsUsageUp(t *testing.T) {
	a := domain.UsageBy{"opus": {Tokens: 10, CostUSD: 1}}
	b := domain.UsageBy{"opus": {Tokens: 5, CostUSD: 0.5}, "haiku": {Tokens: 1}}
	sum := domain.UsageBy{}
	sum.Add(a)
	sum.Add(b)
	if sum["opus"].Tokens != 15 || sum["opus"].CostUSD != 1.5 || sum["haiku"].Tokens != 1 {
		t.Fatalf("sum %+v %+v", sum["opus"], sum["haiku"])
	}
	if a["opus"].Tokens != 10 {
		t.Fatal("adding changed a project's own totals")
	}
}
