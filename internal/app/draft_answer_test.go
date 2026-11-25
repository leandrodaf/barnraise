package app

import (
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// A draft whose analyst asked the owner something stays a draft once the
// owner answers: without criteria, and with its [NEEDS CLARIFICATION] still
// open, it cannot enter the flow (it did, and was integrated with 0/0).
func TestAnswerKeepsAnUnreadyDraftADraft(t *testing.T) {
	e := newEnv(t)
	for _, choice := range []string{"keep the current table", "accept"} {
		res, err := e.svc.CreateItem(e.ctx, "demo", owner(), NewItem{Title: "One table per kind " + choice, Kind: "simple", Area: "x",
			Body: "From proposal P-1.\n\n[NEEDS CLARIFICATION: analyst writes EARS criteria]"})
		if err != nil || res.Item == nil {
			t.Fatal(res, err)
		}
		id := res.Item.ID
		e.wantStatus(id, domain.StatusDraft, "")
		q, err := e.svc.AskOwner(e.ctx, "demo", agent("analyst", id, RefineStep), domain.Question{ItemID: id, Context: "Which plan?",
			Decision: "Keep the table?", Options: []domain.Option{{Label: "keep the current table", Recommended: true}, {Label: "accept"}}, IfNoAnswer: "It waits."})
		if err != nil {
			t.Fatal(err)
		}
		e.wantStatus(id, domain.StatusWaitingOwner, "")
		if _, err := e.svc.Answer(e.ctx, "demo", owner(), q.ID, choice, ""); err != nil {
			t.Fatal(err)
		}
		if got := e.wantStatus(id, domain.StatusDraft, ""); got.WaitingQuestion != "" {
			t.Fatalf("%q: still waits for %s", choice, got.WaitingQuestion)
		}
	}
}
