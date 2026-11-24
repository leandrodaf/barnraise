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

// Where the worktree fronts meet: hooks and teardown run confined like the
// setup and get the shared stores; build caches arrive before seed, and
// again after strip; the hourly sweep retires leftover worktrees.

func TestHooksRunConfinedWithTheStores(t *testing.T) {
	ll := landlockOK()
	e := newEnv(t, withGit(), withExec(), withIsolation(Isolation{Landlock: ll}), withCfg(func(c *domain.ProjectConfig) {
		c.WorktreeStores = map[string]string{"pnpm": "npm_config_store_dir"}
		c.Worktree.Hooks.PostCreate = "make db"
		c.Worktree.Isolate.Teardown = "dropdb app"
	}))
	it := e.ready("simple", "with services", "x")
	e.start(it.ID)
	dir := e.workspace(it.ID, nil)
	store := e.svc.storeDir(e.cfg.Name, "pnpm")
	for _, want := range []string{"make db"} {
		cmds := e.cmdsWith(want)
		if len(cmds) != 1 || cmds[0].Name != "confined-landlock" || cmds[0].Args[0] != domain.SandboxSetup {
			t.Fatalf("%s must run in the setup profile: %+v", want, cmds)
		}
		if envOf(cmds[0])["npm_config_store_dir"] != store || envOf(cmds[0])["COMPOSE_PROJECT_NAME"] == "" {
			t.Fatalf("%s gets the stores and the isolation: %v", want, cmds[0].Env)
		}
	}
	e.svc.ReleaseServices(e.ctx, e.p, it.ID, dir)
	if cmds := e.cmdsWith("dropdb app"); len(cmds) != 1 || cmds[0].Name != "confined-landlock" {
		t.Fatalf("teardown runs confined: %+v", cmds)
	}
}

func TestBuildCacheBeforeSeedAndAgainAfterStrip(t *testing.T) {
	cl := &fakeCloner{}
	e := newEnv(t, withGit(), withExec(), withCfg(func(c *domain.ProjectConfig) {
		c.WorktreeSetup = "make install"
		c.Worktree.BuildCache = []string{"target"}
		c.WorktreeSeed = []string{"target", "node_modules"}
		c.WorktreeStrip = []string{"target"}
		c.WorktreeStripAfter = time.Hour
	}))
	e.svc.d.Cloner = cl
	testkit.WriteFile(t, e.repo.Root, "target/x", "from the root\n")
	testkit.WriteFile(t, e.repo.Root, "node_modules/a.js", "a\n")
	it := e.ready("simple", "warm", "x")
	e.start(it.ID)
	dir := e.workspace(it.ID, nil)
	if len(cl.Calls()) != 1 || !strings.HasSuffix(cl.Calls()[0][1], filepath.Join(it.ID, "target")) {
		t.Fatalf("the build cache is cloned first: %v", cl.Calls())
	}
	if !exists(filepath.Join(dir, "target/x")) || !exists(filepath.Join(dir, "node_modules/a.js")) {
		t.Fatal("seed fills what the cache does not cover")
	}
	e.setStatus(it.ID, domain.StatusWaitingOwner)
	e.clock.Advance(2 * time.Hour)
	if got := e.svc.trimWorkspaces(e.ctx, e.p, nil); got.Stripped != 1 {
		t.Fatalf("stripped %+v", got)
	}
	e.setStatus(it.ID, domain.StatusInProgress)
	e.workspace(it.ID, nil)
	if len(cl.Calls()) != 2 || !exists(filepath.Join(dir, "target/x")) {
		t.Fatalf("a stripped worktree gets its build cache again: %v", cl.Calls())
	}
}

func TestSweepRetiresLeftoverWorktrees(t *testing.T) {
	e := newEnv(t, withGit(), withExec(), withCfg(func(c *domain.ProjectConfig) {
		c.Worktree.Hooks.PreRemove = "make clean"
		c.Worktree.Isolate.Teardown = "dropdb app"
	}))
	closed := e.ready("simple", "given up", "x")
	e.start(closed.ID)
	dir := e.workspace(closed.ID, nil)
	e.setStatus(closed.ID, domain.StatusClosed) // a crash between the event and the removal
	e.clock.Advance(2 * time.Hour)
	if got := e.svc.trimWorkspaces(e.ctx, e.p, nil); got.Finished != 1 {
		t.Fatalf("trimmed %+v", got)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("removed")
	}
	if len(e.cmdsWith("make clean")) != 1 || len(e.cmdsWith("dropdb app")) != 1 {
		t.Fatalf("the sweep runs pre_remove and the teardown: %v", e.exec.Cmds())
	}
}
