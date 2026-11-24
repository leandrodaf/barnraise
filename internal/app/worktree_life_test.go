package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

// fakeCloner copies trees and records each call; unsupported makes it
// answer like a filesystem without copy-on-write.
type fakeCloner struct {
	mu          sync.Mutex
	calls       [][2]string
	unsupported bool
	failWith    error
}

func (c *fakeCloner) CloneTree(_ context.Context, src, dst string, fullCopy bool) error {
	c.mu.Lock()
	c.calls = append(c.calls, [2]string{src, dst})
	c.mu.Unlock()
	if c.failWith != nil {
		return c.failWith
	}
	if c.unsupported && !fullCopy {
		return ports.ErrCloneUnsupported
	}
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
}

func (c *fakeCloner) Calls() [][2]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][2]string(nil), c.calls...)
}

func envOf(c ports.Cmd) map[string]string {
	out := map[string]string{}
	for _, kv := range c.Env {
		k, v, _ := strings.Cut(kv, "=")
		out[k] = v
	}
	return out
}

// cmdsWith returns the commands whose shell text contains s.
func (e *env) cmdsWith(s string) []ports.Cmd {
	var out []ports.Cmd
	for _, c := range e.exec.Cmds() {
		if strings.Contains(strings.Join(append([]string{c.Name}, c.Args...), " "), s) {
			out = append(out, c)
		}
	}
	return out
}

func isolatedCfg(c *domain.ProjectConfig) {
	c.WorktreeSetup = "make install"
	c.EnvFiles = []string{".env"}
	c.Worktree.PortRange = 10
	c.Worktree.Isolate.Env = map[string]string{
		"DB_NAME":      "app_{{.Item}}",
		"DATABASE_URL": `{{dbURL (env "DATABASE_URL") (print "app_" .Item)}}`,
	}
}

// Each item worktree gets its own compose project, database and ports, in
// its setup, its checks and its agent's session.
func TestWorktreeIsolationReachesSetupChecksAndSession(t *testing.T) {
	e := newEnv(t, withGit(), withExec(), withCfg(isolatedCfg))
	testkit.WriteFile(t, e.repo.Root, ".env", "DATABASE_URL=\"postgres://dev@localhost:5432/app?sslmode=disable\"\n")
	it := e.ready("simple", "isolated", "x")
	e.start(it.ID)
	dir := e.workspace(it.ID, map[string]string{"a.txt": "a\n"})
	slug := domain.ItemSlug(it.ID)
	first, _ := domain.ItemPortRange(e.cfg.PortBase, 10, it.ID)
	setups := e.cmdsWith("make install")
	if len(setups) != 1 {
		t.Fatalf("setup ran %d times", len(setups))
	}
	env := envOf(setups[0])
	want := map[string]string{
		"COMPOSE_PROJECT_NAME": domain.ComposeProjectName("demo", it.ID), "DB_NAME": "app_" + slug,
		"DATABASE_URL": "postgres://dev@localhost:5432/app_" + slug + "?sslmode=disable", "BARNRAISE_ITEM": it.ID,
		"PORT": strconv.Itoa(first), "BARNRAISE_PORTS": strconv.Itoa(first) + "-" + strconv.Itoa(first+9),
	}
	for k, w := range want {
		if env[k] != w {
			t.Errorf("setup %s = %q, want %q", k, env[k], w)
		}
	}
	if env["PATH"] == "" {
		t.Error("the daemon's environment is kept under the isolation")
	}
	// The checks of the item see the same identity.
	e.mustDeliver(it.ID, domain.Delivery{})
	e.drive(it.ID)
	specs := e.checks.Specs()
	if len(specs) == 0 {
		t.Fatal("no check ran")
	}
	for _, sp := range specs {
		if sp.Dir == dir && (sp.Env["DB_NAME"] != "app_"+slug || sp.Env["COMPOSE_PROJECT_NAME"] != want["COMPOSE_PROJECT_NAME"] || sp.Env["PORT"] != strconv.Itoa(first)) {
			t.Fatalf("check env %v", sp.Env)
		}
	}
	// Two items never share a database or a compose project.
	other := e.ready("simple", "second", "x")
	if a, b := e.svc.worktreeEnv(e.p, it.ID, dir), e.svc.worktreeEnv(e.p, other.ID, dir); a["DB_NAME"] == b["DB_NAME"] || a["COMPOSE_PROJECT_NAME"] == b["COMPOSE_PROJECT_NAME"] || a["PORT"] == b["PORT"] {
		t.Fatalf("shared identity: %v / %v", a, b)
	}
}

func TestSessionGetsTheIsolationAndPreSessionHook(t *testing.T) {
	e := newEnv(t, withExec(), withCfg(func(c *domain.ProjectConfig) {
		isolatedCfg(c)
		c.Worktree.Hooks.PreSession = "make warm"
	}))
	e.account("fake-1")
	it := e.ready("simple", "session", "x")
	e.fill()
	e.idle()
	specs := e.prov.Specs()
	if len(specs) == 0 {
		t.Fatal("no session")
	}
	env := specs[0].Env
	first, _ := domain.ItemPortRange(e.cfg.PortBase, 10, it.ID)
	if env["COMPOSE_PROJECT_NAME"] != domain.ComposeProjectName("demo", it.ID) || env["DB_NAME"] != "app_"+domain.ItemSlug(it.ID) || env["PORT"] != strconv.Itoa(first) {
		t.Fatalf("session env %v", env)
	}
	if env["BARNRAISE_TOKEN"] == "" || env["BARNRAISE_ITEM"] != it.ID {
		t.Fatal("Barnraise's own variables stay")
	}
	hooks := e.cmdsWith("make warm")
	if len(hooks) == 0 || envOf(hooks[0])["BARNRAISE_HOOK"] != domain.HookPreSession || envOf(hooks[0])["DB_NAME"] == "" {
		t.Fatalf("pre_session hook %+v", hooks)
	}
}

// A pre_session hook that fails keeps the session from starting.
func TestFailingPreSessionHookHoldsTheSession(t *testing.T) {
	e := newEnv(t, withExec(), withCfg(func(c *domain.ProjectConfig) { c.Worktree.Hooks.PreSession = "make warm" }))
	e.exec.Results = map[string]ports.ExecResult{"sh -c exec 2>&1\nmake warm": {ExitCode: 3, Stdout: []byte("no database")}}
	e.account("fake-1")
	e.ready("simple", "held", "x")
	e.fill()
	e.idle()
	if n := len(e.prov.Specs()); n != 0 {
		t.Fatalf("%d sessions opened past a failing pre_session hook", n)
	}
}

// post_create runs once after setup; integration runs post_integrate,
// keeps the build caches, runs pre_remove, the teardown and compose down,
// then removes the worktree.
func TestWorktreeHooksCachesAndCleanupAcrossTheItemsLife(t *testing.T) {
	cl := &fakeCloner{}
	e := newEnv(t, withGit(), withExec(), withCfg(func(c *domain.ProjectConfig) {
		c.WorktreeSetup = "make install"
		c.Worktree.BuildCache = []string{"target", ".next/cache"}
		c.Worktree.Isolate.Teardown = "dropdb app"
		c.Worktree.Hooks = domain.WorktreeHooks{PostCreate: "make db", PostIntegrate: "make publish", PreRemove: "make clean"}
	}))
	e.svc.d.Cloner = cl
	testkit.WriteFile(t, e.repo.Root, "target/debug/app.o", "from main checkout")
	it := e.ready("simple", "life", "x")
	e.start(it.ID)
	dir := e.workspace(it.ID, map[string]string{"compose.yaml": "services: {}\n"})
	e.workspace(it.ID, nil)
	if b, err := os.ReadFile(filepath.Join(dir, "target", "debug", "app.o")); err != nil || string(b) != "from main checkout" {
		t.Fatalf("build cache from the main checkout: %q %v", b, err)
	}
	if calls := cl.Calls(); len(calls) != 1 || calls[0][0] != filepath.Join(e.repo.Root, "target") {
		t.Fatalf("clones %v (a directory nobody has is not cloned)", calls)
	}
	if n := len(e.cmdsWith("make db")); n != 1 {
		t.Fatalf("post_create ran %d times", n)
	}
	setupAt, createAt := -1, -1
	for i, c := range e.exec.Cmds() {
		text := strings.Join(c.Args, " ")
		if strings.Contains(text, "make install") && setupAt < 0 {
			setupAt = i
		}
		if strings.Contains(text, "make db") {
			createAt = i
		}
	}
	if setupAt < 0 || createAt < setupAt {
		t.Fatalf("post_create runs after setup: %d %d", setupAt, createAt)
	}
	// The worktree builds something of its own before it lands.
	testkit.WriteFile(t, dir, "target/debug/app.o", "built by "+it.ID)
	e.mustDeliver(it.ID, domain.Delivery{})
	e.drive(it.ID)
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the integrated worktree must go: %v", err)
	}
	var order []string
	for _, c := range e.exec.Cmds() {
		text := strings.Join(append([]string{c.Name}, c.Args...), " ")
		for _, w := range []string{"make publish", "make clean", "dropdb app", "docker compose -p " + domain.ComposeProjectName("demo", it.ID) + " down -v"} {
			if strings.Contains(text, w) {
				order = append(order, w)
			}
		}
	}
	if len(order) != 4 || order[0] != "make publish" || order[1] != "make clean" || order[2] != "dropdb app" || !strings.HasPrefix(order[3], "docker compose") {
		t.Fatalf("end of life order %v", order)
	}
	snap := filepath.Join(e.svc.buildCacheDir("demo"), "target", "debug", "app.o")
	if b, err := os.ReadFile(snap); err != nil || string(b) != "built by "+it.ID {
		t.Fatalf("the integrated worktree's build cache is kept: %q %v", b, err)
	}
	// The next worktree starts from it, not from the main checkout.
	next := e.ready("simple", "next", "x")
	e.start(next.ID)
	ndir := e.workspace(next.ID, nil)
	if b, _ := os.ReadFile(filepath.Join(ndir, "target", "debug", "app.o")); string(b) != "built by "+it.ID {
		t.Fatalf("the next worktree starts from the last integrated build: %q", b)
	}
}

// Without copy-on-write the build cache is skipped, never copied in full,
// unless build_cache_fallback is copy.
func TestBuildCacheSkipsWithoutCopyOnWrite(t *testing.T) {
	cl := &fakeCloner{unsupported: true}
	e := newEnv(t, withGit(), withCfg(func(c *domain.ProjectConfig) { c.Worktree.BuildCache = []string{"target"} }))
	e.svc.d.Cloner = cl
	testkit.WriteFile(t, e.repo.Root, "target/x", "x")
	it := e.ready("simple", "cold", "x")
	e.start(it.ID)
	dir := e.workspace(it.ID, nil)
	if _, err := os.Stat(filepath.Join(dir, "target")); !os.IsNotExist(err) {
		t.Fatalf("skipped, not copied: %v", err)
	}
	if len(cl.Calls()) != 1 {
		t.Fatal(cl.Calls())
	}
	e.cfg.Worktree.BuildCacheCopy = true
	other := e.ready("simple", "warm", "x")
	e.start(other.ID)
	odir := e.workspace(other.ID, nil)
	if _, err := os.Stat(filepath.Join(odir, "target", "x")); err != nil {
		t.Fatalf("copied when asked: %v", err)
	}
	// Any other error is logged; the worktree is still made.
	cl.failWith = errors.New("disk full")
	third := e.ready("simple", "third", "x")
	e.start(third.ID)
	e.workspace(third.ID, nil)
}

// Closing an item retires its worktree: pre_remove, then its services
// once, then the worktree goes (nothing uses a closed item's worktree again).
func TestCloseRetiresTheItemsWorktree(t *testing.T) {
	e := newEnv(t, withGit(), withExec(), withCfg(func(c *domain.ProjectConfig) {
		c.Worktree.Isolate.Compose = "on"
		c.Worktree.Isolate.Teardown = "dropdb app"
		c.Worktree.Hooks.PreRemove = "make clean"
	}))
	it := e.ready("simple", "abandon", "x")
	e.start(it.ID)
	dir := e.workspace(it.ID, nil)
	if _, err := e.svc.CloseItem(e.ctx, "demo", owner(), it.ID, "abandoned", "", "not needed"); err != nil {
		t.Fatal(err)
	}
	downs := e.cmdsWith("docker compose -p " + domain.ComposeProjectName("demo", it.ID) + " down -v --remove-orphans")
	if len(downs) != 1 || len(e.cmdsWith("dropdb app")) != 1 || len(e.cmdsWith("make clean")) != 1 {
		t.Fatalf("pre_remove and the services once: %d downs, %v", len(downs), e.exec.Cmds())
	}
	if envOf(downs[0])["COMPOSE_PROJECT_NAME"] == "" || downs[0].Dir != dir {
		t.Fatalf("compose down %+v", downs[0])
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("a closed item's worktree is removed")
	}
	// Nothing to do for a worktree that is gone.
	e.svc.retireWorktree(e.ctx, e.p, it.ID, dir, retireRemove)
	if len(e.cmdsWith("make clean")) != 1 {
		t.Fatalf("retired twice: %v", e.exec.Cmds())
	}
}

// Releasing services is once per worktree, and a later removal still runs
// pre_remove.
func TestReleaseServicesOnce(t *testing.T) {
	e := newEnv(t, withGit(), withExec(), withCfg(func(c *domain.ProjectConfig) {
		c.Worktree.Isolate.Teardown = "dropdb app"
		c.Worktree.Hooks.PreRemove = "make clean"
	}))
	it := e.ready("simple", "abandon", "x")
	e.start(it.ID)
	dir := e.workspace(it.ID, nil)
	e.svc.ReleaseServices(e.ctx, e.p, it.ID, dir)
	e.svc.ReleaseServices(e.ctx, e.p, it.ID, dir)
	if len(e.cmdsWith("dropdb app")) != 1 || len(e.cmdsWith("make clean")) != 0 {
		t.Fatalf("release: %v", e.exec.Cmds())
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("a release keeps the worktree")
	}
	e.svc.retireWorktree(e.ctx, e.p, it.ID, dir, retireRemove)
	if len(e.cmdsWith("dropdb app")) != 1 || len(e.cmdsWith("make clean")) != 1 {
		t.Fatalf("released twice: %v", e.exec.Cmds())
	}
}

// compose: auto tears down only where a compose file is; off never.
func TestComposeDownOnlyWhenComposeWasUsed(t *testing.T) {
	e := newEnv(t, withGit(), withExec())
	a := e.ready("simple", "no compose", "x")
	e.start(a.ID)
	dirA := e.workspace(a.ID, nil)
	e.svc.ReleaseServices(e.ctx, e.p, a.ID, dirA)
	if n := len(e.cmdsWith("docker")); n != 0 {
		t.Fatalf("no compose file, no docker: %d", n)
	}
	b := e.ready("simple", "compose", "x")
	e.start(b.ID)
	dirB := e.workspace(b.ID, map[string]string{"docker-compose.yml": "services: {}\n"})
	e.exec.Err = errors.New("docker: not found")
	e.svc.ReleaseServices(e.ctx, e.p, b.ID, dirB)
	if n := len(e.cmdsWith("docker compose")); n != 1 {
		t.Fatalf("compose file, compose down: %d", n)
	}
	e.exec.Err = nil
	e.cfg.Worktree.Isolate.Compose = "off"
	c := e.ready("simple", "off", "x")
	e.start(c.ID)
	dirC := e.workspace(c.ID, map[string]string{"compose.yml": "services: {}\n"})
	e.svc.ReleaseServices(e.ctx, e.p, c.ID, dirC)
	if n := len(e.cmdsWith("docker compose")); n != 1 {
		t.Fatalf("compose off: %d", n)
	}
	// A failing teardown is logged; compose still goes down.
	e.cfg.Worktree.Isolate.Compose = "on"
	e.cfg.Worktree.Isolate.Teardown = "dropdb x"
	e.exec.Results = map[string]ports.ExecResult{"sh -c exec 2>&1\ndropdb x": {ExitCode: 1}, "docker compose -p " + domain.ComposeProjectName("demo", c.ID) + " down -v --remove-orphans": {ExitCode: 1}}
	_ = os.Remove(filepath.Join(dirC, filepath.FromSlash(releasedMarker)))
	e.svc.ReleaseServices(e.ctx, e.p, c.ID, dirC)
	if n := len(e.cmdsWith("docker compose")); n != 2 {
		t.Fatalf("compose on after a failed teardown: %d", n)
	}
	// No item, nothing to release.
	e.svc.ReleaseServices(e.ctx, e.p, "", dirC)
}

func TestWorktreeKeyAndScratchCheckouts(t *testing.T) {
	e := newEnv(t, withExec(), withCfg(func(c *domain.ProjectConfig) { c.WorktreeSetup = "make install" }))
	for dir, want := range map[string]string{
		e.svc.WorktreeDir("demo", "I-4"):           "I-4",
		e.svc.WorktreeDir("demo", "I-4~b"):         "I-4~b",
		e.svc.WorktreeDir("demo", "_main-abc"):     "",
		e.svc.WorktreeDir("demo", "_refine-I-4"):   "",
		e.svc.WorktreeDir("other", "I-4"):          "",
		filepath.Join(t.TempDir(), "worktree-I-4"): "",
	} {
		if got := e.svc.worktreeKeyOf("demo", dir); got != want {
			t.Errorf("key of %s = %q, want %q", dir, got, want)
		}
	}
	// A scratch checkout of no item inherits the environment, as before.
	scratch := e.svc.WorktreeDir("demo", "_main-abc")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetupWorktree(e.ctx, e.p, scratch); err != nil {
		t.Fatal(err)
	}
	// The scratch checkout of a check moved here gets the item's identity.
	remote := e.svc.WorktreeDir("demo", "_remote-I-4-x")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetupWorktreeFor(e.ctx, e.p, remote, "I-4"); err != nil {
		t.Fatal(err)
	}
	cmds := e.cmdsWith("make install")
	if len(cmds) != 2 || cmds[0].Env != nil || envOf(cmds[1])["COMPOSE_PROJECT_NAME"] != "demo-i4" {
		t.Fatalf("setups %+v", cmds)
	}
	// A failing hook says what failed.
	e.cfg.Worktree.Hooks.PostCreate = "make db"
	e.exec.Results = map[string]ports.ExecResult{"sh -c exec 2>&1\nmake db": {ExitCode: 2, Stdout: []byte("no server")}}
	if err := e.svc.prepareWorktree(e.ctx, e.p, remote, "I-4", nil); err == nil || !strings.Contains(err.Error(), "post_create") || !strings.Contains(err.Error(), "no server") {
		t.Fatalf("post_create failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(remote, filepath.FromSlash(postCreateMarker))); err == nil {
		t.Fatal("a failed post_create runs again next time")
	}
}

func TestIncludedEnvParsing(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	testkit.WriteFile(t, dir, ".env", "# comment\n\nexport A=1\nB='two'\nC = \"three\"\nbroken\n")
	got := e.svc.includedEnv(&domain.ProjectConfig{EnvFiles: []string{".env", ".missing"}}, dir)
	if got["A"] != "1" || got["B"] != "two" || got["C"] != "three" || len(got) != 3 {
		t.Fatal(got)
	}
	if environ(nil) != nil {
		t.Fatal("no isolation, inherited environment")
	}
	// A template that cannot render at run time falls back to the base environment.
	e.cfg.Worktree.Isolate.Env = map[string]string{"X": "{{.Nope}}"}
	if env := e.svc.worktreeEnv(e.p, "I-1", dir); env["X"] != "" || env["COMPOSE_PROJECT_NAME"] == "" {
		t.Fatal(env)
	}
}
