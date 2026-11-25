package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

// rootProblems is the root part of the trust status on this machine.
func rootProblems(t *testing.T, e *env) []TrustProblem {
	t.Helper()
	st, err := e.svc.TrustStatus(e.ctx, "demo", owner(), "")
	if err != nil {
		t.Fatal(err)
	}
	var out []TrustProblem
	for _, pr := range st.Problems {
		if pr.Scope == "root" {
			out = append(out, pr)
		}
	}
	return out
}

// The root's barnraise.yaml follows main: a machine that has not pulled the
// owner's new version yet, and one that has, both run; a hand edit is still
// refused.
func TestRootFilesOnMainAreTrusted(t *testing.T) {
	e := newEnv(t, withGit(), withFiles(map[string]string{"barnraise.yaml": "project: demo\n"}))
	if err := e.svc.TrustOnLoad(e.ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	// The owner pushes a new barnraise.yaml from another machine.
	e.repo.PushToMain(t, "turn the tracker repair on", map[string]string{"barnraise.yaml": "project: demo\ntracker:\n  reconcile: true\n"})
	if err := e.repo.Git.Fetch(e.ctx); err != nil {
		t.Fatal(err)
	}
	if pr := rootProblems(t, e); len(pr) != 0 {
		t.Fatalf("a root behind main was refused: %+v", pr)
	}
	// This machine pulls it: the file is main's now, not a change by hand.
	testkit.RunGit(t, e.root, "merge", "-q", "--ff-only", "origin/main")
	if pr := rootProblems(t, e); len(pr) != 0 {
		t.Fatalf("a root that pulled main was refused: %+v", pr)
	}
	// A hand edit is not main's.
	_ = os.WriteFile(filepath.Join(e.root, "barnraise.yaml"), []byte("project: demo\nroles: {}\n"), 0o644)
	pr := rootProblems(t, e)
	if len(pr) != 1 || len(pr[0].Changes) != 1 || pr[0].Changes[0].Path != "barnraise.yaml" {
		t.Fatalf("a hand edit was trusted: %+v", pr)
	}
	// So is a commit main does not hold.
	testkit.RunGit(t, e.root, "commit", "-q", "-am", "local only")
	if pr := rootProblems(t, e); len(pr) != 1 {
		t.Fatalf("a commit off main was trusted: %+v", pr)
	}
}

// Each machine compares its root with its own record: another machine
// trusting different files (its own checkout) does not refuse this one.
func TestAnotherMachinesTrustRecordIsNotThisOnes(t *testing.T) {
	e := newEnv(t)
	_ = os.WriteFile(filepath.Join(e.root, "barnraise.yaml"), []byte("project: demo\n"), 0o644)
	if err := e.svc.TrustOnLoad(e.ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	other := domain.NewEvent(e.ids.NewID(), domain.EvTrustRecorded, "demo", "", "m9", "owner", e.clock.Now(),
		domain.Payload{Note: "trusted by the owner", Extra: map[string]any{"files": map[string]string{"barnraise.yaml": strings.Repeat("ab", 32)}}})
	if err := e.svc.Ingest(e.ctx, other); err != nil {
		t.Fatal(err)
	}
	if pr := rootProblems(t, e); len(pr) != 0 {
		t.Fatalf("another machine's record refused this one: %+v", pr)
	}
	// A change here is still this machine's to trust.
	_ = os.WriteFile(filepath.Join(e.root, "barnraise.yaml"), []byte("project: demo\nroles: {}\n"), 0o644)
	if pr := rootProblems(t, e); len(pr) != 1 {
		t.Fatalf("a change here was trusted: %+v", pr)
	}
}

// A machine whose sessions are all refused claims nothing and gives back
// what it holds, so a machine that can run the items takes them.
func TestMachineThatCannotStartGivesItsItemsBack(t *testing.T) {
	e := newEnv(t)
	e.account("fake-1")
	cfgFile := filepath.Join(e.root, "barnraise.yaml")
	_ = os.WriteFile(cfgFile, []byte("project: demo\n"), 0o644)
	if err := e.svc.TrustOnLoad(e.ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	held := e.ready("simple", "held here", "x")
	e.mutate(held.ID, func(it *domain.Item) {
		it.Status, it.Step = domain.StatusInProgress, "implement"
		it.Claim = &domain.Claim{Machine: e.svc.d.Machine, Agent: "implementer-m1-1", Since: e.clock.Now()}
	})
	running := e.ready("simple", "its session runs", "y")
	e.mutate(running.ID, func(it *domain.Item) {
		it.Status, it.Step = domain.StatusInProgress, "implement"
		it.Claim = &domain.Claim{Machine: e.svc.d.Machine, Agent: "implementer-m1-2", Since: e.clock.Now()}
	})
	other := e.ready("simple", "held elsewhere", "z")
	e.mutate(other.ID, func(it *domain.Item) {
		it.Status, it.Step = domain.StatusInProgress, "implement"
		it.Claim = &domain.Claim{Machine: "m9", Since: e.clock.Now()}
	})
	free := e.ready("simple", "nobody holds it", "w")

	_ = os.WriteFile(cfgFile, []byte("project: demo\nroles: {}\n"), 0o644)
	if len(e.svc.machineDistrust(e.ctx, e.p)) == 0 {
		t.Fatal("the changed root was not seen")
	}
	e.fill()
	if len(e.prov.Specs()) != 0 || e.item(free.ID).Claim != nil {
		t.Fatalf("a machine that cannot start claimed %s", free.ID)
	}
	got := e.svc.standDown(e.ctx, e.p, map[string]bool{running.ID: true}, "root changed")
	if len(got) != 1 || got[0] != held.ID {
		t.Fatalf("given back %v, want [%s]", got, held.ID)
	}
	if it := e.item(held.ID); it.Claim != nil || it.Status != domain.StatusReady || it.Step != "implement" {
		t.Fatalf("held item after stand down: %s claim %+v step %s", it.Status, it.Claim, it.Step)
	}
	if it := e.item(running.ID); it.Claim == nil {
		t.Fatal("the item whose session runs here was given back")
	}
	if it := e.item(other.ID); it.Claim == nil || it.Claim.Machine != "m9" {
		t.Fatal("another machine's item was touched")
	}
	if lastNote(t, e, domain.EvTrustMismatch) == "" {
		t.Fatal("the refusal was not told")
	}
	// Trusted again, it takes work as before.
	if _, err := e.svc.Trust(e.ctx, "demo", owner(), ""); err != nil {
		t.Fatal(err)
	}
	if len(e.svc.machineDistrust(e.ctx, e.p)) != 0 {
		t.Fatal("still refused after the owner re-trusted")
	}
}

// Clearing the clarifications answers the markers in the text too: the
// readiness check reads the body, and the item stayed a draft.
func TestClearingClarificationsMakesTheItemReady(t *testing.T) {
	e := newEnv(t)
	it := e.create(NewItem{Title: "needs criteria", Kind: "simple", Area: "x", Priority: "P1",
		Body: "Problem: the lodge.\n\n[NEEDS CLARIFICATION: analyst writes EARS criteria]"})
	if it.Status != domain.StatusDraft || len(it.Clarifications) != 1 {
		t.Fatalf("created %s with %v", it.Status, it.Clarifications)
	}
	none := []string{}
	cs := crit(ears)
	got, err := e.svc.UpdateItem(e.ctx, "demo", owner(), it.ID, ItemPatch{Criteria: &cs, Clarifications: &none})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusReady {
		t.Fatalf("after clearing: %s (%v)", got.Status, got.Readiness(true))
	}
	if strings.Contains(got.Body, domain.ClarificationMarker) || !strings.Contains(got.Body, "(clarified: analyst writes EARS criteria)") {
		t.Fatalf("body %q", got.Body)
	}
}

func TestResolveClarifications(t *testing.T) {
	cases := map[string]string{
		"no marker":                                             "no marker",
		"a [NEEDS CLARIFICATION: why?] b":                       "a (clarified: why?) b",
		"[NEEDS CLARIFICATION]":                                 "(clarified)",
		"x [NEEDS CLARIFICATION: open to the end":               "x (clarified: open to the end)",
		"[NEEDS CLARIFICATION: 1] and [NEEDS CLARIFICATION: 2]": "(clarified: 1) and (clarified: 2)",
	}
	for in, want := range cases {
		if got := domain.ResolveClarifications(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
		if len(domain.FindClarifications(domain.ResolveClarifications(in))) != 0 {
			t.Errorf("%q still has a marker", in)
		}
	}
}

// An item whose agent cannot start reads as stalled, with the reason, and
// sits with the blocked items: not idle, "takes it at the next pass".
func TestItemWhoseAgentCannotStartIsStalled(t *testing.T) {
	e := newEnv(t)
	draft := e.create(NewItem{Title: "a draft", Kind: "simple", Area: "x", Priority: "P1", Body: "[NEEDS CLARIFICATION: criteria]"})
	held := e.ready("simple", "held", "y")
	e.mutate(held.ID, func(it *domain.Item) {
		it.Status, it.Step = domain.StatusInProgress, "implement"
		it.Claim = &domain.Claim{Machine: "m2", Since: e.clock.Now()}
	})
	e.clock.Advance(1)
	e.heartbeat("m2")
	why := "untrusted: the project's barnraise.yaml changed outside Barnraise"
	e.live(domain.EvAgentSession, "m2", domain.Payload{Session: &domain.SessionInfo{Agent: "analyst-m2-1", Role: "analyst", ItemID: draft.ID, Step: RefineStep, State: "failed", Detail: why, Machine: "m2", At: e.clock.Now()}})
	e.live(domain.EvAgentSession, "m2", domain.Payload{Session: &domain.SessionInfo{Agent: "implementer-m2-1", Role: "implementer", ItemID: held.ID, Step: "implement", State: "failed", Detail: why, Machine: "m2", At: e.clock.Now()}})
	o, err := e.svc.Overview(e.ctx, owner(), OverviewQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{draft.ID, held.ID} {
		io := o.item(t, id)
		if io.Activity != ActStalled || io.Column != ColBlocked || io.Signal.Kind != "cannot_start" || io.Signal.Arg != "m2" || !strings.Contains(io.Detail, "cannot start: "+why) {
			t.Errorf("%s: %s in %s, signal %+v, detail %q", id, io.Activity, io.Column, io.Signal, io.Detail)
		}
	}
	// A change after the failure (an edit, a new claim) starts over.
	e.clock.Advance(1)
	e.mutate(held.ID, func(*domain.Item) {})
	o, _ = e.svc.Overview(e.ctx, owner(), OverviewQuery{})
	if io := o.item(t, held.ID); io.Activity == ActStalled {
		t.Fatalf("an old failure still stalls %s: %s", held.ID, io.Detail)
	}
}

// A start failure that repeats is told to the cluster again now and then:
// the agent's failed state is live only, and a machine that restarted would
// otherwise show the item idle for as long as the failure lasts.
func TestRepeatingStartFailureIsToldAgain(t *testing.T) {
	r := &Runtime{}
	if !r.saidStartErr("demo/a/I-1", true) {
		t.Fatal("first telling")
	}
	if r.saidStartErr("demo/a/I-1", false) {
		t.Fatal("told again at once")
	}
	r.startErrSaid["demo/a/I-1"] = time.Now().Add(-restateStartErr - time.Second)
	if !r.saidStartErr("demo/a/I-1", false) {
		t.Fatal("not told again after the interval")
	}
	if r.saidStartErr("demo/a/I-1", false) {
		t.Fatal("the new telling was not recorded")
	}
}
