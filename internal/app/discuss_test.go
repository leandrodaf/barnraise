package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

type fakeAdvisor struct {
	got   []AdviceRequest
	reply string
}

func (f *fakeAdvisor) Advise(_ context.Context, r AdviceRequest) (AdviceAnswer, error) {
	f.got = append(f.got, r)
	if r.Progress != nil {
		r.Progress("read", "backend/plano.go")
	}
	text := "Option (a) keeps the tests; I recommend it."
	if f.reply != "" {
		text = f.reply
	}
	return AdviceAnswer{Text: text, Model: "fable", Tokens: 42}, nil
}

// The owner talks a question over with the advisor: both turns are kept in
// the log, the advisor reads the question, the item and the conversation, it
// answers in standby, and the owner's words reach the next agent's prompt.
func TestDiscussAQuestion(t *testing.T) {
	e := newEnv(t, withCfg(func(c *domain.ProjectConfig) { c.Language = "pt-BR" }))
	adv := &fakeAdvisor{}
	pp := *e.p.Ports()
	pp.Advisor = adv
	e.p.portsV.Store(&pp)
	it := e.ready("simple", "Cards screen", "cards")
	q, err := e.svc.AskOwner(e.ctx, "demo", agent("implementer", it.ID, "implement"), domain.Question{ItemID: it.ID, Context: "The criteria contradict the lock.",
		Decision: "Which do you want?", Options: []domain.Option{{Label: "(a) keep the tests", Recommended: true}, {Label: "(b) rewrite them"}}, IfNoAnswer: "The item waits."})
	if err != nil {
		t.Fatal(err)
	}
	e.svc.Runtime().SetStandby(true, "") // the owner's own question still gets an answer
	steps, cancel := e.svc.Hub().Subscribe("demo", []string{domain.EvDiscussionStep}, 8)
	defer cancel()
	stored := len(e.store.Events())

	conv, err := e.svc.Discuss(e.ctx, "demo", owner(), q.ID, "o que muda para o usuário em cada opção?")
	if err != nil {
		t.Fatal(err)
	}
	if len(conv) != 2 || conv[0].Role != domain.DiscussOwner || conv[1].Role != domain.DiscussAdvisor || conv[1].Model != "fable" || conv[1].ItemID != it.ID {
		t.Fatalf("conversation %+v", conv)
	}
	// The owner follows the advisor live; the steps are not kept in the log.
	select {
	case ev := <-steps:
		pl, err := ev.DecodePayload()
		if err != nil || pl.DiscussionStep == nil || pl.DiscussionStep.Target != "backend/plano.go" || pl.DiscussionStep.Thread != q.ID {
			t.Fatalf("step %+v %v", pl.DiscussionStep, err)
		}
	default:
		t.Fatal("no live step while the advisor worked")
	}
	if got := len(e.store.Events()) - stored; got != 2 {
		t.Fatalf("%d events stored, want the 2 messages and no step", got)
	}
	r := adv.got[0]
	for _, want := range []string{"Which do you want?", "(a) keep the tests", it.ID, "o que muda para o usuário"} {
		if !strings.Contains(r.Prompt, want) {
			t.Errorf("the advisor's prompt misses %q", want)
		}
	}
	if !strings.Contains(r.System, "Brazilian Portuguese") || !strings.Contains(r.System, "You only read") {
		t.Errorf("advisor system: %s", r.System)
	}
	// The second turn carries the first one.
	if _, err := e.svc.Discuss(e.ctx, "demo", owner(), q.ID, "e o custo?"); err != nil {
		t.Fatal(err)
	}
	if p := adv.got[1].Prompt; !strings.Contains(p, "I recommend it") || !strings.Contains(p, "e o custo?") {
		t.Fatalf("the second turn lost the conversation:\n%s", p)
	}
	if got, _ := e.svc.Discussion("demo", owner(), strings.ToLower(q.ID)); len(got) != 4 {
		t.Fatalf("stored %d messages, want 4", len(got))
	}
	// Only the owner talks to the advisor.
	if _, err := e.svc.Discuss(e.ctx, "demo", agent("implementer", it.ID, "implement"), q.ID, "hi"); err == nil {
		t.Fatal("an agent talked to the owner's advisor")
	}
	// The owner's words travel to the next agent.
	if notes := discussionNotes(e.p, it.ID); !strings.Contains(notes, "e o custo?") || strings.Contains(notes, "I recommend it") {
		t.Fatalf("notes for the next agent:\n%s", notes)
	}
	// Without an advisor the owner is told why.
	none := *e.p.Ports()
	none.Advisor = nil
	e.p.portsV.Store(&none)
	wantErr(t, func() error { _, err := e.svc.Discuss(e.ctx, "demo", owner(), q.ID, "x"); return err }(), ErrNoAdvisor)
}

func TestAdvisorActions(t *testing.T) {
	for _, c := range []struct{ tool, input, tool2, target string }{
		{"Read", `{"file_path":"/w/backend/a.go"}`, "read", "backend/a.go"},
		{"Grep", `{"pattern":"ContaParaNegociar"}`, "search", "ContaParaNegociar"},
		{"Glob", `{"pattern":"mobile/**/*.tsx"}`, "list", "mobile/**/*.tsx"},
		{"Bash", `{"command":"rm -rf /"}`, "", ""},
	} {
		if tool, target := advisorAction(c.tool, []byte(c.input), "/w"); tool != c.tool2 || target != c.target {
			t.Errorf("%s: %q %q", c.tool, tool, target)
		}
	}
}

func TestParseSuggestion(t *testing.T) {
	shown, opt, note := parseSuggestion("Keep the tests.\n\n<<<SUGGESTED ANSWER\noption: (a) keep the tests\nnote: Edit only the two asserted sentences.\n>>>END SUGGESTED ANSWER")
	if shown != "Keep the tests." || opt != "(a) keep the tests" || note != "Edit only the two asserted sentences." {
		t.Fatalf("%q %q %q", shown, opt, note)
	}
	if shown, opt, _ := parseSuggestion("no block"); shown != "no block" || opt != "" {
		t.Fatalf("no block: %q %q", shown, opt)
	}
}

// Decide for me: the advisor's suggested option is applied as the owner's
// answer, with its note, and the conversation says so.
func TestDelegateAnswersTheQuestion(t *testing.T) {
	e := newEnv(t)
	adv := &fakeAdvisor{reply: "Keep them.\n<<<SUGGESTED ANSWER\noption: (a) keep the tests\nnote: only the sentences change\n>>>END SUGGESTED ANSWER"}
	pp := *e.p.Ports()
	pp.Advisor = adv
	e.p.portsV.Store(&pp)
	it := e.ready("simple", "Cards screen", "cards")
	q, err := e.svc.AskOwner(e.ctx, "demo", agent("implementer", it.ID, "implement"), domain.Question{ItemID: it.ID, Context: "c", Decision: "Which?",
		Options: []domain.Option{{Label: "(a) keep the tests", Recommended: true}, {Label: "(b) rewrite them"}}, IfNoAnswer: "waits"})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := e.svc.Delegate(e.ctx, "demo", owner(), q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if last := conv[len(conv)-1]; !last.Delegated || conv[1].SuggestOption != "(a) keep the tests" || strings.Contains(conv[1].Text, "SUGGESTED") {
		t.Fatalf("conversation %+v", conv)
	}
	got := e.p.Snapshot().Questions[q.ID]
	if got.Open() || !strings.HasPrefix(got.Answer, "(a) keep the tests") || !strings.Contains(got.Answer, "only the sentences change") {
		t.Fatalf("answer %q", got.Answer)
	}
}

// The advisor triages every open proposal at once; unknown ids and words
// other than approve, reject and later are dropped; nothing is applied.
func TestTriageProposals(t *testing.T) {
	e := newEnv(t)
	adv := &fakeAdvisor{}
	pp := *e.p.Ports()
	pp.Advisor = adv
	e.p.portsV.Store(&pp)
	res, err := e.svc.Propose(e.ctx, "demo", agent("implementer", "", ""), prop("A faster list", "cards"))
	if err != nil {
		t.Fatal(err)
	}
	pr := res.Proposal
	adv.reply = `{"verdicts":[{"id":"` + pr.ID + `","decision":"Approve","reason":"it serves the goal"},{"id":"P-999","decision":"approve","reason":"x"}]}`
	v, err := e.svc.TriageProposals(e.ctx, "demo", owner())
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 1 || v[0].ID != pr.ID || v[0].Decision != "approve" {
		t.Fatalf("verdicts %+v", v)
	}
	if e.p.Snapshot().Proposals[pr.ID].Status != domain.ProposalOpen {
		t.Fatal("triage applied a decision")
	}
}

// scriptedAdvisor answers each call with the next reply, and records the calls.
type scriptedAdvisor struct {
	mu      sync.Mutex
	got     []AdviceRequest
	replies func(r AdviceRequest) string
}

func (f *scriptedAdvisor) Advise(_ context.Context, r AdviceRequest) (AdviceAnswer, error) {
	f.mu.Lock()
	f.got = append(f.got, r)
	f.mu.Unlock()
	return AdviceAnswer{Text: f.replies(r), Model: "fable"}, nil
}

// A long list is weighed a few proposals at a time; an answer in prose is
// put in the shape once more; an advisor that never gives verdicts says why.
func TestTriageProposalsInBatches(t *testing.T) {
	e := newEnv(t)
	adv := &scriptedAdvisor{}
	pp := *e.p.Ports()
	pp.Advisor = adv
	e.p.portsV.Store(&pp)
	var ids []string
	for i := range triageBatch + 3 {
		res, err := e.svc.Propose(e.ctx, "demo", owner(), prop(fmt.Sprintf("Idea number %d about lists", i), fmt.Sprintf("area%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, res.Proposal.ID)
	}
	verdictsOf := func(prompt string) string {
		var vs []string
		for _, id := range ids {
			if strings.Contains(prompt, `"`+id+`"`) || strings.Contains(prompt, id+",") || strings.HasSuffix(strings.SplitN(prompt, "\n", 2)[0], id+".") {
				vs = append(vs, `{"id":"`+id+`","decision":"later","reason":"not now"}`)
			}
		}
		return `{"verdicts":[` + strings.Join(vs, ",") + `]}`
	}
	// The first answer of each batch is prose; the rewrite is the JSON.
	adv.replies = func(r AdviceRequest) string {
		if strings.HasPrefix(r.System, "You turn a text into JSON") {
			return verdictsOf(r.Prompt)
		}
		return "You have many good ideas here; I would wait on most of them."
	}
	v, err := e.svc.TriageProposals(e.ctx, "demo", owner())
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != len(ids) {
		t.Fatalf("%d verdicts for %d proposals: %+v", len(v), len(ids), v)
	}
	if n := len(adv.got); n != 4 {
		t.Errorf("%d calls: two batches, each asked twice", n)
	}
	adv.got = nil
	adv.replies = func(AdviceRequest) string { return "You've hit your limit · resets 7pm" }
	if _, err := e.svc.TriageProposals(e.ctx, "demo", owner()); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "hit your limit") {
		t.Fatalf("no verdicts: %v", err)
	}
}
