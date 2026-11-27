package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Two Barnraise not paired with each other adopt the same issue under their
// own ids: the issue's lease in the shared repository lets only one work.
func TestIssueLeaseKeepsOtherBarnraiseOut(t *testing.T) {
	e := newEnv(t)
	it := e.create(NewItem{Title: "From an issue", Kind: "feature", Criteria: crit(ears), Priority: "P1", TrackerRef: "#7"})
	if it.Status != domain.StatusReady {
		t.Fatalf("%s is %s", it.ID, it.Status)
	}
	other, err := e.arb.Claim(e.ctx, "issues/7", "someone-elses-machine")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.claim(e.ctx, e.p, it.ID, slot("implementer")); !errors.Is(err, ErrConflict) {
		t.Fatalf("claimed an issue another Barnraise works on: %v", err)
	}
	if e.item(it.ID).Status != domain.StatusReady {
		t.Fatal("the item must stay ready")
	}
	if info, _ := e.arb.Inspect(e.ctx, claimKey(it.ID)); info.Held {
		t.Fatal("the item's own lease was left behind")
	}
	if err := e.arb.Release(e.ctx, other); err != nil {
		t.Fatal(err)
	}
	e.start(it.ID)
	e.p.mu.Lock()
	l, held := e.p.issueLeases[it.ID]
	e.p.mu.Unlock()
	if !held || l.Key != "issues/7" {
		t.Fatalf("issue lease %+v %v", l, held)
	}
	e.svc.releaseLease(e.ctx, e.p, it.ID)
	if info, _ := e.arb.Inspect(e.ctx, "issues/7"); info.Held {
		t.Fatal("releasing the item frees its issue")
	}
}

func TestIssueClaimKey(t *testing.T) {
	for in, want := range map[string]string{"#134": "issues/134", "PROJ-12": "issues/PROJ-12", "": "", "a b": "", "../x": ""} {
		if got := issueClaimKey(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// A person who assigns the issue to themselves takes the task; the claim
// tells the tracker's readers who works on it and where.
func TestIssueHoldersAndClaimNotice(t *testing.T) {
	e := newEnv(t, withTracker())
	it := e.create(NewItem{Title: "From an issue", Kind: "feature", Criteria: crit(ears), Priority: "P1", TrackerRef: "#9"})
	e.tracker.SetItems([]ports.TrackerItem{{Ref: "#9", Title: "From an issue", Assignees: []string{"ana"}}})
	if _, err := e.svc.ImportFromTracker(e.ctx, "demo", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if got := e.item(it.ID).HeldBy; len(got) != 1 || got[0] != "ana" {
		t.Fatalf("held by %v", got)
	}
	if a, err := e.svc.NextWork(e.ctx, "demo", slot("implementer")); err == nil && a != nil && a.Item != nil {
		t.Fatalf("started %s while a person holds its issue", a.Item.ID)
	}
	e.tracker.SetItems([]ports.TrackerItem{{Ref: "#9", Title: "From an issue"}})
	if _, err := e.svc.ImportFromTracker(e.ctx, "demo", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if len(e.item(it.ID).HeldBy) != 0 {
		t.Fatal("unassigned: free again")
	}
	e.next("implementer", it.ID)
	deadline := time.Now().Add(2 * time.Second)
	for len(e.tracker.Comments()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	c := e.tracker.Comments()
	if len(c) != 1 || !strings.HasPrefix(c[0], "#9: ") || !strings.Contains(c[0], it.ID) || !strings.Contains(c[0], "status:in_progress") {
		t.Fatalf("claim notice %q", c)
	}
}
