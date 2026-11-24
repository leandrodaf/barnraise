package app

import (
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

func (e *env) lessons() []*domain.Lesson { return e.p.Snapshot().LessonList() }

func (e *env) waitLessons(n int) []*domain.Lesson {
	e.t.Helper()
	eventually(e.t, "lessons", func() bool { return len(e.lessons()) == n })
	return e.lessons()
}

func (e *env) promptOf(id string) string {
	e.t.Helper()
	it := e.item(id)
	f, _ := e.cfg.FlowFor(it.Kind)
	st, _ := f.Step(it.Step)
	pr, err := e.svc.BuildStepPrompt(e.ctx, e.p, it, st, "implementer", "implementer-m1-1")
	if err != nil {
		e.t.Fatal(err)
	}
	return pr.Prompt
}

// reviewRound sends the item back from review, fixes files, and passes review.
func (e *env) reviewRound(id, finding string, before, fix map[string]string) {
	e.t.Helper()
	e.workspace(id, before)
	e.mustDeliver(id, domain.Delivery{})
	if _, err := e.review(id, reworkReview(finding)); err != nil {
		e.t.Fatal(err)
	}
	e.clock.Advance(time.Minute)
	e.workspace(id, fix)
	e.mustDeliver(id, domain.Delivery{})
	if _, err := e.review(id, approveReview("good now")); err != nil {
		e.t.Fatal(err)
	}
}

func TestLessonsFromReviewRework(t *testing.T) {
	e := newEnv(t, withGit(), withFiles(map[string]string{"wallet/balance.go": "package wallet\n", "cards/list.go": "package cards\n"}))
	a := e.ready("reviewed", "Balance", "wallet")
	e.next("implementer", a.ID)
	e.reviewRound(a.ID, "the balance is rounded with floats instead of integer cents",
		map[string]string{"wallet/balance.go": "package wallet\n\nvar B = 1.5\n"},
		map[string]string{"wallet/balance.go": "package wallet\n\nvar B = 150\n", "wallet/cents.go": "package wallet\n"})
	ls := e.waitLessons(1)
	l := ls[0]
	if l.Step != "review" || l.Count != 1 || !domain.Contains(l.Areas, "wallet") || len(l.Sources) != 1 ||
		!domain.Contains(l.Files, "wallet/balance.go") || !domain.Contains(l.Files, "wallet/cents.go") || domain.Contains(l.Files, "plan.md") ||
		!strings.Contains(l.Text, "integer cents") || !strings.HasPrefix(l.ID, "L-") {
		t.Fatalf("lesson %+v", l)
	}
	got := e.item(a.ID)
	if len(got.Rework) != 1 || !got.Rework[0].Resolved || got.Rework[0].Commit == "" {
		t.Fatalf("round %+v", got.Rework)
	}
	if !strings.Contains(e.types(l.ID), domain.EvLessonLearned) {
		t.Fatalf("events %s", e.types(l.ID))
	}

	// A later item of the same area hears it; one of another area does not.
	b := e.ready("reviewed", "Transfers", "wallet")
	c := e.ready("reviewed", "Cards list", "cards")
	if p := e.promptOf(b.ID); !strings.Contains(p, "Lessons from earlier rework") || !strings.Contains(p, l.ID) {
		t.Fatalf("prompt of %s:\n%s", b.ID, p)
	}
	if p := e.promptOf(c.ID); strings.Contains(p, l.ID) {
		t.Fatalf("prompt of %s has the lesson:\n%s", c.ID, p)
	}

	// The same mistake again merges into the lesson.
	e.next("implementer", b.ID)
	e.reviewRound(b.ID, "balance rounded with floats instead of integer cents again",
		map[string]string{"wallet/transfer.go": "package wallet\n\nvar T = 2.5\n"},
		map[string]string{"wallet/transfer.go": "package wallet\n\nvar T = 250\n"})
	eventually(t, "merged lesson", func() bool { ls := e.lessons(); return len(ls) == 1 && ls[0].Count == 2 })
	m := e.lessons()[0]
	if m.ID != l.ID || len(m.Sources) != 2 || !domain.Contains(m.Files, "wallet/transfer.go") || !strings.Contains(m.Text, "seen 2 times") {
		t.Fatalf("merged %+v", m)
	}
	if !strings.Contains(e.types(l.ID), domain.EvLessonMerged) {
		t.Fatalf("events %s", e.types(l.ID))
	}

	// The owner lists and deletes it.
	list, err := e.svc.ListLessons("demo", owner(), LessonFilter{File: "wallet/balance.go"})
	if err != nil || len(list) != 1 {
		t.Fatalf("list %v %v", list, err)
	}
	if list, _ := e.svc.ListLessons("demo", owner(), LessonFilter{Area: "cards"}); len(list) != 0 {
		t.Fatalf("cards %v", list)
	}
	if _, err := e.svc.ListLessons("demo", Actor{}, LessonFilter{}); err == nil {
		t.Fatal("list without scope")
	}
	if _, err := e.svc.DeleteLesson(e.ctx, "demo", agent("implementer", b.ID, "implement"), l.ID); err == nil {
		t.Fatal("an agent deletes lessons")
	}
	_, err = e.svc.DeleteLesson(e.ctx, "demo", owner(), "I-1")
	wantErr(t, err, ErrInvalid)
	_, err = e.svc.DeleteLesson(e.ctx, "demo", owner(), "L-00000000")
	wantErr(t, err, ErrNotFound)
	if _, err := e.svc.DeleteLesson(e.ctx, "demo", owner(), strings.ToLower(l.ID)); err != nil {
		t.Fatal(err)
	}
	if len(e.lessons()) != 0 || strings.Contains(e.promptOf(c.ID), "Lessons from") {
		t.Fatal("deleted lesson still given")
	}
	if _, err := e.svc.ListLessons("nope", owner(), LessonFilter{}); err == nil {
		t.Fatal("unknown project")
	}
	if _, err := e.svc.DeleteLesson(e.ctx, "nope", owner(), l.ID); err == nil {
		t.Fatal("unknown project")
	}
}

func TestLessonsFromChecksWithoutGit(t *testing.T) {
	e := newEnv(t)
	e.checks.Script("main", testkit.CheckOutcome{Passed: false, Summary: "TestCards: nil map"}, testkit.Pass)
	it := e.ready("simple", "Cards", "cards")
	e.start(it.ID)
	e.mustDeliver(it.ID, domain.Delivery{Files: []string{"cards/map.go"}})
	e.drive(it.ID)
	e.wantStatus(it.ID, domain.StatusInProgress, "implement")
	if len(e.lessons()) != 0 {
		t.Fatal("learned before the fix")
	}
	e.mustDeliver(it.ID, domain.Delivery{Files: []string{"cards/map.go"}})
	e.drive(it.ID)
	ls := e.waitLessons(1)
	if l := ls[0]; !domain.Contains(l.Checks, "main") || !strings.Contains(l.Text, "check main") || !domain.Contains(l.Files, "cards/map.go") {
		t.Fatalf("lesson %+v", l)
	}
	// Learned once: later passes of the step do not learn it again.
	got := e.item(it.ID)
	if r := resolveRounds(got, "check", true); len(r) != 0 {
		t.Fatalf("resolved again %+v", r)
	}
	all, _ := e.svc.ListLessons("demo", owner(), LessonFilter{})
	if len(all) != 1 {
		t.Fatalf("all %v", all)
	}
}

func TestLessonsOwnerCorrectionAndOff(t *testing.T) {
	e := newEnv(t)
	it := e.ready("reviewed", "Profile", "profile")
	e.next("implementer", it.ID)
	e.mustDeliver(it.ID, domain.Delivery{Files: []string{"profile/view.go"}})
	if _, err := e.svc.Redo(e.ctx, "demo", owner(), it.ID, "implement", "the avatar must come from the CDN, not the API"); err != nil {
		t.Fatal(err)
	}
	got := e.item(it.ID)
	if len(got.PastRework) != 1 || got.PastRework[0].From != "review" || got.PastRework[0].Outcome != "redo" || len(got.Rework) != 0 {
		t.Fatalf("correction %+v / %+v", got.PastRework, got.Rework)
	}
	e.mustDeliver(it.ID, domain.Delivery{Files: []string{"profile/avatar.go"}})
	if _, err := e.review(it.ID, approveReview("ok")); err != nil {
		t.Fatal(err)
	}
	ls := e.waitLessons(1)
	if !strings.Contains(ls[0].Text, "CDN") || !domain.Contains(ls[0].Files, "profile/avatar.go") || ls[0].Outcome != "redo" {
		t.Fatalf("lesson %+v", ls[0])
	}

	// Rounds of a budget reset for a stronger model keep their lesson.
	x := &domain.Item{Rework: []domain.ReworkRecord{{From: "review", Resolved: true}, {From: "review", Reason: "r"}}}
	pastRework(x)
	if len(x.PastRework) != 1 || x.PastRework[0].Reason != "r" {
		t.Fatalf("past %+v", x.PastRework)
	}

	off := newEnv(t, withCfg(func(c *domain.ProjectConfig) { c.Lessons.Enabled = false }))
	o := off.ready("reviewed", "Off", "profile")
	off.next("implementer", o.ID)
	off.mustDeliver(o.ID, domain.Delivery{Files: []string{"profile/a.go"}})
	if _, err := off.review(o.ID, reworkReview("wrong")); err != nil {
		t.Fatal(err)
	}
	off.mustDeliver(o.ID, domain.Delivery{})
	if _, err := off.review(o.ID, approveReview("ok")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if len(off.lessons()) != 0 || off.svc.lessonsPrompt(off.ctx, off.p, o) != "" {
		t.Fatal("lessons while off")
	}
}
