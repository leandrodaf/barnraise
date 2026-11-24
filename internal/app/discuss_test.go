package app

import (
	"context"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

type fakeAdvisor struct{ got []AdviceRequest }

func (f *fakeAdvisor) Advise(_ context.Context, r AdviceRequest) (AdviceAnswer, error) {
	f.got = append(f.got, r)
	return AdviceAnswer{Text: "Option (a) keeps the tests; I recommend it.", Model: "fable", Tokens: 42}, nil
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

	conv, err := e.svc.Discuss(e.ctx, "demo", owner(), q.ID, "o que muda para o usuário em cada opção?")
	if err != nil {
		t.Fatal(err)
	}
	if len(conv) != 2 || conv[0].Role != domain.DiscussOwner || conv[1].Role != domain.DiscussAdvisor || conv[1].Model != "fable" || conv[1].ItemID != it.ID {
		t.Fatalf("conversation %+v", conv)
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
