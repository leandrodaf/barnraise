package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
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

// scriptedSession is a provider session that answers with a fixed result.
type limitProvider struct {
	answers map[string]ports.SessionResult // model -> result
	asked   []string
}

func (l *limitProvider) Name() string   { return "claude" }
func (l *limitProvider) Family() string { return "anthropic" }
func (l *limitProvider) Detect(context.Context) (ports.Capabilities, error) {
	return ports.Capabilities{}, nil
}
func (l *limitProvider) Open(_ context.Context, spec ports.SessionSpec) (ports.Session, error) {
	l.asked = append(l.asked, spec.Account.ID+"/"+spec.Model)
	r := l.answers[spec.Account.ID+"/"+spec.Model]
	if _, ok := l.answers[spec.Account.ID+"/"+spec.Model]; !ok {
		r = l.answers[spec.Model]
	}
	ch := make(chan ports.ProviderEvent)
	close(ch)
	return &fixedSession{ev: ch, res: r}, nil
}

type fixedSession struct {
	ev  chan ports.ProviderEvent
	res ports.SessionResult
}

func (f *fixedSession) Events() <-chan ports.ProviderEvent { return f.ev }
func (f *fixedSession) Wait() (ports.SessionResult, error) { return f.res, nil }
func (f *fixedSession) Stop() error                        { return nil }

// The advisor goes down its ladder: a model at its own limit gives the
// answer to the next model; an account at its window's limit to the next
// account. A limit message is never taken for an answer.
func TestAdvisorFallsBackOnLimits(t *testing.T) {
	prov := &limitProvider{answers: map[string]ports.SessionResult{
		"a1/fable": {Text: "You've hit your Fable limit · resets Mon 7pm", Limited: true},
		"a2/fable": {Text: "You've hit your 5-hour limit · resets 1am", Limited: true},
		"opus":     {Text: "Use option (a)."},
	}}
	var heard []string
	adv := &ProviderAdvisor{Provider: prov, Models: []string{"fable", "opus"}, TempDir: t.TempDir(),
		Accounts: func(string) []domain.Account { return []domain.Account{{ID: "a1"}, {ID: "a2"}} },
		OnLimit: func(acc, model string, _ time.Time, modelOnly bool) {
			heard = append(heard, fmt.Sprintf("%s/%s/%v", acc, model, modelOnly))
		}}
	ans, err := adv.Advise(context.Background(), AdviceRequest{Workdir: t.TempDir()})
	if err != nil || ans.Model != "opus" || ans.Text != "Use option (a)." {
		t.Fatalf("%+v %v", ans, err)
	}
	if strings.Join(heard, ",") != "a1/fable/true,a2/fable/false" {
		t.Fatalf("limits heard %v", heard)
	}
	// Nothing left: the advisor says why, and no limit text is an answer.
	prov.answers["opus"] = ports.SessionResult{Text: "You've hit your weekly limit"}
	if _, err := adv.Advise(context.Background(), AdviceRequest{Workdir: t.TempDir()}); !errors.Is(err, ErrNoAdvisor) || !strings.Contains(err.Error(), "weekly limit") {
		t.Fatalf("every option at its limit: %v", err)
	}
	if ok, _ := (&ProviderAdvisor{Models: []string{"fable"}, Accounts: func(string) []domain.Account { return nil }}).Ready(); ok {
		t.Fatal("ready without an account")
	}
}

// A model at its own limit leaves the account to the next model of the class.
func TestPickChoiceSkipsAModelAtItsLimit(t *testing.T) {
	e := newEnv(t)
	accs := []*domain.Account{{ID: "c1", Provider: "claude", Status: domain.AccountOK, CeilingPct: 100}}
	choices := []domain.ModelChoice{{Provider: "claude", Model: "fable"}, {Provider: "claude", Model: "opus"}}
	if c, a := e.svc.pickChoice(choices, accs, "demo", e.clock.Now()); c.Model != "fable" || a == nil {
		t.Fatalf("%+v %v", c, a)
	}
	e.svc.markModelLimited("c1", "fable", e.clock.Now().Add(time.Hour))
	if c, a := e.svc.pickChoice(choices, accs, "demo", e.clock.Now()); c.Model != "opus" || a == nil {
		t.Fatalf("after fable's limit: %+v %v", c, a)
	}
	if c, _ := e.svc.pickChoice(choices, accs, "demo", e.clock.Now().Add(2*time.Hour)); c.Model != "fable" {
		t.Fatalf("after the reset: %+v", c)
	}
	if status := e.svc.AdvisorStatus("demo"); status == "" {
		t.Fatal("no advisor in this env, and no reason given")
	}
}

// "Decide for me" when only the owner can decide: the conversation says
// what to do, and no error comes back.
func TestDelegateLeavesToOwner(t *testing.T) {
	e, _ := autopilotEnv(t, func(AdviceRequest) string { return suggestion("OWNER", "Sign in to the store first.") })
	it := e.ready("simple", "Store", "store")
	q := e.askOwner(it.ID, "Enroll now?", "(a) yes", "(b) later")
	conv, err := e.svc.Delegate(e.ctx, "demo", owner(), q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if last := conv[len(conv)-1]; !last.LeftToOwner || !strings.Contains(last.Text, "Sign in to the store") {
		t.Fatalf("%+v", last)
	}
	if !e.p.Snapshot().Questions[q.ID].Open() {
		t.Fatal("answered a decision only the owner can take")
	}
}

// When a step waits only because a locked test must change, the advisor
// unlocks it for that item and the step goes on: nobody edits it by hand.
func TestAdviceUnlocksALockedTest(t *testing.T) {
	e, _ := autopilotEnv(t, func(AdviceRequest) string {
		return suggestion("UNLOCK tests/quarry_test.go", "The owner decided the radius; the locked test must follow.")
	})
	it := e.ready("simple", "Radius", "field")
	q := e.askOwner(it.ID, "How to go on?", "Try the step again", "Give the item up")
	if n := e.svc.runAutopilot(e.ctx, e.p); n != 1 {
		t.Fatalf("%d decisions", n)
	}
	snap := e.p.Snapshot()
	if got := snap.Items[it.ID].Unlocked; len(got) != 1 || got[0] != "tests/quarry_test.go" {
		t.Fatalf("unlocked %v", got)
	}
	if a := snap.Questions[q.ID].Answer; !strings.HasPrefix(a, "Try the step again") {
		t.Fatalf("answer %q", a)
	}
	// Only locked files, inside the repository, and only by who approves.
	wantErr(t, func() error {
		_, err := e.svc.UnlockFiles(e.ctx, "demo", owner(), it.ID, []string{"app/main.go"}, "")
		return err
	}(), ErrInvalid)
	wantErr(t, func() error {
		_, err := e.svc.UnlockFiles(e.ctx, "demo", owner(), it.ID, []string{"../tests/x_test.go"}, "")
		return err
	}(), ErrInvalid)
	wantErr(t, func() error {
		_, err := e.svc.UnlockFiles(e.ctx, "demo", agent("implementer", it.ID, "implement"), it.ID, []string{"tests/a_test.go"}, "")
		return err
	}(), ErrForbidden)
	if files, ok := unlockOption("UNLOCK: a_test.go, tests/b.test.ts"); !ok || len(files) != 2 {
		t.Fatalf("%v %v", files, ok)
	}
	if _, ok := unlockOption("UNLOCKED"); ok {
		t.Fatal("UNLOCKED read as an unlock")
	}
}

// The owner talks a request over in a chat, then the advisor writes the
// item from it, with criteria tied to checks.
func TestChatToItem(t *testing.T) {
	e, adv := autopilotEnv(t, func(r AdviceRequest) string {
		if strings.Contains(r.Prompt, "Answer ONLY with JSON") {
			return `{"title":"Sound the bell early","kind":"feature","area":"nope","priority":"P1","size":"3h","body":"A button starts the next wave.",` +
				`"criteria":[{"text":"WHEN the keeper presses Sound the bell, THE SYSTEM SHALL start the next wave","check":"main"}],"files":["bastion.gd"]}`
		}
		return "Today the wave starts after 10 s; a button could start it early."
	})
	if !IsChat("C-AB12CD") || IsChat("Q-1") || IsChat("C-x") {
		t.Fatal("chat ids")
	}
	conv, err := e.svc.Discuss(e.ctx, "demo", owner(), "c-ab12cd", "Quero um botão para chamar a onda antes.")
	if err != nil || len(conv) != 2 || conv[0].ItemID != "" {
		t.Fatalf("%+v %v", conv, err)
	}
	if !strings.Contains(adv.got[0].System, "talks it over with you before it becomes an item") {
		t.Fatal("the chat runs with the decision's instructions")
	}
	res, err := e.svc.CreateItemFromChat(e.ctx, "demo", owner(), "C-AB12CD")
	if err != nil || res.Item == nil {
		t.Fatalf("%+v %v", res, err)
	}
	it := res.Item
	if it.Title != "Sound the bell early" || len(it.Criteria) != 1 || it.Area != "" || !strings.Contains(it.Body, "C-AB12CD") {
		t.Fatalf("item %+v", it)
	}
	conv = e.p.Snapshot().Discussions["C-AB12CD"]
	if last := conv[len(conv)-1]; !strings.Contains(last.Text, it.ID) {
		t.Fatalf("the chat does not say the item: %+v", last)
	}
	wantErr(t, func() error { _, err := e.svc.CreateItemFromChat(e.ctx, "demo", owner(), "C-EMPTY1"); return err }(), ErrInvalid)
	wantErr(t, func() error { _, err := e.svc.CreateItemFromChat(e.ctx, "demo", owner(), "Q-1"); return err }(), ErrInvalid)
}
