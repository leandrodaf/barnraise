package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

// toQueue brings a simple item with files to the merge queue.
func (e *env) toQueue(title string, files map[string]string) *domain.Item {
	e.t.Helper()
	e.clock.Advance(time.Second)
	it := e.ready("simple", title, "x")
	e.start(it.ID)
	e.workspace(it.ID, files)
	e.mustDeliver(it.ID, domain.Delivery{})
	e.drive(it.ID)
	return e.wantStatus(it.ID, domain.StatusQueued, "integrate")
}

func contains(list []string, v string) bool { return domain.Contains(list, v) }

func TestMergeQueueSquash(t *testing.T) {
	e := newEnv(t, withGit(), withHost())
	dep := e.toQueue("Cards screen", map[string]string{"app/cards.txt": "cards\n", "plan.md": "# plan\n"})
	after := e.create(NewItem{Title: "After cards", Criteria: crit(ears), Deps: []string{dep.ID}})
	q, err := e.svc.Queue("demo", owner())
	if err != nil || len(q) != 1 || q[0].Item.ID != dep.ID || !q[0].Here {
		t.Fatalf("queue %+v %v", q, err)
	}
	n, err := e.svc.ProcessQueue(e.ctx, "demo")
	if err != nil || n != 1 {
		t.Fatalf("process %d %v", n, err)
	}
	got := e.wantStatus(dep.ID, domain.StatusIntegrated, "integrate")
	if got.MergeCommit == "" || got.MergeCommit != e.repo.RemoteMain(t) || got.Claim != nil {
		t.Fatalf("merge commit %s, main %s", got.MergeCommit, e.repo.RemoteMain(t))
	}
	log := e.repo.MainLog(t)
	if !strings.Contains(log, dep.ID+": Cards screen") || !strings.Contains(log, "Barnraise-Item: "+dep.ID) {
		t.Fatalf("main log %s", log)
	}
	files := e.repo.MainFiles(t)
	if !contains(files, "app/cards.txt") || contains(files, "plan.md") || contains(files, "criteria.json") {
		t.Fatalf("main files %v", files)
	}
	if _, err := os.Stat(e.svc.WorktreeDir("demo", dep.ID)); !os.IsNotExist(err) {
		t.Fatal("the worktree is removed after integration")
	}
	e.wantStatus(after.ID, domain.StatusReady, "")
	if e.arb.(interface {
		Inspect(context.Context, string) (ports.LeaseInfo, error)
	}) == nil {
		t.Fatal()
	}
	if info, _ := e.arb.Inspect(e.ctx, claimKey(dep.ID)); info.Held {
		t.Fatal("the claim is released after integration")
	}
	eventually(t, "integration comment", func() bool { return e.host.Count("Comment 1 Integrated into main") == 1 })
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 0 || err != nil {
		t.Fatalf("empty queue %d %v", n, err)
	}
	if _, err := e.svc.Queue("demo", Actor{}); err == nil {
		t.Fatal("queue without scope")
	}
	if _, err := e.svc.ProcessQueue(e.ctx, "nope"); err == nil {
		t.Fatal("unknown project")
	}
}

func TestMergeQueuePausedOrWithoutGit(t *testing.T) {
	e := newEnv(t)
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	g := newEnv(t, withGit())
	g.toQueue("x", map[string]string{"x.txt": "x\n"})
	if err := g.svc.Pause(g.ctx, "demo", owner(), "freeze"); err != nil {
		t.Fatal(err)
	}
	if n, err := g.svc.ProcessQueue(g.ctx, "demo"); n != 0 || err != nil {
		t.Fatal(n, err)
	}
}

// staleOnce makes main move right before the first integration, as if another machine won.
type staleOnce struct {
	ports.Arbiter
	once   sync.Once
	before func()
}

func (s *staleOnce) Integrate(ctx context.Context, expected, next string) error {
	s.once.Do(s.before)
	return s.Arbiter.Integrate(ctx, expected, next)
}

func TestMergeQueueStaleMainRetries(t *testing.T) {
	clock := testkit.NewClock()
	repo := testkit.NewRepo(t, clock, nil)
	st := &staleOnce{Arbiter: repo.Arbiter}
	st.before = func() { repo.PushToMain(t, "other machine", map[string]string{"other.txt": "other\n"}) }
	e := newEnv(t, withRepo(repo), withClock(clock))
	e.p.SetPorts(ProjectPorts{Git: repo.Git, Arbiter: st})
	it := e.toQueue("Wallet", map[string]string{"wallet.txt": "wallet\n"})
	n, err := e.svc.ProcessQueue(e.ctx, "demo")
	if err != nil || n != 1 {
		t.Fatalf("process %d %v", n, err)
	}
	e.wantStatus(it.ID, domain.StatusIntegrated, "")
	files := repo.MainFiles(t)
	if !contains(files, "other.txt") || !contains(files, "wallet.txt") {
		t.Fatalf("main %v", files)
	}
}

func TestMergeQueueConflictIsRework(t *testing.T) {
	e := newEnv(t, withGit())
	it := e.toQueue("Readme", map[string]string{"README.md": "# demo by the item\n"})
	e.repo.PushToMain(t, "someone else", map[string]string{"README.md": "# demo by someone else\n"})
	n, err := e.svc.ProcessQueue(e.ctx, "demo")
	if err != nil || n != 0 {
		t.Fatalf("process %d %v", n, err)
	}
	got := e.wantStatus(it.ID, domain.StatusInProgress, "implement")
	if r := got.Rework[0]; r.From != "integrate" || !strings.Contains(r.Reason, "merge queue: rebase on main conflicts") || !strings.Contains(r.Reason, "README.md") {
		t.Fatalf("rework %+v", got.Rework)
	}
}

func TestMergeQueueBatchBisection(t *testing.T) {
	e := newEnv(t, withGit(), withCfg(func(c *domain.ProjectConfig) { c.Merge.Batch = 4 }))
	var combined []string
	var mu sync.Mutex
	e.checks.Func = func(spec ports.CheckSpec) (domain.CheckResult, bool, error) {
		if spec.Def.Scope != "all" {
			return domain.CheckResult{Passed: true, Summary: "item check"}, false, nil
		}
		_, err := os.Stat(filepath.Join(spec.Dir, "bad.txt"))
		mu.Lock()
		combined = append(combined, spec.ItemID)
		mu.Unlock()
		if err == nil {
			return domain.CheckResult{Passed: false, Summary: "bad.txt breaks the build"}, false, nil
		}
		return domain.CheckResult{Passed: true, Summary: "green"}, false, nil
	}
	a := e.toQueue("A", map[string]string{"a.txt": "a\n"})
	b := e.toQueue("B", map[string]string{"b.txt": "b\n"})
	c := e.toQueue("C", map[string]string{"bad.txt": "bad\n"})
	d := e.toQueue("D", map[string]string{"d.txt": "d\n"})
	n, err := e.svc.ProcessQueue(e.ctx, "demo")
	if err != nil || n != 3 {
		t.Fatalf("integrated %d, %v (combined checks: %v)", n, err, combined)
	}
	for _, it := range []*domain.Item{a, b, d} {
		e.wantStatus(it.ID, domain.StatusIntegrated, "")
	}
	got := e.wantStatus(c.ID, domain.StatusInProgress, "implement")
	if !strings.Contains(got.Rework[0].Reason, "bad.txt breaks the build") {
		t.Fatal(got.Rework[0].Reason)
	}
	files := e.repo.MainFiles(t)
	if !contains(files, "a.txt") || !contains(files, "b.txt") || !contains(files, "d.txt") || contains(files, "bad.txt") {
		t.Fatalf("main %v", files)
	}
}

func TestMergeQueueChangelogAndSteps(t *testing.T) {
	e := newEnv(t, withGit(), withCfg(func(c *domain.ProjectConfig) { c.Merge.Changelog = "CHANGELOG.md" }))
	it := e.toQueue("Profile", map[string]string{"profile.txt": "p\n"})
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	out := testkit.RunGit(t, e.repo.Root, "--git-dir", e.repo.Remote, "show", "main:CHANGELOG.md")
	if !strings.Contains(out, "- "+it.ID+" Profile") {
		t.Fatalf("changelog %q", out)
	}

	s := newEnv(t, withGit(), withCfg(func(c *domain.ProjectConfig) { c.Merge.Mode = "steps" }))
	it = s.toQueue("Steps", map[string]string{"s.txt": "s\n"})
	if n, err := s.svc.ProcessQueue(s.ctx, "demo"); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	log := s.repo.MainLog(t)
	if !strings.Contains(log, it.ID+": done step implement") {
		t.Fatalf("steps mode keeps the item's commits: %s", log)
	}
}

func TestMergeQueueCombinedCheckFails(t *testing.T) {
	e := newEnv(t, withGit())
	it := e.toQueue("Breaks main", map[string]string{"x.txt": "x\n"})
	e.checks.Script("main", testkit.CheckOutcome{Passed: false, Summary: "flaky on main"})
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	got := e.wantStatus(it.ID, domain.StatusInProgress, "implement")
	if !strings.Contains(got.Rework[0].Reason, "check main failed on the combined result: flaky on main") {
		t.Fatal(got.Rework[0].Reason)
	}
	if ok, why := e.svc.runQueueChecks(e.ctx, e.p, "", "", nil); !ok || why != "" {
		t.Fatal("no directory: nothing to check")
	}
}
