package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

// atCheck brings a simple item to its check step.
func (e *env) atCheck(title string) *domain.Item {
	e.t.Helper()
	it := e.ready("simple", title, "x")
	e.start(it.ID)
	e.mustDeliver(it.ID, domain.Delivery{})
	return e.wantStatus(it.ID, domain.StatusInProgress, "check")
}

func TestCheckStepOutcomes(t *testing.T) {
	e := newEnv(t)
	pass := e.atCheck("pass")
	e.drive(pass.ID)
	e.wantStatus(pass.ID, domain.StatusQueued, "integrate")
	if ty := e.types(pass.ID); !strings.Contains(ty, domain.EvCheckRequested+" "+domain.EvCheckCompleted) {
		t.Fatalf("events %s", ty)
	}

	e.checks.Script("main", testkit.CheckOutcome{Passed: false, Summary: "2 tests failed"})
	fail := e.atCheck("fail")
	e.drive(fail.ID)
	got := e.wantStatus(fail.ID, domain.StatusInProgress, "implement")
	if got.Rework[0].Reason != "check main failed: 2 tests failed" || got.Rework[0].From != "check" {
		t.Fatalf("rework %+v", got.Rework)
	}

	e.checks.Script("main", testkit.CheckOutcome{Err: errors.New("runner down")})
	broken := e.atCheck("broken")
	e.drive(broken.ID)
	got = e.wantStatus(broken.ID, domain.StatusInProgress, "implement")
	if !strings.Contains(got.Rework[0].Reason, "check could not run: runner down") {
		t.Fatal(got.Rework[0].Reason)
	}
}

func TestCheckPendingThenReport(t *testing.T) {
	e := newEnv(t, withCfg(func(c *domain.ProjectConfig) {
		c.Checks["lint"] = domain.CheckDef{Name: "lint", Run: "golangci-lint run", RunsOn: "local"}
		c.Flows["allchecks"] = &domain.Flow{Name: "allchecks", Limit: domain.DefaultReworkLimit, Steps: []domain.StepDef{
			{Name: "implement", Type: domain.StepAgent, Role: "implementer"}, {Name: "check", Type: domain.StepCheck, Run: "all"}, {Name: "integrate", Type: domain.StepMergeQueue}}}
	}))
	e.checks.Script("main", testkit.Pending)
	it := e.atCheck("pending")
	e.drive(it.ID)
	e.wantStatus(it.ID, domain.StatusInProgress, "check")
	if _, err := e.svc.ReportCheck(e.ctx, "demo", owner(), domain.CheckResult{ItemID: it.ID, Check: "other", Passed: true}); err != nil {
		t.Fatal(err)
	}
	e.wantStatus(it.ID, domain.StatusInProgress, "check")
	ci := Actor{Kind: ActorToken, Name: "ci", Scopes: []string{ScopeChecksReport}}
	got, err := e.svc.ReportCheck(e.ctx, "demo", ci, domain.CheckResult{ItemID: strings.TrimPrefix(it.ID, "I-"), Check: "main", Passed: true, Summary: "green in CI"})
	if err != nil || got.Status != domain.StatusQueued {
		t.Fatalf("reported %+v %v", got, err)
	}
	// A late result is recorded and moves nothing.
	got, err = e.svc.ReportCheck(e.ctx, "demo", ci, domain.CheckResult{ItemID: it.ID, Check: "main", Passed: false})
	if err != nil || got.Status != domain.StatusQueued {
		t.Fatalf("late %+v %v", got, err)
	}
	_, err = e.svc.ReportCheck(e.ctx, "demo", ci, domain.CheckResult{ItemID: "x"})
	wantErr(t, err, ErrInvalid)
	_, err = e.svc.ReportCheck(e.ctx, "demo", ci, domain.CheckResult{ItemID: "I-404", Check: "main"})
	wantErr(t, err, ErrNotFound)
	_, err = e.svc.ReportCheck(e.ctx, "demo", Actor{Kind: ActorToken, Scopes: []string{ScopeItemsRead}}, domain.CheckResult{ItemID: it.ID})
	wantErr(t, err, ErrForbidden)
	_, err = e.svc.ReportCheck(e.ctx, "nope", ci, domain.CheckResult{ItemID: it.ID})
	wantErr(t, err, ErrUnknownProj)

	// "checks" runs every check: the step waits for all of them.
	e.checks.Script("main", testkit.Pass)
	e.checks.Script("lint", testkit.Pending)
	all := e.ready("allchecks", "all", "x")
	e.start(all.ID)
	e.mustDeliver(all.ID, domain.Delivery{})
	e.drive(all.ID)
	e.wantStatus(all.ID, domain.StatusInProgress, "check")
	got, err = e.svc.ReportCheck(e.ctx, "demo", ci, domain.CheckResult{ItemID: all.ID, Check: "lint", Passed: false, Summary: "unused import"})
	if err != nil || got.Step != "implement" || !strings.Contains(got.Rework[0].Reason, "check lint failed: unused import") {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestCheckStepGuards(t *testing.T) {
	e := newEnv(t)
	it := e.atCheck("no check for a criterion")
	crit := []domain.Criterion{{ID: "C1", Text: ears}}
	if _, err := e.svc.UpdateItem(e.ctx, "demo", owner(), it.ID, ItemPatch{Criteria: &crit}); err != nil {
		t.Fatal(err)
	}
	e.drive(it.ID)
	got := e.wantStatus(it.ID, domain.StatusInProgress, "implement")
	if got.Rework[0].Reason != "criteria without a check: C1" {
		t.Fatal(got.Rework[0].Reason)
	}
	other := e.atCheck("no runner")
	e.svc.d.Checks = nil
	if err := e.svc.runDriverStep(e.ctx, e.p, other, domain.StepDef{Name: "check", Type: domain.StepCheck, Run: "checks.main"}); err == nil {
		t.Fatal("a check step without a runner must fail")
	}
	if ok, _ := e.svc.reproductionHolds(e.ctx, e.p, other, []string{"main"}); !ok {
		t.Fatal("without git or runner the reproduction cannot be checked; it holds")
	}
}

func TestScriptStep(t *testing.T) {
	e := newEnv(t, withExec())
	ok := e.ready("scripted", "ok", "x")
	e.next("implementer", ok.ID)
	e.mustDeliver(ok.ID, domain.Delivery{})
	e.drive(ok.ID)
	e.wantStatus(ok.ID, domain.StatusQueued, "integrate")
	if c := e.exec.Cmds()[0]; c.Name != "sh" || c.Args[1] != "make lint" {
		t.Fatalf("command %+v", c)
	}
	e.exec.Results = map[string]ports.ExecResult{"sh -c make lint": {ExitCode: 2, Stderr: []byte("lint: 3 problems")}}
	bad := e.ready("scripted", "bad", "x")
	e.next("implementer", bad.ID)
	e.mustDeliver(bad.ID, domain.Delivery{})
	e.drive(bad.ID)
	got := e.wantStatus(bad.ID, domain.StatusInProgress, "implement")
	if got.Rework[0].Reason != "script failed: lint: 3 problems" {
		t.Fatal(got.Rework[0].Reason)
	}
	e.exec.Err = errors.New("no shell")
	e.exec.Results = nil
	e.mustDeliver(bad.ID, domain.Delivery{})
	e.drive(bad.ID)
	if got := e.item(bad.ID); !strings.Contains(got.Rework[1].Reason, "no shell") {
		t.Fatal(got.Rework[1].Reason)
	}
	noExec := newEnv(t)
	it := noExec.ready("scripted", "x", "x")
	if err := noExec.svc.runScriptStep(noExec.ctx, noExec.p, it, domain.StepDef{Name: "lint", Run: "x"}); err == nil {
		t.Fatal("a script step without exec must fail")
	}
}

func TestCheckReportsToTheCodeHost(t *testing.T) {
	g := &testkit.FakeGit{Main: "m1", HeadSHA: "h1", Changed: map[string][]string{"*": {"app/a.go", "plan.md"}}}
	e := newEnv(t, withFakeGit(g), withHost(), withCfg(func(c *domain.ProjectConfig) {
		c.Areas = map[string]domain.AreaDef{"app": {Name: "app", Paths: []string{"app/**"}}}
		c.EnvFiles = []string{".env"}
	}))
	testkit.WriteFile(t, e.root, ".env", "KEY=1\n")
	e.checks.Func = func(spec ports.CheckSpec) (domain.CheckResult, bool, error) {
		if spec.Env["PORT"] == "" || len(spec.Areas) != 1 || spec.Areas[0] != "app" || spec.Commit != "h1" {
			t.Errorf("spec %+v", spec)
		}
		return domain.CheckResult{Passed: true, Summary: "ok", Artifacts: []string{"https://preview.test/1"}}, false, nil
	}
	it := e.ready("simple", "preview", "x")
	e.start(it.ID)
	e.workspace(it.ID, nil)
	e.mustDeliver(it.ID, domain.Delivery{})
	e.drive(it.ID)
	dir := e.svc.WorktreeDir("demo", it.ID)
	if data, err := os.ReadFile(filepath.Join(dir, ".env")); err != nil || string(data) != "KEY=1\n" {
		t.Fatalf("env files are copied into the worktree: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "criteria.json")); err != nil {
		t.Fatal("criteria.json is written")
	}
	if e.item(it.ID).PR == "" || e.host.Count("OpenDraftPR") != 1 {
		t.Fatalf("draft PR: %v", e.host.Calls())
	}
	e.wantStatus(it.ID, domain.StatusQueued, "integrate")
	eventually(t, "check reported on the PR", func() bool {
		return e.host.Count("ReportCheck h1 main true") == 1 && e.host.Count("Comment 1 **Preview from check main**") == 1
	})
	eventually(t, "the delivery is published", func() bool { return e.host.Count("MarkReady 1") == 1 && e.host.Count("UpdatePR 1") >= 1 })
}

func TestLockedExistingTests(t *testing.T) {
	e := newEnv(t, withGit(), withFiles(map[string]string{"README.md": "# demo\n", "tests/a_test.sh": "exit 0\n"}))
	it := e.ready("simple", "touch a test", "x")
	e.next("implementer", it.ID)
	e.workspace(it.ID, map[string]string{"tests/a_test.sh": "exit 0 # weakened\n", "tests/new_test.sh": "exit 0\n", "app.go": "package app\n"})
	e.mustDeliver(it.ID, domain.Delivery{})
	e.drive(it.ID)
	got := e.wantStatus(it.ID, domain.StatusInProgress, "implement")
	if r := got.Rework[0].Reason; !strings.Contains(r, "tests/a_test.sh") || strings.Contains(r, "new_test") {
		t.Fatalf("reason %q", r)
	}
	spec := e.checks.Specs()
	if len(spec) != 0 {
		t.Fatalf("no check runs when a locked file changed: %+v", spec)
	}
}

// toBugCheck brings a bug through reproduce and implement.
func (e *env) toBugCheck(title string, repro bool) *domain.Item {
	e.t.Helper()
	bug := e.create(NewItem{Title: title, Kind: "bug", Priority: "P1", Area: "login", Criteria: crit(ears)})
	e.start(bug.ID)
	name := "tests/repro_" + strings.ToLower(bug.ID) + ".sh"
	if repro {
		e.workspace(bug.ID, map[string]string{name: "exit 1\n"})
	}
	e.mustDeliver(bug.ID, domain.Delivery{Files: []string{name}})
	e.workspace(bug.ID, map[string]string{"login.go": "package login // fixed\n"})
	e.mustDeliver(bug.ID, domain.Delivery{})
	return e.wantStatus(bug.ID, domain.StatusInProgress, "check")
}

func TestReproductionMustFailOnMain(t *testing.T) {
	e := newEnv(t, withGit())
	e.checks.ScriptMain("main", testkit.Fail)
	good := e.toBugCheck("crash", true)
	e.drive(good.ID)
	e.wantStatus(good.ID, domain.StatusInProgress, "review")
	var mainRuns []ports.CheckSpec
	for _, s := range e.checks.Specs() {
		if s.Main {
			mainRuns = append(mainRuns, s)
		}
	}
	if len(mainRuns) != 1 || mainRuns[0].Def.Scope != "all" || mainRuns[0].Def.FlakyRetries != 1 {
		t.Fatalf("main runs %+v", mainRuns)
	}
	if _, err := os.Stat(mainRuns[0].Dir); !os.IsNotExist(err) {
		t.Fatal("the reproduction worktree is removed")
	}

	e.checks.ScriptMain("main", testkit.Pass)
	weak := e.toBugCheck("weak", true)
	e.drive(weak.ID)
	got := e.wantStatus(weak.ID, domain.StatusInProgress, "implement")
	if !strings.Contains(got.Rework[0].Reason, "passes on main, so it does not reproduce the bug") {
		t.Fatal(got.Rework[0].Reason)
	}

	none := e.create(NewItem{Title: "no repro", Kind: "bug", Priority: "P1", Criteria: crit(ears)})
	e.start(none.ID)
	if _, err := e.svc.Redo(e.ctx, "demo", owner(), none.ID, "check", "skip ahead"); err != nil {
		t.Fatal(err)
	}
	e.drive(none.ID)
	got = e.wantStatus(none.ID, domain.StatusInProgress, "implement")
	if got.Rework[0].Reason != "no reproduction test was delivered in the reproduce step" {
		t.Fatal(got.Rework[0].Reason)
	}
}
