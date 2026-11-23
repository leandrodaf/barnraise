package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

// risky creates a risky item, claims it and prepares both worktrees.
func (e *env) risky(title string, filesA, filesB map[string]string) (*domain.Item, string) {
	e.t.Helper()
	it := e.create(NewItem{Title: title, Kind: "simple", Area: "x", Criteria: crit(ears), Priority: "P1", Risky: true})
	a := e.start(it.ID)
	if !dualEligible(e.item(it.ID), a.Step) {
		e.t.Fatal("a risky agent step runs as two attempts")
	}
	e.workspace(it.ID, filesA)
	dirB, err := e.svc.prepareAlt(e.ctx, e.p, it.ID, a.Step)
	if err != nil {
		e.t.Fatalf("prepare the second attempt: %v", err)
	}
	for name, content := range filesB {
		testkit.WriteFile(e.t, dirB, name, content)
	}
	return it, dirB
}

func attemptActor(id, attempt string) Actor {
	a := agent("implementer", id, "implement")
	if attempt == "b" {
		a.Name += "-b"
		a.Attempt = "b"
	}
	return a
}

func (e *env) deliverAttempt(id, attempt string, d domain.Delivery) (*domain.Item, error) {
	d.ItemID = id
	if d.Summary == "" {
		d.Summary = "attempt " + attempt
	}
	if d.Status == "" {
		d.Status = domain.DeliveryDone
	}
	return e.svc.Deliver(e.ctx, "demo", attemptActor(id, attempt), d)
}

func TestDualNotForOrdinaryItems(t *testing.T) {
	impl := domain.StepDef{Name: "implement", Type: domain.StepAgent}
	for name, tc := range map[string]struct {
		it   domain.Item
		st   domain.StepDef
		want bool
	}{
		"risky implement": {domain.Item{Risky: true}, impl, true},
		"not risky":       {domain.Item{}, impl, false},
		"review":          {domain.Item{Risky: true}, domain.StepDef{Name: "review", Type: domain.StepAgent, Review: true}, false},
		"plan":            {domain.Item{Risky: true}, domain.StepDef{Name: "plan", Type: domain.StepAgent, Output: "plan.md"}, false},
		"refine":          {domain.Item{Risky: true}, domain.StepDef{Name: RefineStep, Type: domain.StepAgent}, false},
		"check":           {domain.Item{Risky: true}, domain.StepDef{Name: "check", Type: domain.StepCheck}, false},
	} {
		if got := dualEligible(&tc.it, tc.st); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

func TestDualSmallerChangeWinsWhenChecksTie(t *testing.T) {
	e := newEnv(t, withGit())
	it, dirB := e.risky("Risky change", map[string]string{"a1.txt": "a\n", "a2.txt": "a\n"}, map[string]string{"b.txt": "b\n"})
	if got := e.item(it.ID); got.Alt == nil || got.Alt.Step != "implement" || got.Alt.Branch != got.Branch+"-b" {
		t.Fatalf("second attempt not recorded: %+v", got.Alt)
	}
	// Preparing again is a no-op that returns the same worktree.
	again, err := e.svc.prepareAlt(e.ctx, e.p, it.ID, domain.StepDef{Name: "implement", Type: domain.StepAgent})
	if err != nil || again != dirB {
		t.Fatalf("prepare twice: %s %v", again, err)
	}

	held, err := e.deliverAttempt(it.ID, "a", domain.Delivery{})
	if err != nil {
		t.Fatal(err)
	}
	if held.Step != "implement" || held.Alt == nil || held.Alt.A == nil || held.Alt.BEnded {
		t.Fatalf("the first delivery waits for the second attempt: %+v", held.Alt)
	}
	out, err := e.deliverAttempt(it.ID, "b", domain.Delivery{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Alt != nil || out.Step != "check" {
		t.Fatalf("after both attempts the item moves on: step %s alt %+v", out.Step, out.Alt)
	}
	// Attempt b (one file) was adopted on the item's branch.
	dirA := e.svc.WorktreeDir("demo", it.ID)
	if _, err := os.Stat(filepath.Join(dirA, "b.txt")); err != nil {
		t.Fatalf("the item's worktree must hold attempt b: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirA, "a1.txt")); !os.IsNotExist(err) {
		t.Fatal("attempt a's files must be gone")
	}
	if _, err := os.Stat(dirB); !os.IsNotExist(err) {
		t.Fatal("the second worktree is removed")
	}
	tl, _ := e.svc.Timeline(e.ctx, "demo", owner(), it.ID)
	var why string
	for _, te := range tl {
		if strings.Contains(te.Summary, "attempt b chosen") {
			why = te.Summary
		}
	}
	if !strings.Contains(why, "1 files changed") || !strings.Contains(why, "attempt a:") {
		t.Fatalf("the timeline must say why b won: %q", why)
	}
	// A late delivery of the losing attempt is refused.
	if _, err := e.deliverAttempt(it.ID, "a", domain.Delivery{}); err == nil {
		t.Fatal("the step is over: a late delivery must fail")
	}
}

func TestDualChecksDecideBeforeSize(t *testing.T) {
	e := newEnv(t, withGit())
	it, dirB := e.risky("Risky checks", map[string]string{"a.txt": "a\n"}, map[string]string{"b1.txt": "b\n", "b2.txt": "b\n"})
	e.checks.Func = func(spec ports.CheckSpec) (domain.CheckResult, bool, error) {
		return domain.CheckResult{Check: spec.Def.Name, ItemID: spec.ItemID, Passed: spec.Dir != dirB, Summary: "scripted"}, false, nil
	}
	// B first, then A: the order of the deliveries does not matter.
	held, err := e.deliverAttempt(it.ID, "b", domain.Delivery{})
	if err != nil || held.Alt == nil || !held.Alt.BEnded || held.Alt.B == nil {
		t.Fatalf("b held: %+v %v", held, err)
	}
	out, err := e.deliverAttempt(it.ID, "a", domain.Delivery{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Alt != nil || out.Step != "check" {
		t.Fatalf("step %s alt %+v", out.Step, out.Alt)
	}
	dirA := e.svc.WorktreeDir("demo", it.ID)
	if _, err := os.Stat(filepath.Join(dirA, "a.txt")); err != nil {
		t.Fatal("attempt a passed the checks and stays, even being the same size or larger")
	}
	if _, err := os.Stat(filepath.Join(dirA, "b1.txt")); !os.IsNotExist(err) {
		t.Fatal("attempt b failed the checks and must not be adopted")
	}
}

func TestDualSecondAttemptEndsWithoutDelivery(t *testing.T) {
	e := newEnv(t, withGit())
	it, _ := e.risky("Risky silent b", map[string]string{"a.txt": "a\n"}, nil)
	// B ends first with nothing: the item keeps waiting for A.
	e.svc.altEnded(e.ctx, e.p, it.ID)
	if got := e.item(it.ID); got.Alt == nil || !got.Alt.BEnded || got.Step != "implement" {
		t.Fatalf("waiting for a: %+v", got.Alt)
	}
	out, err := e.deliverAttempt(it.ID, "a", domain.Delivery{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Alt != nil || out.Step != "check" {
		t.Fatalf("a alone wins: step %s alt %+v", out.Step, out.Alt)
	}
	// Nothing to end any more: no panic, no change.
	e.svc.altEnded(e.ctx, e.p, it.ID)
	if _, err := e.svc.resolveDual(e.ctx, e.p, it.ID); err == nil {
		t.Fatal("no attempts left to choose")
	}
}

func TestDualFirstDeliversThenSecondEnds(t *testing.T) {
	e := newEnv(t, withGit())
	it, _ := e.risky("Risky a then end", map[string]string{"a.txt": "a\n"}, nil)
	if _, err := e.deliverAttempt(it.ID, "a", domain.Delivery{}); err != nil {
		t.Fatal(err)
	}
	e.svc.altEnded(e.ctx, e.p, it.ID)
	if got := e.item(it.ID); got.Alt != nil || got.Step != "check" {
		t.Fatalf("the end of b resolves the step: step %s alt %+v", got.Step, got.Alt)
	}
}

func TestDualNeitherUsable(t *testing.T) {
	e := newEnv(t, withGit())
	it, _ := e.risky("Risky blocked", map[string]string{"a.txt": "a\n"}, map[string]string{"b.txt": "b\n"})
	if _, err := e.deliverAttempt(it.ID, "a", domain.Delivery{Status: domain.DeliveryBlocked, Reason: "no access to the API"}); err != nil {
		t.Fatal(err)
	}
	out, err := e.deliverAttempt(it.ID, "b", domain.Delivery{Status: domain.DeliveryBlocked, Reason: "same here"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Alt != nil || out.Status == domain.StatusInProgress && out.Step != "implement" {
		t.Fatalf("blocked attempts must not advance the item: %s at %s", out.Status, out.Step)
	}
	if out.Step != "implement" {
		t.Fatalf("step %s", out.Step)
	}
}

func TestDualNeedsARepository(t *testing.T) {
	e := newEnv(t)
	it := e.create(NewItem{Title: "Risky without git", Kind: "simple", Area: "x", Criteria: crit(ears), Priority: "P1", Risky: true})
	a := e.start(it.ID)
	if _, err := e.svc.prepareAlt(e.ctx, e.p, it.ID, a.Step); err == nil || !strings.Contains(err.Error(), "repository") {
		t.Fatalf("err %v", err)
	}
	if e.svc.hasAlt(e.p, it.ID, "implement") {
		t.Fatal("no second attempt without a repository")
	}
}

func TestRuntimeStartsBothAttemptsOfARiskyItem(t *testing.T) {
	e := newEnv(t, withGit())
	e.account("fake-1")
	it := e.create(NewItem{Title: "Risky via runtime", Kind: "simple", Area: "x", Criteria: crit(ears), Priority: "P1", Risky: true})
	release := make(chan struct{})
	e.prov.Handle(func(ctx context.Context, _ ports.SessionSpec, _ func(ports.ProviderEvent)) (ports.SessionResult, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return ports.SessionResult{}, nil
	})
	e.fill()
	specs := e.prov.Specs()
	if len(specs) != 2 {
		t.Fatalf("two sessions for a risky item, got %d", len(specs))
	}
	a, b := e.actorOf(specs[0]), e.actorOf(specs[1])
	if a.Attempt != "" || b.Attempt != "b" || a.ItemID != it.ID || b.ItemID != it.ID || b.Name != a.Name+"-b" {
		t.Fatalf("actors %+v / %+v", a, b)
	}
	if specs[0].Workdir == specs[1].Workdir {
		t.Fatal("each attempt has its own worktree")
	}
	if got := e.item(it.ID); got.Alt == nil || got.Alt.Step != "implement" {
		t.Fatalf("alt %+v", got.Alt)
	}
	e.svc.Runtime().MaxSessions = -1 // nothing new starts while the two end
	close(release)
	e.idle()
}
