package app

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

func reconcileOn(c *domain.ProjectConfig) { c.Tracker.Reconcile = true }

// moveTo moves an item without the flow, as another machine's change arrives.
func (e *env) moveTo(id string, st domain.Status) *domain.Item {
	e.t.Helper()
	it, err := e.svc.mutateItem(e.ctx, e.p, id, domain.EvItemUpdated, "test", func(x *domain.Item) error {
		x.Status = st
		if st == domain.StatusIntegrated {
			x.IntegratedAt = e.clock.Now()
		}
		return nil
	}, domain.Payload{})
	if err != nil {
		e.t.Fatal(err)
	}
	return it
}

func TestRepairCreatesTheIssueAWriteNeverMade(t *testing.T) {
	e := newEnv(t, withTracker(), withCfg(reconcileOn))
	e.tracker.CreateErr = errors.New("github: 404 Not Found")
	it := e.create(NewItem{Title: "Bell defeat", Kind: "feature", Area: "arena", Priority: "P1"})
	eventually(t, "the failed create is remembered", func() bool { return e.svc.TrackerErr("demo") != "" })
	e.clock.Advance(11 * time.Minute)
	st, err := e.svc.GetStatus(e.ctx, "demo", owner())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(st.Alerts, func(a string) bool { return strings.Contains(a, "1 items have no issue in the tracker: "+it.ID) }) {
		t.Fatalf("alerts %q", st.Alerts)
	}

	e.tracker.CreateErr = nil
	res, err := e.svc.RepairTracker(e.ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Created, []string{it.ID}) || e.item(it.ID).TrackerRef == "" {
		t.Fatalf("repair %+v, ref %q", res, e.item(it.ID).TrackerRef)
	}
	st, _ = e.svc.GetStatus(e.ctx, "demo", owner())
	for _, a := range st.Alerts {
		if strings.Contains(a, "no issue") {
			t.Fatalf("alert stays: %q", a)
		}
	}
}

func TestRepairLinksBackBarnraisesOwnIssueInsteadOfADuplicate(t *testing.T) {
	e := newEnv(t, withTracker(), withCfg(reconcileOn))
	e.tracker.CreateErr = errors.New("timeout")
	a := e.create(NewItem{Title: "Iron", Kind: "feature", Area: "field", Priority: "P1"})
	b := e.create(NewItem{Title: "Militia", Kind: "feature", Area: "field", Priority: "P1"})
	eventually(t, "both creates failed", func() bool { return e.tracker.CreateAttempts() >= 2 && e.svc.TrackerErr("demo") != "" })
	e.tracker.SetCreateErr(nil)
	e.tracker.SetItems([]ports.TrackerItem{
		// Barnraise's own issue for a: its label proves it.
		{Ref: "#7", Title: "[" + a.ID + "] Iron", Labels: []string{"barnraise", "status:" + string(e.item(a.ID).Status)}, Status: string(e.item(a.ID).Status)},
		// Anyone can type a title: without the label it is not taken.
		{Ref: "#8", Title: "[" + b.ID + "] Militia", Labels: []string{"bug"}},
	})
	e.clock.Advance(3 * time.Minute)
	res, err := e.svc.RepairTracker(e.ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Adopted, []string{a.ID}) || !slices.Equal(res.Created, []string{b.ID}) {
		t.Fatalf("%+v", res)
	}
	if e.item(a.ID).TrackerRef != "#7" || e.item(b.ID).TrackerRef == "#8" {
		t.Fatalf("refs %q %q", e.item(a.ID).TrackerRef, e.item(b.ID).TrackerRef)
	}
	if slices.Contains(e.tracker.Created(), a.ID) {
		t.Fatal("a duplicate issue was opened")
	}
}

func TestRepairUpdatesStaleIssuesOnceAndLeavesFreshOnesToTheFastPath(t *testing.T) {
	e := newEnv(t, withTracker(), withCfg(reconcileOn))
	it := e.create(NewItem{Title: "Bell", Kind: "feature", Area: "arena", Priority: "P1"})
	eventually(t, "issue created", func() bool { return e.item(it.ID).TrackerRef != "" })
	ref := e.item(it.ID).TrackerRef
	// The item was integrated on a machine whose update never arrived.
	e.moveTo(it.ID, domain.StatusIntegrated)
	e.tracker.Items = []ports.TrackerItem{{Ref: ref, Title: "[" + it.ID + "] Bell", Labels: []string{"barnraise"}, Status: "draft"}}

	res, _ := e.svc.RepairTracker(e.ctx, "demo")
	if res.Changed() {
		t.Fatalf("a change younger than the grace is the fast path's: %+v", res)
	}
	e.clock.Advance(3 * time.Minute)
	before := len(e.tracker.Updated())
	res, err := e.svc.RepairTracker(e.ctx, "demo")
	if err != nil || !slices.Equal(res.Updated, []string{it.ID}) {
		t.Fatalf("%+v %v", res, err)
	}
	if u := e.tracker.Updated(); len(u) != before+1 || u[len(u)-1] != it.ID+":integrated" {
		t.Fatalf("updates %q", u)
	}
	// The fake tracker still shows draft: the same version is not written again.
	res, _ = e.svc.RepairTracker(e.ctx, "demo")
	if res.Changed() {
		t.Fatalf("written twice: %+v", res)
	}
	// In sync: nothing to do.
	e.tracker.Items[0].Status, e.tracker.Items[0].Closed = "integrated", true
	e.moveTo(it.ID, domain.StatusIntegrated)
	e.clock.Advance(3 * time.Minute)
	if res, _ := e.svc.RepairTracker(e.ctx, "demo"); res.Changed() {
		t.Fatalf("in sync but written: %+v", res)
	}
}

func TestRepairLeavesOldHistoryOutAndFailsWhenNothingGoesThrough(t *testing.T) {
	e := newEnv(t, withTracker(), withCfg(reconcileOn))
	e.tracker.CreateErr = errors.New("403 Resource not accessible")
	old := e.create(NewItem{Title: "Old", Kind: "feature", Area: "arena", Priority: "P1"})
	e.moveTo(old.ID, domain.StatusIntegrated)
	e.clock.Advance(8 * 24 * time.Hour)
	live := e.create(NewItem{Title: "Live", Kind: "feature", Area: "arena", Priority: "P1"})
	e.clock.Advance(3 * time.Minute)
	res, err := e.svc.RepairTracker(e.ctx, "demo")
	if err == nil || !strings.Contains(err.Error(), live.ID) || slices.Contains(res.Failed, old.ID) {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestTrackerAlertsNameTheMachineThatCannotWrite(t *testing.T) {
	e := newEnv(t, withTracker())
	ms := []MachineView{
		{Machine: domain.Machine{Name: "dma", TrackerErr: "github: 404 Not Found"}, Health: "green"},
		{Machine: domain.Machine{Name: "gone", TrackerErr: "x"}, Health: "red"},
	}
	got := e.svc.trackerAlerts(e.p, e.p.Snapshot(), ms)
	if len(got) != 1 || !strings.Contains(got[0], "machine dma cannot write to the tracker (github: 404 Not Found)") {
		t.Fatalf("%q", got)
	}
}

func TestRepairDutyStepsAsideAfterAFailure(t *testing.T) {
	e := newEnv(t, withTracker(), withCfg(reconcileOn))
	e.tracker.SetListErr(errors.New("401 Bad credentials"))
	r := e.svc.Runtime()
	now := time.Now()
	r.repairTracker(e.ctx, e.p, now)
	eventually(t, "down after the failed repair", func() bool { return e.svc.trackerDown("demo", time.Now()) })
	eventually(t, "duty released", func() bool { return e.backend.Holder(TrackerKey) == "" })
	r.repairTracker(e.ctx, e.p, now.Add(trackerRepairEvery+time.Second))
	if e.backend.Holder(TrackerKey) != "" {
		t.Fatal("a machine that cannot write took the duty again")
	}
	if !strings.Contains(e.svc.TrackerErr("demo"), "401") {
		t.Fatal(e.svc.TrackerErr("demo"))
	}
}
