package app

import (
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// A fix an item cannot be delivered without is not paperwork: it becomes an
// item at once, the result names it to wait for, and no cap stops it.
func TestBlockingProposalBecomesAnItem(t *testing.T) {
	e := newEnv(t)
	review := e.ready("simple", "Review the joined Lodge", "lodge")
	e.next("implementer", review.ID)
	a := agent("implementer", review.ID, "implement")

	fix := prop("Fix the naming-screen Escape", "lodge")
	fix.Kind, fix.Blocks = "bug", true
	res, err := e.svc.Propose(e.ctx, "demo", a, fix)
	if err != nil {
		t.Fatal(err)
	}
	if res.Item == "" || res.Proposal == nil || res.Proposal.Status != domain.ProposalApproved || res.Proposal.ItemID != res.Item {
		t.Fatalf("blocking proposal: %+v", res)
	}
	got := e.item(res.Item)
	if got.Kind != "bug" || got.Status != domain.StatusDraft {
		t.Fatalf("item %+v", got)
	}
	if p := e.p.Snapshot().Proposals[res.Proposal.ID]; p.FromItem != review.ID || !p.Blocks {
		t.Fatalf("proposal %+v", p)
	}

	// The same fix again: the item that covers it, not a second one.
	again, err := e.svc.Propose(e.ctx, "demo", a, fix)
	if err != nil || again.Item != res.Item {
		t.Fatalf("again: %+v %v", again, err)
	}

	// Many findings in a day are fine: what is not needed is weighed by
	// triage, not counted.
	for i, title := range []string{"Contrast of the ledger", "Store route line", "Keeper column", "Plan check mark"} {
		r, err := e.svc.Propose(e.ctx, "demo", a, prop(title, "lodge"))
		if err != nil || r.Proposal == nil || r.Proposal.Status != domain.ProposalOpen || r.Item != "" {
			t.Fatalf("finding %d: %+v %v", i, r, err)
		}
	}

	// Blocks means something only from an item's session.
	loose := prop("A new menu", "lodge")
	loose.Blocks = true
	r, err := e.svc.Propose(e.ctx, "demo", agent("analyst", "", ""), loose)
	if err != nil || r.Item != "" || r.Proposal == nil || r.Proposal.Status != domain.ProposalOpen || r.Proposal.Blocks {
		t.Fatalf("outside an item: %+v %v", r, err)
	}
}
