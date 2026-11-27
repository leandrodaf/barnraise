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

// A check that is red on main too says nothing about the item. Instead of
// sending it back to an agent to "fix" code that is not wrong, or asking
// the owner, Barnraise opens one item that repairs the check on main, and
// every item held by that check waits for it, with nobody asked.
func TestCheckRedOnMainWaitsForOneRepair(t *testing.T) {
	e := newEnv(t)
	e.checks.Script("main", testkit.CheckOutcome{Passed: false, MainRed: true, Summary: "red on main too: branch 0/1 passed, main 0/1 passed"})
	it := e.atCheck("lint broken on main")
	e.drive(it.ID)
	got := e.item(it.ID)
	if len(got.Rework) != 0 || got.WaitingQuestion != "" || got.Status == domain.StatusWaitingOwner {
		t.Fatalf("not waiting quietly: %+v", got)
	}
	if len(got.Deps) != 1 {
		t.Fatalf("deps %v, want the repair item", got.Deps)
	}
	repair := e.item(got.Deps[0])
	if repair.Title != mainRepairTitle("main") || repair.Priority.String() != "P0" || !strings.Contains(repair.Body, "red on main too") {
		t.Fatalf("repair item %+v", repair)
	}
	if k := strings.Join(e.notes.Kinds(), ","); strings.Contains(k, "check_red_on_main") {
		t.Fatalf("the owner was told: %s", k)
	}

	// A second item held by the same check waits for the same repair.
	other := e.atCheck("another one")
	e.drive(other.ID)
	if deps := e.item(other.ID).Deps; len(deps) != 1 || deps[0] != repair.ID {
		t.Fatalf("second item deps %v, want %s", deps, repair.ID)
	}
	n := 0
	p, _ := e.svc.Project("demo")
	for _, x := range p.Snapshot().Items {
		if x.Title == mainRepairTitle("main") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d repair items", n)
	}
}

// A check that cannot run at all here (its program is missing, the network
// is down) is no item's to repair: the item is held and the owner told.
func TestCheckThatCannotRunIsHeldForTheOwner(t *testing.T) {
	e := newEnv(t)
	e.checks.Script("main", testkit.CheckOutcome{Passed: false, MainRed: true, CannotRun: true, Summary: "check main needs ast-grep, which is not installed"})
	it := e.atCheck("no ast-grep here")
	e.drive(it.ID)
	got := e.wantStatus(it.ID, domain.StatusBlocked, "check")
	if len(got.Deps) != 0 || len(got.Rework) != 0 {
		t.Fatalf("deps %v rework %v", got.Deps, got.Rework)
	}
	if k := strings.Join(e.notes.Kinds(), ","); !strings.Contains(k, "check_red_on_main") {
		t.Fatalf("notifications %s", k)
	}
}

// The repair item's own check red on main is its work not done yet: it goes
// back to its agent like any failure, never waiting for itself.
func TestMainRepairItemDoesNotWaitForItself(t *testing.T) {
	e := newEnv(t)
	e.checks.Script("main", testkit.CheckOutcome{Passed: false, MainRed: true, Summary: "still red"})
	it := e.atCheck(mainRepairTitle("main"))
	e.drive(it.ID)
	got := e.item(it.ID)
	if len(got.Deps) != 0 || len(got.Rework) != 1 {
		t.Fatalf("repair item deps %v rework %d", got.Deps, len(got.Rework))
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

func partsCfg(c *domain.ProjectConfig) {
	c.Areas = map[string]domain.AreaDef{"backend": {Name: "backend", Paths: []string{"backend/**"}, Dir: "backend", Stack: "go", About: "REST API"},
		"web": {Name: "web", Paths: []string{"web/**"}, Dir: "web", Stack: "react"}}
	c.Checks = map[string]domain.CheckDef{
		"lint":         {Name: "lint", Run: "make lint", RunsOn: "local", Scope: "touched", Order: -1},
		"backend-test": {Name: "backend-test", Run: "go test ./...", RunsOn: "local", Scope: "touched", Areas: []string{"backend"}, Dir: "backend"},
		"web-test":     {Name: "web-test", Run: "npm test", RunsOn: "local", Scope: "touched", Areas: []string{"web"}, Dir: "web"},
	}
	c.Selection = domain.SelectionDef{NoneWhen: []string{"docs/**"}}
	for _, f := range c.Flows {
		for i := range f.Steps {
			if f.Steps[i].Type == domain.StepCheck {
				f.Steps[i].Run = "checks"
			}
		}
	}
}

func ranChecks(e *env) string {
	var out []string
	for _, s := range e.checks.Specs() {
		out = append(out, s.Def.Name)
	}
	return strings.Join(out, " ")
}

// In a repository with several parts, the check step runs only what the
// item touched, cheapest first, and records why the rest was skipped.
func TestCheckStepRunsOnlyWhatTheItemTouched(t *testing.T) {
	e := newEnv(t, withGit(), withCfg(partsCfg))
	it := e.ready("simple", "Backend endpoint", "backend")
	e.start(it.ID)
	e.workspace(it.ID, map[string]string{"backend/handler.go": "package backend\n", "docs/api.md": "# api\n"})
	e.mustDeliver(it.ID, domain.Delivery{})
	e.drive(it.ID)
	if got := ranChecks(e); got != "lint backend-test" {
		t.Fatalf("ran %q, want lint then backend-test", got)
	}
	e.wantStatus(it.ID, domain.StatusQueued, "integrate")
	tl, _ := e.svc.Timeline(e.ctx, "demo", owner(), it.ID)
	var skipped string
	for _, te := range tl {
		if te.Type == domain.EvCheckCompleted && strings.Contains(te.Summary, "web-test") {
			skipped = te.Summary
		}
	}
	if !strings.Contains(skipped, "skipped: the change touches nothing in web") {
		t.Fatalf("the timeline must say why web-test did not run: %q", skipped)
	}
	// The agent was told which part it works in.
	text := e.svc.itemText(e.p, e.item(it.ID), false)
	for _, want := range []string{"Part of the repository: backend, in `backend/` (go): REST API", "backend-test (`go test ./...`)", "Other parts", "web in `web/`"} {
		if !strings.Contains(text, want) {
			t.Fatalf("item text lacks %q:\n%s", want, text)
		}
	}
}

func TestCheckStepFailsFastAndRunsBothPartsWhenBothAreTouched(t *testing.T) {
	e := newEnv(t, withGit(), withCfg(partsCfg))
	it := e.ready("simple", "Shared type", "web")
	e.start(it.ID)
	e.workspace(it.ID, map[string]string{"backend/types.go": "package backend\n", "web/types.ts": "export {}\n"})
	e.mustDeliver(it.ID, domain.Delivery{})
	e.checks.Script("backend-test", testkit.Fail)
	e.drive(it.ID)
	if got := ranChecks(e); got != "lint backend-test" {
		t.Fatalf("after backend-test fails nothing else runs: %q", got)
	}
	got := e.wantStatus(it.ID, domain.StatusInProgress, "implement")
	if len(got.Rework) != 1 || !strings.Contains(got.Rework[0].Reason, "check backend-test failed") || strings.Contains(got.Rework[0].Reason, "web-test") {
		t.Fatalf("rework %+v", got.Rework)
	}
	// Fixed: the next round runs both parts.
	e.checks.Script("backend-test", testkit.Pass)
	e.mustDeliver(it.ID, domain.Delivery{})
	e.drive(it.ID)
	if got := ranChecks(e); got != "lint backend-test lint backend-test web-test" {
		t.Fatalf("second round: %q", got)
	}
	e.wantStatus(it.ID, domain.StatusQueued, "integrate")
}

// The merge queue checks the combined tree only for what the batch touches.
func TestMergeQueueChecksOnlyWhatTheBatchTouches(t *testing.T) {
	e := newEnv(t, withGit(), withCfg(partsCfg))
	it := e.ready("simple", "Web page", "web")
	e.start(it.ID)
	e.workspace(it.ID, map[string]string{"web/page.tsx": "export {}\n"})
	e.mustDeliver(it.ID, domain.Delivery{})
	e.drive(it.ID)
	// Main moved in between (a backend change by another machine), so the
	// combined tree is new; still, only the checks of what this batch touches run.
	e.repo.PushToMain(t, "backend change elsewhere", map[string]string{"backend/other.go": "package backend\n"})
	before := len(e.checks.Specs())
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); err != nil || n != 1 {
		t.Fatalf("queue %d %v", n, err)
	}
	var queue []string
	for _, s := range e.checks.Specs()[before:] {
		queue = append(queue, s.Def.Name)
	}
	if got := strings.Join(queue, " "); got != "lint web-test" {
		t.Fatalf("the queue ran %q; the backend was not touched", got)
	}
	e.wantStatus(it.ID, domain.StatusIntegrated, "")
}

// When main did not move since the item's checks, the tree that lands is the
// one already checked: the queue does not run the same checks again.
func TestMergeQueueDoesNotRepeatChecksOnTheSameTree(t *testing.T) {
	e := newEnv(t, withGit(), withCfg(partsCfg))
	it := e.ready("simple", "Web page", "web")
	e.start(it.ID)
	e.workspace(it.ID, map[string]string{"web/page.tsx": "export {}\n", "plan.md": "# plan\n"})
	e.mustDeliver(it.ID, domain.Delivery{})
	e.drive(it.ID)
	before := len(e.checks.Specs())
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); err != nil || n != 1 {
		t.Fatalf("queue %d %v", n, err)
	}
	if extra := len(e.checks.Specs()) - before; extra != 0 {
		t.Fatalf("%d checks ran again on a tree that was already checked", extra)
	}
	e.wantStatus(it.ID, domain.StatusIntegrated, "")
	if files := e.repo.MainFiles(t); !contains(files, "web/page.tsx") || contains(files, "plan.md") {
		t.Fatalf("main %v", files)
	}
}

// Each part of the repository may send its items through its own flow.
func TestAreaChoosesTheFlowOfItsItems(t *testing.T) {
	e := newEnv(t, withCfg(func(c *domain.ProjectConfig) {
		partsCfg(c)
		web := c.Areas["web"]
		web.KindFlows = map[string]string{"simple": "reviewed"}
		c.Areas["web"] = web
	}))
	back := e.ready("simple", "Backend endpoint", "backend")
	web := e.ready("simple", "Web page", "web")
	e.start(back.ID)
	e.mustDeliver(back.ID, domain.Delivery{})
	e.wantStatus(back.ID, domain.StatusInProgress, "check") // flow simple: implement, check, integrate
	e.start(web.ID)
	e.mustDeliver(web.ID, domain.Delivery{})
	e.wantStatus(web.ID, domain.StatusInProgress, "review") // flow reviewed: implement, review, integrate
	// An item already moving cannot hop to an area with another flow.
	area := "backend"
	if _, err := e.svc.UpdateItem(e.ctx, "demo", owner(), web.ID, ItemPatch{Area: &area}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("moving an item in progress to another flow: %v", err)
	}
	// A flow that does not exist is reported by the configuration.
	bad := *e.cfg
	bad.Areas = map[string]domain.AreaDef{"web": {Name: "web", KindFlows: map[string]string{"simple": "ghost"}}}
	bad.Checks = map[string]domain.CheckDef{}
	found := false
	for _, err := range bad.Validate() {
		if strings.Contains(err.Error(), `flow "ghost"`) {
			found = true
		}
	}
	if !found {
		t.Fatal("an unknown flow must be reported")
	}
}

// A code host refuses a pull request without a difference from main, so the
// draft opens with the first commit of the item, once, and is closed (with
// its branch removed) when the squash lands on main.
func TestPullRequestOpensWithTheFirstCommitAndClosesOnIntegration(t *testing.T) {
	e := newEnv(t, withGit(), withHost())
	it := e.ready("simple", "Cards", "x")
	e.start(it.ID)
	e.workspace(it.ID, nil)
	if n := e.host.Count("OpenDraftPR"); n != 0 {
		t.Fatalf("no pull request before the branch has a commit of its own: %d", n)
	}
	e.workspace(it.ID, map[string]string{"app/cards.txt": "cards\n"})
	e.mustDeliver(it.ID, domain.Delivery{})
	if n := e.host.Count("OpenDraftPR"); n != 1 || e.item(it.ID).PR == "" {
		t.Fatalf("the first step's commit opens the draft: %d opens, pr %q", n, e.item(it.ID).PR)
	}
	branch := e.item(it.ID).Branch
	e.drive(it.ID)
	if n := e.host.Count("OpenDraftPR"); n != 1 {
		t.Fatalf("opened once: %d", n)
	}
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); err != nil || n != 1 {
		t.Fatalf("queue %d %v", n, err)
	}
	eventually(t, "the pull request to be closed", func() bool { return e.host.Count("Close 1") == 1 })
	if e.host.Count("Comment 1 Integrated into main") != 1 {
		t.Fatalf("the pull request says where the change went: %v", e.host.Calls())
	}
	if out, _ := testkit.GitOutput(e.repo.Root, "--git-dir", e.repo.Remote, "branch", "--list", branch); strings.TrimSpace(out) != "" {
		t.Fatalf("the item's branch is removed from the remote after integration: %q", out)
	}
}

// A delivery that changes nothing must not land on main as an empty commit.
func TestCheckStepRefusesADeliveryThatChangesNothing(t *testing.T) {
	e := newEnv(t, withGit())
	it := e.ready("simple", "Already there", "x")
	e.start(it.ID)
	e.workspace(it.ID, map[string]string{"README.md": "# demo\n"}) // identical to main
	e.mustDeliver(it.ID, domain.Delivery{})
	e.drive(it.ID)
	got := e.wantStatus(it.ID, domain.StatusInProgress, "implement")
	if len(got.Rework) != 1 || !strings.Contains(got.Rework[0].Reason, "changes nothing on top of main") {
		t.Fatalf("rework %+v", got.Rework)
	}
	if len(e.checks.Specs()) != 0 {
		t.Fatal("nothing to verify: no check runs")
	}
}

// A claim whose release was lost (the network dropped) is removed later.
func TestFinishedItemsDoNotKeepTheirClaim(t *testing.T) {
	e := newEnv(t)
	it := e.ready("simple", "left behind", "x")
	e.start(it.ID)
	e.mustDeliver(it.ID, domain.Delivery{})
	if _, err := e.svc.CloseItem(e.ctx, "demo", owner(), it.ID, "abandoned", "", "not needed"); err != nil {
		t.Fatal(err)
	}
	// The release got lost: the claim is back on the arbiter.
	if _, err := e.arb.Claim(e.ctx, claimKey(it.ID), "m1"); err != nil {
		t.Fatal(err)
	}
	other := e.ready("simple", "still going", "x")
	e.start(other.ID)
	e.svc.releaseFinishedClaims(e.ctx, e.p)
	if info, _ := e.arb.Inspect(e.ctx, claimKey(it.ID)); info.Held {
		t.Fatal("the claim of a finished item must be released")
	}
	if info, _ := e.arb.Inspect(e.ctx, claimKey(other.ID)); !info.Held {
		t.Fatal("the claim of an item in progress stays")
	}
}

// worktree.setup runs once in a new worktree, before work or checks there,
// and again only if it never finished.
func TestWorktreeSetupRunsOncePerWorktree(t *testing.T) {
	e := newEnv(t, withGit(), withExec(), withCfg(func(c *domain.ProjectConfig) { c.WorktreeSetup = "make install" }))
	it := e.ready("simple", "needs its dependencies", "x")
	e.start(it.ID)
	dir := e.workspace(it.ID, map[string]string{"a.txt": "a\n"})
	setups := func() int {
		n := 0
		for _, c := range e.exec.Cmds() {
			if len(c.Args) == 2 && strings.Contains(c.Args[1], "make install") && c.Dir == dir {
				n++
			}
		}
		return n
	}
	if setups() != 1 {
		t.Fatalf("setup ran %d times in the new worktree", setups())
	}
	e.workspace(it.ID, nil)
	e.workspace(it.ID, nil)
	if setups() != 1 {
		t.Fatalf("setup must run once per worktree, ran %d times", setups())
	}
	if _, err := os.Stat(filepath.Join(dir, ".barnraise-session", "setup-done")); err != nil {
		t.Fatal("the marker of a finished setup is missing")
	}
	if out := testkit.RunGit(t, dir, "status", "--porcelain"); strings.Contains(out, "barnraise-session") {
		t.Fatalf("nothing of the setup is offered to git: %q", out)
	}
	// A setup that never finished runs again on the next use.
	if err := os.Remove(filepath.Join(dir, ".barnraise-session", "setup-done")); err != nil {
		t.Fatal(err)
	}
	e.workspace(it.ID, nil)
	if setups() != 2 {
		t.Fatalf("an unfinished setup must run again: %d", setups())
	}
}

// An item may continue work that already exists on a branch (work started
// before Barnraise drove the project): its worktree starts there, not on main.
func TestItemContinuesOnAnExistingBranch(t *testing.T) {
	e := newEnv(t, withGit())
	// Work someone already did, on a branch of their own.
	testkit.RunGit(t, e.repo.Root, "checkout", "-q", "-b", "old/issue-7")
	testkit.WriteFile(t, e.repo.Root, "half.txt", "half done\n")
	testkit.RunGit(t, e.repo.Root, "add", "-A")
	testkit.RunGit(t, e.repo.Root, "commit", "-q", "-m", "wip: half of it")
	testkit.RunGit(t, e.repo.Root, "push", "-q", "origin", "old/issue-7")
	testkit.RunGit(t, e.repo.Root, "checkout", "-q", "main")

	it := e.ready("simple", "Finish the export", "x")
	branch := "old/issue-7"
	got, err := e.svc.UpdateItem(e.ctx, "demo", owner(), it.ID, ItemPatch{Branch: &branch})
	if err != nil || got.Branch != branch {
		t.Fatalf("set branch: %+v %v", got, err)
	}
	e.start(it.ID)
	dir := e.workspace(it.ID, map[string]string{"rest.txt": "the rest\n"})
	if _, err := os.Stat(filepath.Join(dir, "half.txt")); err != nil {
		t.Fatal("the worktree must start from the existing work, not from main")
	}
	e.mustDeliver(it.ID, domain.Delivery{})
	e.drive(it.ID)
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	files := e.repo.MainFiles(t)
	if !contains(files, "half.txt") || !contains(files, "rest.txt") {
		t.Fatalf("both the old and the new work land: %v", files)
	}
	// Not once work started, not by an agent, and never main.
	other := e.ready("simple", "Another", "x")
	main := "main"
	if _, err := e.svc.UpdateItem(e.ctx, "demo", owner(), other.ID, ItemPatch{Branch: &main}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("main: %v", err)
	}
	e.start(other.ID)
	if _, err := e.svc.UpdateItem(e.ctx, "demo", owner(), other.ID, ItemPatch{Branch: &branch}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("after the start: %v", err)
	}
}

// The lock looks at what the branch changes now: a locked file reported in
// an earlier round and put back, or one that main brought in and the branch
// holds as main has it, is no change. I-56 was sent back four times for
// three such files.
func TestLockedTestsCountOnlyWhatTheBranchChanges(t *testing.T) {
	e := newEnv(t, withGit(), withFiles(map[string]string{"README.md": "# demo\n", "tests/a_test.sh": "exit 0\n"}))
	it := e.ready("simple", "keep the lock fair", "x")
	e.next("implementer", it.ID)
	// main moves on: a new test arrives there after the branch started.
	e.repo.PushToMain(t, "add b", map[string]string{"tests/b_test.sh": "exit 0 # b\n"})
	// The branch holds the same file as main (left by a stopped rebase),
	// and a.sh as main has it, though an earlier round reported it.
	e.workspace(it.ID, map[string]string{"tests/b_test.sh": "exit 0 # b\n", "app.go": "package app\n"})
	e.mustDeliver(it.ID, domain.Delivery{Files: []string{"tests/a_test.sh", "app.go"}})
	e.drive(it.ID)
	got := e.item(it.ID)
	for _, r := range got.Rework {
		if strings.Contains(r.Reason, "locked") {
			t.Fatalf("no locked change: %q", r.Reason)
		}
	}
	// A real change to a locked test is still caught.
	it2 := e.ready("simple", "weaken a test", "y")
	e.next("implementer", it2.ID)
	e.workspace(it2.ID, map[string]string{"tests/a_test.sh": "exit 0 # weakened\n"})
	e.mustDeliver(it2.ID, domain.Delivery{})
	e.drive(it2.ID)
	got2 := e.wantStatus(it2.ID, domain.StatusInProgress, "implement")
	if len(got2.Rework) == 0 || !strings.Contains(got2.Rework[0].Reason, "tests/a_test.sh") {
		t.Fatalf("rework %+v", got2.Rework)
	}
}
