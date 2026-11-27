package app

import (
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func TestDeliverValidation(t *testing.T) {
	e := newEnv(t)
	it := e.ready("simple", "Cards", "cards")
	_, err := e.deliver(it.ID, domain.Delivery{})
	wantErr(t, err, ErrConflict) // not started: a late (or early) delivery
	e.next("implementer", it.ID)
	cases := []struct {
		name string
		a    Actor
		d    domain.Delivery
		err  error
	}{
		{"no summary", agent("implementer", it.ID, "implement"), domain.Delivery{Status: domain.DeliveryDone, Summary: " "}, ErrInvalid},
		{"bad status", agent("implementer", it.ID, "implement"), domain.Delivery{Status: "FINISHED", Summary: "x"}, ErrInvalid},
		{"needs context without reason", agent("implementer", it.ID, "implement"), domain.Delivery{Status: domain.DeliveryNeedsContext, Summary: "x"}, ErrInvalid},
		{"concerns without concerns", agent("implementer", it.ID, "implement"), domain.Delivery{Status: domain.DeliveryConcerns, Summary: "x"}, ErrInvalid},
		{"unknown criterion", agent("implementer", it.ID, "implement"), domain.Delivery{Status: domain.DeliveryDone, Summary: "x", Criteria: []domain.CriterionMark{{ID: "C9"}}}, ErrInvalid},
		{"met without evidence", agent("implementer", it.ID, "implement"), domain.Delivery{Status: domain.DeliveryDone, Summary: "x", Criteria: []domain.CriterionMark{{ID: "C1", Met: true}}}, ErrInvalid},
		{"wrong step", agent("implementer", it.ID, ""), domain.Delivery{Step: "check", Status: domain.DeliveryDone, Summary: "x"}, ErrConflict},
		{"session of another step", agent("implementer", it.ID, "plan"), domain.Delivery{Status: domain.DeliveryDone, Summary: "x"}, ErrForbidden},
		{"another item", agent("implementer", "I-99", "implement"), domain.Delivery{ItemID: it.ID, Status: domain.DeliveryDone, Summary: "x"}, ErrForbidden},
		{"no scope", Actor{Kind: ActorAgent, Name: "r", Scopes: []string{ScopeReview}}, domain.Delivery{ItemID: it.ID, Status: domain.DeliveryDone, Summary: "x"}, ErrForbidden},
		{"bad id", owner(), domain.Delivery{ItemID: "x", Status: domain.DeliveryDone, Summary: "x"}, ErrInvalid},
		{"unknown item", owner(), domain.Delivery{ItemID: "I-404", Status: domain.DeliveryDone, Summary: "x"}, ErrNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.d.ItemID == "" && c.a.ItemID == "" {
				c.d.ItemID = it.ID
			}
			_, err := e.svc.Deliver(e.ctx, "demo", c.a, c.d)
			wantErr(t, err, c.err)
		})
	}
	_, err = e.svc.Deliver(e.ctx, "nope", owner(), domain.Delivery{})
	wantErr(t, err, ErrUnknownProj)
	// The item id defaults to the session's item.
	got, err := e.svc.Deliver(e.ctx, "demo", agent("implementer", it.ID, "implement"), domain.Delivery{Status: domain.DeliveryConcerns, Summary: "done", Concerns: []string{"slow on old phones"},
		Criteria: []domain.CriterionMark{{ID: "C1", Met: true, Evidence: "ok"}}, Files: []string{"app/cards.go"}})
	if err != nil || got.Step != "check" || got.Status != domain.StatusInProgress || !got.Criteria[0].Met || got.FilesTouched[0] != "app/cards.go" || got.Runs["implement"].Account != "fake-1" || got.Runs["implement"].Provider != "fake" {
		t.Fatalf("delivered %+v %v", got, err)
	}
	_, err = e.deliver(it.ID, domain.Delivery{Step: "implement"})
	wantErr(t, err, ErrConflict) // a second delivery for the same step is late
	_, err = e.svc.Deliver(e.ctx, "demo", owner(), domain.Delivery{ItemID: it.ID, Status: domain.DeliveryDone, Summary: "x"})
	wantErr(t, err, ErrInvalid) // the check step takes no delivery
}

func TestDeliverReproduceNeedsTheTestFile(t *testing.T) {
	e := newEnv(t)
	bug := e.ready("bug", "Crash", "login")
	e.next("implementer", bug.ID)
	_, err := e.deliver(bug.ID, domain.Delivery{})
	wantErr(t, err, ErrInvalid)
	got := e.mustDeliver(bug.ID, domain.Delivery{Files: []string{"tests/repro.sh"}})
	if got.Step != "implement" {
		t.Fatal(got.Step)
	}
}

func TestDeliverNeedsContextAndBlocked(t *testing.T) {
	e := newEnv(t)
	it := e.ready("simple", "Export", "export")
	e.next("implementer", it.ID)
	got := e.mustDeliver(it.ID, domain.Delivery{Status: domain.DeliveryNeedsContext, Reason: "which CSV separator?"})
	if got.Status != domain.StatusWaitingOwner || got.WaitingQuestion == "" {
		t.Fatalf("needs context %+v", got)
	}
	q := e.question(it.ID)
	if q.Decision != "which CSV separator?" || q.Recommended() != "Answer and continue" {
		t.Fatalf("question %+v", q)
	}
	if _, err := e.svc.Answer(e.ctx, "demo", owner(), strings.ToLower(q.ID), "answer", "semicolon"); err != nil {
		t.Fatal(err)
	}
	got = e.wantStatus(it.ID, domain.StatusInProgress, "implement")
	if got.WaitingQuestion != "" {
		t.Fatal("the question is answered")
	}
	inbox, _ := e.svc.Inbox(e.ctx, "demo", "x", "implementer", it.ID, false)
	if len(inbox) == 0 || inbox[len(inbox)-1].Text != "Owner answered: Answer and continue — semicolon" {
		t.Fatalf("the answer goes to the agent: %+v", inbox)
	}

	got = e.mustDeliver(it.ID, domain.Delivery{Status: domain.DeliveryBlocked, Reason: "no access to the bucket"})
	if got.Status != domain.StatusBlocked || got.WaitingQuestion == "" {
		t.Fatalf("blocked %+v", got)
	}
	if !strings.Contains(e.types(it.ID), domain.EvItemBlocked) {
		t.Fatal("no item.blocked event")
	}
	q = e.question(it.ID)
	if q.Recommended() != "Retry the step" || !q.Urgent {
		t.Fatalf("question %+v", q)
	}
	if _, err := e.svc.Answer(e.ctx, "demo", owner(), q.ID, "retry", ""); err != nil {
		t.Fatal(err)
	}
	e.wantStatus(it.ID, domain.StatusInProgress, "implement")
	_, err := e.svc.Answer(e.ctx, "demo", owner(), q.ID, "retry", "")
	wantErr(t, err, ErrConflict)
	_, err = e.svc.Answer(e.ctx, "demo", owner(), "Q-99", "retry", "")
	wantErr(t, err, ErrNotFound)
	_, err = e.svc.Answer(e.ctx, "demo", Actor{Kind: ActorAgent, Scopes: []string{ScopeDeliver}}, q.ID, "retry", "")
	wantErr(t, err, ErrForbidden)
	_, err = e.svc.Answer(e.ctx, "nope", owner(), q.ID, "retry", "")
	wantErr(t, err, ErrUnknownProj)
}

// A different reviewer model is a preference, never a wait: when only the
// implementer's model could run, its fresh session reviews, and the review
// counts instead of being thrown away after it ran.
func TestReviewBySameModelCounts(t *testing.T) {
	e := newEnv(t)
	it := e.ready("reviewed", "Cards", "")
	e.next("implementer", it.ID)
	e.mustDeliver(it.ID, domain.Delivery{})
	if _, err := e.svc.Review(e.ctx, "demo", agent("reviewer", it.ID, "review"), approveReview("same model, fresh session")); err != nil {
		t.Fatalf("a review by the same model was refused: %v", err)
	}
}

func TestReviewValidation(t *testing.T) {
	e := newEnv(t, withCfg(func(c *domain.ProjectConfig) {
		c.Areas = map[string]domain.AreaDef{"cards": {Name: "cards", Paths: []string{"app/cards/**"}}, "wallet": {Name: "wallet", Paths: []string{"app/wallet/**"}}}
	}))
	it := e.ready("reviewed", "Cards", "cards")
	_, err := e.review(it.ID, approveReview("early"))
	wantErr(t, err, ErrConflict)
	e.next("implementer", it.ID)
	_, err = e.review(it.ID, approveReview("not yet"))
	wantErr(t, err, ErrInvalid) // implement takes a delivery
	e.mustDeliver(it.ID, domain.Delivery{})
	_, err = e.deliver(it.ID, domain.Delivery{})
	wantErr(t, err, ErrInvalid) // review takes a review

	wrongRole := agent("critic", it.ID, "review")
	wrongRole.Family = "other"
	_, err = e.svc.Review(e.ctx, "demo", wrongRole, approveReview("x"))
	wantErr(t, err, ErrForbidden)
	for _, r := range []domain.Review{
		{Criteria: "maybe", Code: domain.VerdictPass, Decision: domain.ReviewApprove, Summary: "x"},
		{Criteria: domain.VerdictFail, Code: domain.VerdictPass, Decision: domain.ReviewApprove, Summary: "x"},
		{Criteria: domain.VerdictFail, Code: domain.VerdictPass, Decision: domain.ReviewRework, Summary: "x"},
		// A missing summary is filled, not refused (TestReviewFillSummary).
	} {
		_, err := e.review(it.ID, r)
		wantErr(t, err, ErrInvalid)
	}
	_, err = e.svc.Review(e.ctx, "demo", owner(), domain.Review{ItemID: "x"})
	wantErr(t, err, ErrInvalid)
	_, err = e.svc.Review(e.ctx, "demo", owner(), approveReviewFor("I-404"))
	wantErr(t, err, ErrNotFound)
	_, err = e.svc.Review(e.ctx, "demo", Actor{Kind: ActorAgent, Name: "x", Scopes: []string{ScopeDeliver}}, approveReviewFor(it.ID))
	wantErr(t, err, ErrForbidden)
	_, err = e.svc.Review(e.ctx, "demo", agent("reviewer", "I-77", "review"), approveReviewFor(it.ID))
	wantErr(t, err, ErrForbidden)
	_, err = e.svc.Review(e.ctx, "nope", owner(), approveReviewFor(it.ID))
	wantErr(t, err, ErrUnknownProj)

	// Findings of another area become proposals; with nothing left here, it approves.
	r := domain.Review{Criteria: domain.VerdictPass, Code: domain.VerdictFail, Decision: domain.ReviewRework, Summary: "the wallet has a leak",
		Findings: []domain.Finding{{File: "app/wallet/balance.go", Line: 12, Text: "balance is cached forever"}}}
	got, err := e.review(it.ID, r)
	if err != nil || got.Step != "integrate" || got.Status != domain.StatusQueued {
		t.Fatalf("other-area findings approve: %+v %v", got, err)
	}
	props, _, _ := e.svc.ListProposals("demo", owner(), false)
	if len(props) != 1 || props[0].Area != "wallet" || props[0].Kind != "improvement" || !strings.Contains(props[0].Evidence, "app/wallet/balance.go:12") {
		t.Fatalf("proposals %+v", props)
	}
	if got.Runs["review"].Family != "other" {
		t.Fatalf("review run %+v", got.Runs)
	}
}

func approveReviewFor(id string) domain.Review {
	r := approveReview("fine")
	r.ItemID = id
	return r
}

func TestReworkLimitEscalationAndOwner(t *testing.T) {
	e := newEnv(t, withHost())
	it := e.ready("reviewed", "Wallet", "wallet")
	e.next("implementer", it.ID)
	tiers := []int{0, 1}
	for round, tier := range tiers {
		e.mustDeliver(it.ID, domain.Delivery{})
		got, err := e.review(it.ID, reworkReview("the balance is wrong"))
		if err != nil {
			t.Fatal(err)
		}
		if got.Step != "implement" || got.Status != domain.StatusInProgress || len(got.Rework) != round+1 || got.Tier != tier {
			t.Fatalf("round %d: %+v", round+1, got)
		}
		rr := got.Rework[round]
		if rr.From != "review" || rr.To != "implement" || rr.Outcome != "rework" || !strings.Contains(rr.Reason, "the balance is wrong") {
			t.Fatalf("rework record %+v", rr)
		}
	}
	inbox, _ := e.svc.Inbox(e.ctx, "demo", "x", "", it.ID, false)
	if len(inbox) != 2 || !strings.HasPrefix(inbox[1].Text, "Rework round 2 from review") {
		t.Fatalf("rework messages %+v", inbox)
	}
	e.mustDeliver(it.ID, domain.Delivery{})
	got, err := e.review(it.ID, reworkReview("still wrong"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusWaitingOwner || got.WaitingQuestion == "" || len(got.Rework) != 2 {
		t.Fatalf("exhausted %+v", got)
	}
	q := e.question(it.ID)
	if q.Recommended() != "Retry with a stronger model" || len(q.Options) != 4 || !strings.Contains(q.Context, "came back 2 times") {
		t.Fatalf("question %+v", q)
	}
	if kinds := strings.Join(e.notes.Kinds(), ","); !strings.Contains(kinds, "rework_exhausted") {
		t.Fatalf("notifications %s", kinds)
	}
	props, _, _ := e.svc.ListProposals("demo", owner(), false)
	if len(props) != 1 || props[0].Kind != "self_improvement" || !strings.Contains(props[0].Title, "fix review") {
		t.Fatalf("the exhausted rework becomes a self-improvement proposal: %+v", props)
	}
	if _, err := e.svc.Answer(e.ctx, "demo", owner(), q.ID, "1", ""); err != nil {
		t.Fatal(err)
	}
	got = e.wantStatus(it.ID, domain.StatusInProgress, "implement")
	if got.Tier != 2 || len(got.Rework) != 0 {
		t.Fatalf("stronger model with a fresh budget: tier %d, rework %d", got.Tier, len(got.Rework))
	}
}

// exhaust runs a reviewed item until the owner is asked.
func (e *env) exhaust(title string) (*domain.Item, *domain.Question) {
	e.t.Helper()
	it := e.ready("reviewed", title, "a")
	e.start(it.ID)
	for range 3 {
		e.mustDeliver(it.ID, domain.Delivery{})
		if _, err := e.review(it.ID, reworkReview("wrong")); err != nil {
			e.t.Fatal(err)
		}
	}
	e.wantStatus(it.ID, domain.StatusWaitingOwner, "implement")
	return it, e.question(it.ID)
}

func TestAnswerPaths(t *testing.T) {
	e := newEnv(t)
	it, q := e.exhaust("accept")
	if _, err := e.svc.Answer(e.ctx, "demo", owner(), q.ID, "Accept", "known limitation"); err != nil {
		t.Fatal(err)
	}
	e.wantStatus(it.ID, domain.StatusQueued, "integrate")

	it, q = e.exhaust("split")
	if _, err := e.svc.Answer(e.ctx, "demo", owner(), q.ID, "split", ""); err != nil {
		t.Fatal(err)
	}
	if got := e.wantStatus(it.ID, domain.StatusClosed, ""); got.ClosedReason != "split" {
		t.Fatal(got.ClosedReason)
	}

	it, q = e.exhaust("abandon")
	got, err := e.svc.Answer(e.ctx, "demo", owner(), q.ID, "4", "not worth it")
	if err != nil || got.Answer != "Abandon the item — not worth it" || got.AnsweredBy != "ana" {
		t.Fatalf("answer %+v %v", got, err)
	}
	if got := e.wantStatus(it.ID, domain.StatusClosed, ""); got.ClosedReason != "abandoned" {
		t.Fatal(got.ClosedReason)
	}

	_, q = e.exhaust("bad answer")
	_, err = e.svc.Answer(e.ctx, "demo", owner(), q.ID, "maybe", "")
	wantErr(t, err, ErrInvalid)

	// An answer to a question the item no longer waits for moves nothing.
	it, q = e.exhaust("moved on")
	if _, err := e.svc.Redo(e.ctx, "demo", owner(), it.ID, "implement", "start over"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Answer(e.ctx, "demo", owner(), q.ID, "abandon", ""); err != nil {
		t.Fatal(err)
	}
	e.wantStatus(it.ID, domain.StatusInProgress, "implement")
}

func TestAccessAndRetryWithoutClaim(t *testing.T) {
	e := newEnv(t)
	it := e.ready("simple", "x", "x")
	e.next("implementer", it.ID)
	e.mustDeliver(it.ID, domain.Delivery{Status: domain.DeliveryBlocked, Reason: "stuck"})
	q := e.question(it.ID)
	// The machine left meanwhile: a retry puts the item back in the queue.
	if _, err := e.svc.MachineLeave(e.ctx, "demo", owner()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Answer(e.ctx, "demo", owner(), q.ID, "retry", ""); err != nil {
		t.Fatal(err)
	}
	e.wantStatus(it.ID, domain.StatusReady, "implement")
}

func TestApproveWhat(t *testing.T) {
	e := newEnv(t)
	post := e.ready("post", "Launch post", "blog")
	e.next("implementer", post.ID)
	e.mustDeliver(post.ID, domain.Delivery{})
	err := e.svc.Approve(e.ctx, "demo", owner(), post.ID, "delivery", true, "")
	if err != nil {
		t.Fatalf("approve before the question is asked: %v", err)
	}
	e.wantStatus(post.ID, domain.StatusQueued, "integrate")

	post = e.ready("post", "Second post", "blog")
	e.next("implementer", post.ID)
	e.mustDeliver(post.ID, domain.Delivery{})
	e.drive(post.ID)
	got := e.wantStatus(post.ID, domain.StatusWaitingOwner, "owner")
	q := e.question(post.ID)
	if q.Decision != "Approve the delivery of "+post.ID+"?" {
		t.Fatal(q.Decision)
	}
	e.drive(post.ID) // asking twice asks once
	if len(e.p.Snapshot().OpenQuestions()) != 1 {
		t.Fatal("one question per approval")
	}
	eventually(t, "owner approval notification", func() bool { return strings.Contains(strings.Join(e.notes.Kinds(), ","), "owner_approval") })
	wantErr(t, e.svc.Approve(e.ctx, "demo", owner(), post.ID, "", true, ""), ErrInvalid)
	wantErr(t, e.svc.Approve(e.ctx, "demo", owner(), post.ID, "plan", true, ""), ErrConflict)
	if err := e.svc.Approve(e.ctx, "demo", owner(), post.ID, "delivery", true, "lovely"); err != nil {
		t.Fatal(err)
	}
	e.wantStatus(post.ID, domain.StatusQueued, "integrate")
	if qq := e.p.Snapshot().Questions[got.WaitingQuestion]; qq.Open() || !strings.Contains(qq.Answer, "approved") {
		t.Fatalf("the approval question is closed: %+v", qq)
	}

	plan := e.ready("planned", "Big change", "core")
	e.next("implementer", plan.ID)
	e.mustDeliver(plan.ID, domain.Delivery{})
	e.drive(plan.ID)
	q = e.question(plan.ID)
	if q.Decision != "Approve the plan of "+plan.ID+"?" {
		t.Fatal(q.Decision)
	}
	wantErr(t, e.svc.Approve(e.ctx, "demo", owner(), plan.ID, "delivery", true, ""), ErrConflict)
	if err := e.svc.Approve(e.ctx, "demo", owner(), plan.ID, "plan", false, "split the migration"); err != nil {
		t.Fatal(err)
	}
	got = e.wantStatus(plan.ID, domain.StatusInProgress, "plan")
	if got.Rework[0].Reason != "split the migration" {
		t.Fatalf("rework %+v", got.Rework)
	}
	wantErr(t, e.svc.Approve(e.ctx, "demo", owner(), plan.ID, "plan", true, ""), ErrConflict) // plan step waits for no approval

	// Answering the approval question works like approve.
	e.mustDeliver(plan.ID, domain.Delivery{})
	e.drive(plan.ID)
	if _, err := e.svc.Answer(e.ctx, "demo", owner(), e.question(plan.ID).ID, "send back", "more detail"); err != nil {
		t.Fatal(err)
	}
	e.wantStatus(plan.ID, domain.StatusInProgress, "plan")
	e.mustDeliver(plan.ID, domain.Delivery{})
	e.drive(plan.ID)
	if _, err := e.svc.Answer(e.ctx, "demo", owner(), e.question(plan.ID).ID, "approve the plan", ""); err != nil {
		t.Fatal(err)
	}
	e.wantStatus(plan.ID, domain.StatusInProgress, "implement")

	wantErr(t, e.svc.Approve(e.ctx, "demo", owner(), "zz", "plan", true, ""), ErrInvalid)
	wantErr(t, e.svc.Approve(e.ctx, "demo", owner(), "I-404", "plan", true, ""), ErrNotFound)
	wantErr(t, e.svc.Approve(e.ctx, "demo", Actor{Kind: ActorAgent, Scopes: []string{ScopeDeliver}}, plan.ID, "plan", true, ""), ErrForbidden)
	wantErr(t, e.svc.Approve(e.ctx, "nope", owner(), plan.ID, "plan", true, ""), ErrUnknownProj)
}

func TestApproveReviewStepsAndRoles(t *testing.T) {
	e := newEnv(t)
	it := e.ready("reviewed", "Owner reviews", "x")
	e.next("implementer", it.ID)
	e.mustDeliver(it.ID, domain.Delivery{})
	if err := e.svc.Approve(e.ctx, "demo", owner(), it.ID, "delivery", true, ""); err != nil {
		t.Fatal(err)
	}
	e.wantStatus(it.ID, domain.StatusQueued, "integrate")

	so := e.ready("approved", "Analyst signs off", "x")
	e.next("implementer", so.ID)
	e.mustDeliver(so.ID, domain.Delivery{})
	wantErr(t, e.svc.Approve(e.ctx, "demo", Actor{Kind: ActorAgent, Role: "implementer", Scopes: []string{ScopeApprove}}, so.ID, "delivery", true, ""), ErrForbidden)
	a := e.nextBusy("analyst", so.ID)
	if a.Step.Name != "signoff" {
		t.Fatal(a.Step.Name)
	}
	analyst := agent("analyst", so.ID, "signoff")
	analyst.Family = "other"
	got, err := e.svc.Review(e.ctx, "demo", analyst, approveReview("ship it"))
	if err != nil || got.Step != "integrate" {
		t.Fatalf("analyst approval %+v %v", got, err)
	}
}

func TestAskOwner(t *testing.T) {
	e := newEnv(t)
	it := e.ready("simple", "x", "x")
	e.next("implementer", it.ID)
	q := domain.Question{Context: "two ways", Decision: "Which one?", Options: []domain.Option{{Label: "A", Recommended: true}, {Label: "B"}}, IfNoAnswer: "A"}
	got, err := e.svc.AskOwner(e.ctx, "demo", agent("implementer", it.ID, "implement"), q)
	if err != nil || got.ItemID != it.ID || got.AskedBy != "implementer-m1-1" {
		t.Fatalf("%+v %v", got, err)
	}
	e.wantStatus(it.ID, domain.StatusWaitingOwner, "implement")
	_, err = e.svc.AskOwner(e.ctx, "demo", agent("implementer", it.ID, "implement"), q)
	wantErr(t, err, ErrConflict)
	if _, err := e.svc.AskOwner(e.ctx, "demo", agent("analyst", it.ID, ""), q); err != nil {
		t.Fatalf("analysts may ask more: %v", err)
	}
	eventually(t, "question notification", func() bool { return strings.Contains(strings.Join(e.notes.Kinds(), ","), "question") })
	_, err = e.svc.AskOwner(e.ctx, "demo", owner(), domain.Question{Context: "x", Decision: "y"})
	wantErr(t, err, ErrInvalid)
	_, err = e.svc.AskOwner(e.ctx, "demo", owner(), domain.Question{ItemID: "bad"})
	wantErr(t, err, ErrInvalid)
	_, err = e.svc.AskOwner(e.ctx, "demo", Actor{Kind: ActorAgent, Scopes: []string{ScopeReview}}, q)
	wantErr(t, err, ErrForbidden)
	_, err = e.svc.AskOwner(e.ctx, "nope", owner(), q)
	wantErr(t, err, ErrUnknownProj)
	free, err := e.svc.AskOwner(e.ctx, "demo", owner(), q)
	if err != nil || free.ItemID != "" {
		t.Fatal(free, err)
	}
	if _, err := e.svc.Answer(e.ctx, "demo", owner(), free.ID, "B", ""); err != nil {
		t.Fatal(err)
	}
}

func TestFinishStepGuards(t *testing.T) {
	e := newEnv(t)
	it := e.ready("simple", "x", "x")
	_, _, err := e.svc.finishStep(e.ctx, e.p, stepOutcome{Item: "I-404", Step: "implement"})
	wantErr(t, err, ErrNotFound)
	_, _, err = e.svc.finishStep(e.ctx, e.p, stepOutcome{Item: it.ID, Step: "implement"})
	wantErr(t, err, ErrConflict)
	e.next("implementer", it.ID)
	_, _, err = e.svc.finishStep(e.ctx, e.p, stepOutcome{Item: it.ID, Step: "implement", Outcome: domain.OutcomeDone,
		Criteria: []domain.CriterionMark{{ID: "C7"}}})
	wantErr(t, err, ErrInvalid)
	if _, err := e.svc.CloseItem(e.ctx, "demo", owner(), it.ID, "obsolete", "", ""); err != nil {
		t.Fatal(err)
	}
	e.p.mu.Lock()
	e.p.state.Items[it.ID].Step = "implement"
	e.p.mu.Unlock()
	_, _, err = e.svc.finishStep(e.ctx, e.p, stepOutcome{Item: it.ID, Step: "implement"})
	wantErr(t, err, ErrConflict)
	if truncate("abcdef", 3) != "abc…" || tail("abcdef", 2) != "…ef" || tail("ab", 5) != "ab" {
		t.Fatal("truncate/tail")
	}
}

// An answer without a note still reaches the item, and every later step's
// prompt lists the owner's decisions: on Nobira a second agent asked the
// owner the same question again because a bare choice was never passed on.
func TestOwnerAnswerReachesTheNextAgent(t *testing.T) {
	e := newEnv(t)
	it := e.ready("simple", "Table", "docs")
	e.next("implementer", it.ID)
	e.mustDeliver(it.ID, domain.Delivery{Status: domain.DeliveryNeedsContext, Reason: "Do you approve the table in docs/mobile.md?"})
	q := e.question(it.ID)
	if _, err := e.svc.Answer(e.ctx, "demo", owner(), q.ID, "1", ""); err != nil {
		t.Fatal(err)
	}
	inbox, _ := e.svc.Inbox(e.ctx, "demo", "other", "implementer", it.ID, false)
	if len(inbox) == 0 || !strings.HasPrefix(inbox[len(inbox)-1].Text, "Owner answered: ") {
		t.Fatalf("a bare choice must reach the item: %+v", inbox)
	}
	got := e.item(it.ID)
	f, _ := e.cfg.FlowOf(got.Kind, got.Area)
	st, _ := f.Step(got.Step)
	sp, err := e.svc.BuildStepPrompt(e.ctx, e.p, got, st, "implementer", "implementer-2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sp.Prompt, "The owner already decided") || !strings.Contains(sp.Prompt, q.ID+": Do you approve the table in docs/mobile.md?") {
		t.Fatalf("the prompt of the next agent lacks the decision:\n%s", sp.Prompt)
	}
}

// The questions Barnraise asks come out in the project's language, and an
// answer in that language still does what the option says: on Nobira the
// owner, who does not read English, got "How should it continue?" with
// "Retry the step".
func TestOwnerQuestionsInTheProjectsLanguage(t *testing.T) {
	e := newEnv(t, withCfg(func(c *domain.ProjectConfig) { c.Language = "pt-BR" }))
	it := e.ready("simple", "Exportar", "export")
	e.next("implementer", it.ID)
	got := e.mustDeliver(it.ID, domain.Delivery{Status: domain.DeliveryBlocked, Reason: "sem acesso ao bucket"})
	if got.Status != domain.StatusBlocked {
		t.Fatalf("blocked %+v", got)
	}
	q := e.question(it.ID)
	if q.Decision != "Como seguir?" || q.Recommended() != "Tentar a etapa de novo" || !strings.Contains(q.Context, "parou na etapa") ||
		q.IfNoAnswer != "O item fica parado; os agentes seguem com os outros." {
		t.Fatalf("question not in Portuguese: %+v", q)
	}
	for _, o := range q.Options {
		if o.Action == "" {
			t.Fatalf("option %q has no action", o.Label)
		}
	}
	if _, err := e.svc.Answer(e.ctx, "demo", owner(), q.ID, "Tentar a etapa de novo", ""); err != nil {
		t.Fatal(err)
	}
	got = e.item(it.ID)
	if got.WaitingQuestion != "" || (got.Status != domain.StatusInProgress && got.Status != domain.StatusReady) {
		t.Fatalf("a Portuguese answer must retry the step: %+v", got)
	}
	inbox, _ := e.svc.Inbox(e.ctx, "demo", "x", "implementer", it.ID, false)
	if len(inbox) == 0 || inbox[len(inbox)-1].Text != "O dono respondeu: Tentar a etapa de novo" {
		t.Fatalf("inbox %+v", inbox)
	}

	// "Desistir do item" closes it, by its action and not by an English word.
	e.next("implementer", it.ID)
	e.mustDeliver(it.ID, domain.Delivery{Status: domain.DeliveryBlocked, Reason: "de novo"})
	q = e.question(it.ID)
	if _, err := e.svc.Answer(e.ctx, "demo", owner(), q.ID, "2", ""); err != nil {
		t.Fatal(err)
	}
	if got := e.item(it.ID); got.Status != domain.StatusClosed {
		t.Fatalf("Desistir do item must close it: %s", got.Status)
	}
}

// A step that blocks again after a retry, with nothing to wait for, is not
// retried at once against the same wall: the autopilot still decides it, on
// its own, after a pause that grows with each attempt. Nothing goes to the
// owner that only a person could do.
func TestBlockedAgainWaitsBeforeTheAutopilot(t *testing.T) {
	e := newEnv(t)
	it := e.ready("simple", "Report the review", "x")
	e.next("implementer", it.ID)
	e.mustDeliver(it.ID, domain.Delivery{Status: domain.DeliveryBlocked, Reason: "the fix for D2 is not filed yet"})
	q := e.question(it.ID)
	if !q.NotBefore.IsZero() || q.Topic != "blocked:implement" || q.Recommended() != "Retry the step" {
		t.Fatalf("first block: %+v", q)
	}
	inWork := func(id string) bool {
		ds, _ := e.svc.autopilotWork(e.ctx, e.p)
		for _, d := range ds {
			if d.thread == id {
				return true
			}
		}
		return false
	}
	if !inWork(q.ID) {
		t.Fatal("the autopilot answers a first block at once")
	}
	if _, err := e.svc.Answer(e.ctx, "demo", owner(), q.ID, "retry", ""); err != nil {
		t.Fatal(err)
	}

	e.next("implementer", it.ID)
	e.mustDeliver(it.ID, domain.Delivery{Status: domain.DeliveryBlocked, Reason: "still not filed"})
	q2 := e.question(it.ID)
	if q2.ID == q.ID || !q2.NotBefore.Equal(e.clock.Now().UTC().Add(30*time.Minute)) || !strings.Contains(q2.Context, "attempt 2") || !strings.Contains(q2.IfNoAnswer, "no session") {
		t.Fatalf("second block: %+v", q2)
	}
	if inWork(q2.ID) {
		t.Fatal("a repeated block waits before the autopilot answers it")
	}
	e.clock.Advance(31 * time.Minute)
	if !inWork(q2.ID) {
		t.Fatal("after the pause the autopilot decides it, not the owner")
	}

	if blockedBackoff(1) != 30*time.Minute || blockedBackoff(2) != 2*time.Hour || blockedBackoff(5) != 8*time.Hour {
		t.Fatal("backoff")
	}
	// Another step's block, or the same step a day later, starts over.
	if n := blockedRetries(e.p.Snapshot(), it.ID, "blocked:check", e.clock.Now()); n != 0 {
		t.Fatalf("another step: %d", n)
	}
	if n := blockedRetries(e.p.Snapshot(), it.ID, "blocked:implement", e.clock.Now().Add(25*time.Hour)); n != 0 {
		t.Fatalf("a day later: %d", n)
	}
}
