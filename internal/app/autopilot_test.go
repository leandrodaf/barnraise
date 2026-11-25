package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func suggestion(option, note string) string {
	return "My reasoning.\n" + suggestionOpen + "\noption: " + option + "\nnote: " + note + "\n" + suggestionClose
}

func autopilotEnv(t *testing.T, replies func(AdviceRequest) string) (*env, *scriptedAdvisor) {
	t.Helper()
	e := newEnv(t)
	adv := &scriptedAdvisor{replies: replies}
	pp := *e.p.Ports()
	pp.Advisor = adv
	e.p.portsV.Store(&pp)
	if err := e.svc.SetAutopilot(e.ctx, "demo", owner(), true); err != nil {
		t.Fatal(err)
	}
	return e, adv
}

func (e *env) askOwner(itemID, decision string, options ...string) *domain.Question {
	e.t.Helper()
	var opts []domain.Option
	for i, o := range options {
		opts = append(opts, domain.Option{Label: o, Recommended: i == 0})
	}
	q, err := e.svc.AskOwner(e.ctx, "demo", agent("implementer", itemID, "implement"), domain.Question{ItemID: itemID, Context: "context",
		Decision: decision, Options: opts, IfNoAnswer: "The item waits."})
	if err != nil {
		e.t.Fatal(err)
	}
	return q
}

// On autopilot the advisor answers the questions agents ask; what only the
// owner can do is left to them, said once, and not asked again.
func TestAutopilotAnswersQuestions(t *testing.T) {
	e, adv := autopilotEnv(t, func(r AdviceRequest) string {
		if strings.Contains(r.Prompt, "Which lock?") {
			return suggestion("OWNER", "Sign in to the Play Console and enroll the app.")
		}
		return suggestion("(b) rewrite them", "The criteria are newer than the tests.")
	})
	it := e.ready("simple", "Cards screen", "cards")
	q1 := e.askOwner(it.ID, "Which do you want?", "(a) keep the tests", "(b) rewrite them")
	it2 := e.ready("simple", "Play preview", "store")
	q2 := e.askOwner(it2.ID, "Which lock?", "(a) enroll now", "(b) later")

	if n := e.svc.runAutopilot(e.ctx, e.p); n != 1 {
		t.Fatalf("%d decisions applied", n)
	}
	snap := e.p.Snapshot()
	if a := snap.Questions[q1.ID].Answer; !strings.HasPrefix(a, "(b) rewrite them") || !strings.Contains(a, "autopilot") {
		t.Fatalf("answer %q", a)
	}
	conv := snap.Discussions[q1.ID]
	if last := conv[len(conv)-1]; !last.Delegated || !strings.Contains(last.Text, "(b) rewrite them") {
		t.Fatalf("the conversation does not keep the decision: %+v", last)
	}
	if !snap.Questions[q2.ID].Open() {
		t.Fatal("a decision only the owner can take was answered")
	}
	conv = snap.Discussions[q2.ID]
	if last := conv[len(conv)-1]; !last.LeftToOwner || !strings.Contains(last.Text, "Play Console") {
		t.Fatalf("left to the owner without what to do: %+v", last)
	}
	// It is not asked again while the question is the same.
	calls := len(adv.got)
	e.svc.runAutopilot(e.ctx, e.p)
	if len(adv.got) != calls {
		t.Fatalf("asked again: %d calls, then %d", calls, len(adv.got))
	}
}

// On autopilot the advisor approves a plan that waits for the owner, and
// decides the open proposals.
func TestAutopilotApprovesAndDecidesProposals(t *testing.T) {
	e, _ := autopilotEnv(t, func(r AdviceRequest) string {
		if strings.Contains(r.Prompt, "UNTRUSTED proposals") {
			start := strings.Index(r.Prompt, `"ID":"`) + 6
			id := r.Prompt[start : start+strings.Index(r.Prompt[start:], `"`)]
			return `{"verdicts":[{"id":"` + id + `","decision":"reject","reason":"not for this goal"}]}`
		}
		return suggestion("approve", "The plan covers every criterion.")
	})
	e.account("fake-1")
	e.heartbeat("m1")
	it := e.ready("planned", "Big change", "core")
	e.next("implementer", it.ID)
	e.mustDeliver(it.ID, domain.Delivery{})
	e.drive(it.ID)
	if io := e.overviewItem(it.ID); io.Awaits != "plan" {
		t.Fatalf("awaits %q", io.Awaits)
	}
	res, err := e.svc.Propose(e.ctx, "demo", owner(), prop("A faster list", "cards"))
	if err != nil {
		t.Fatal(err)
	}
	if n := e.svc.runAutopilot(e.ctx, e.p); n != 2 {
		t.Fatalf("%d decisions applied", n)
	}
	if io := e.overviewItem(it.ID); io.Awaits != "" {
		t.Fatalf("the plan still waits: %q", io.Awaits)
	}
	pr := e.p.Snapshot().Proposals[res.Proposal.ID]
	if pr.Status != domain.ProposalRejected || pr.DecidedBy != "advisor" || !strings.Contains(pr.Reason, "autopilot") {
		t.Fatalf("proposal %+v", pr)
	}
}

// The switch: the owner turns it on and off, it needs an advisor and the
// approve scope; a paused project, or a run that just started, waits.
func TestAutopilotSwitch(t *testing.T) {
	e := newEnv(t)
	wantErr(t, e.svc.SetAutopilot(e.ctx, "demo", owner(), true), ErrInvalid) // no advisor
	pp := *e.p.Ports()
	pp.Advisor = &scriptedAdvisor{replies: func(AdviceRequest) string { return "" }}
	e.p.portsV.Store(&pp)
	wantErr(t, e.svc.SetAutopilot(e.ctx, "demo", agent("implementer", "", ""), true), ErrForbidden)
	if err := e.svc.SetAutopilot(e.ctx, "demo", owner(), true); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetAutopilot(e.ctx, "demo", owner(), true); err != nil || !e.p.Snapshot().Autopilot {
		t.Fatalf("on twice: %v", err)
	}
	now := time.Now()
	if !e.svc.autopilotDue(e.p, now) {
		t.Fatal("not due when switched on")
	}
	if e.svc.autopilotDue(e.p, now.Add(time.Hour)) {
		t.Fatal("due while a run goes on")
	}
	e.svc.autopilotState("demo").running.Store(false)
	if e.svc.autopilotDue(e.p, now.Add(time.Second)) {
		t.Fatal("due again before the interval")
	}
	if err := e.svc.Pause(e.ctx, "demo", owner(), "night"); err != nil {
		t.Fatal(err)
	}
	if e.svc.autopilotDue(e.p, now.Add(time.Hour)) {
		t.Fatal("due on a paused project")
	}
	if err := e.svc.SetAutopilot(e.ctx, "demo", owner(), false); err != nil || e.p.Snapshot().Autopilot {
		t.Fatalf("off: %v", err)
	}
}

// A new question or proposal wakes the autopilot before its interval.
func TestAutopilotWakesOnNewWork(t *testing.T) {
	e, _ := autopilotEnv(t, func(AdviceRequest) string { return "" })
	now := time.Now()
	if !e.svc.autopilotDue(e.p, now) {
		t.Fatal("not due when switched on")
	}
	e.svc.autopilotState("demo").running.Store(false)
	if e.svc.autopilotDue(e.p, now.Add(time.Second)) {
		t.Fatal("due again before the interval")
	}
	if _, err := e.svc.Propose(e.ctx, "demo", owner(), prop("A faster list", "cards")); err != nil {
		t.Fatal(err)
	}
	if !e.svc.autopilotDue(e.p, now.Add(2*time.Second)) {
		t.Fatal("a new proposal did not wake the autopilot")
	}
}

type failingAdvisor struct{ calls int }

func (f *failingAdvisor) Advise(context.Context, AdviceRequest) (AdviceAnswer, error) {
	f.calls++
	return AdviceAnswer{}, errors.New("claude: exit code 1")
}

// An advisor that fails leaves the failure said once in the conversation,
// not the request again and again; the decision waits before the next try,
// and after a few failures in a row the machine gives the autopilot up.
func TestAutopilotWhenTheAdvisorFails(t *testing.T) {
	e := newEnv(t)
	adv := &failingAdvisor{}
	pp := *e.p.Ports()
	pp.Advisor = adv
	e.p.portsV.Store(&pp)
	if err := e.svc.SetAutopilot(e.ctx, "demo", owner(), true); err != nil {
		t.Fatal(err)
	}
	it := e.ready("simple", "Cards screen", "cards")
	q := e.askOwner(it.ID, "Which do you want?", "(a) keep", "(b) rewrite")
	e.svc.runAutopilot(e.ctx, e.p)
	conv := e.p.Snapshot().Discussions[q.ID]
	if len(conv) != 1 || conv[0].Role != domain.DiscussAdvisor || !strings.Contains(conv[0].Text, "exit code 1") || !strings.Contains(conv[0].Text, "m1") {
		t.Fatalf("conversation after a failure: %+v", conv)
	}
	// The next run does not ask again before the wait is over.
	calls := adv.calls
	e.svc.runAutopilot(e.ctx, e.p)
	if adv.calls != calls || len(e.p.Snapshot().Discussions[q.ID]) != 1 {
		t.Fatalf("asked again at once: %d calls, %d messages", adv.calls, len(e.p.Snapshot().Discussions[q.ID]))
	}
	// After the wait it tries again, and the failure is not said twice.
	e.clock.Advance(autopilotRetry(1) + time.Second)
	e.svc.runAutopilot(e.ctx, e.p)
	if adv.calls != calls+1 || len(e.p.Snapshot().Discussions[q.ID]) != 1 {
		t.Fatalf("after the wait: %d calls, %d messages", adv.calls, len(e.p.Snapshot().Discussions[q.ID]))
	}
	// A third failure in a row hands the autopilot to another machine.
	e.clock.Advance(autopilotRetry(2) + time.Second)
	e.svc.runAutopilot(e.ctx, e.p)
	st := e.svc.autopilotState("demo")
	if st.benched.Load() <= e.clock.Now().UnixNano() {
		t.Fatal("not handed over after three failures in a row")
	}
	if e.svc.autopilotDue(e.p, e.clock.Now().Add(time.Minute)) {
		t.Fatal("due on a machine that handed the autopilot over")
	}
	if !e.svc.autopilotDue(e.p, e.clock.Now().Add(autopilotBench+time.Minute)) {
		t.Fatal("not due again after the bench")
	}
}

func TestAutopilotRetry(t *testing.T) {
	if autopilotRetry(1) != 5*time.Minute || autopilotRetry(2) != 10*time.Minute || autopilotRetry(10) != 2*time.Hour {
		t.Fatal(autopilotRetry(1), autopilotRetry(2), autopilotRetry(10))
	}
}
