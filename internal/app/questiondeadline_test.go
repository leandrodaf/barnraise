package app

import (
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// questions.decide_after: a question nobody answered takes its recommended
// option once the time is up; before that it waits.
func TestOverdueQuestionTakesTheRecommendedOption(t *testing.T) {
	e := newEnv(t, withCfg(func(c *domain.ProjectConfig) { c.Questions.DecideAfter = 30 * time.Minute }))
	it := e.ready("feature", "Needs a call", "")
	e.next("implementer", it.ID)
	q := e.askOwner(it.ID, "Unlock the two tests?", "Unlock them", "Keep them")
	e.clock.Advance(10 * time.Minute)
	e.svc.decideOverdueQuestions(e.ctx, e.p, e.clock.Now())
	if got := e.p.Snapshot().Questions[q.ID]; got.Answer != "" {
		t.Fatalf("answered before the time: %q", got.Answer)
	}
	e.clock.Advance(25 * time.Minute)
	e.svc.decideOverdueQuestions(e.ctx, e.p, e.clock.Now())
	got := e.p.Snapshot().Questions[q.ID]
	if !strings.HasPrefix(got.Answer, "Unlock them") || got.AnsweredBy == "" {
		t.Fatalf("answer %q by %q", got.Answer, got.AnsweredBy)
	}
	// Off by default.
	e2 := newEnv(t)
	it2 := e2.ready("feature", "Needs a call", "")
	e2.next("implementer", it2.ID)
	q2 := e2.askOwner(it2.ID, "Unlock?", "Yes", "No")
	e2.clock.Advance(48 * time.Hour)
	e2.svc.decideOverdueQuestions(e2.ctx, e2.p, e2.clock.Now())
	if e2.p.Snapshot().Questions[q2.ID].Answer != "" {
		t.Fatal("without decide_after a question waits for the owner")
	}
}
