package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// age sets a directory's time to d before the test clock's now.
func (e *env) age(dir string, d time.Duration) {
	e.t.Helper()
	at := e.clock.Now().Add(-d)
	if err := os.Chtimes(dir, at, at); err != nil {
		e.t.Fatal(err)
	}
}

// noTrash waits for the background deletions to finish.
func (e *env) noTrash() {
	e.t.Helper()
	eventually(e.t, "the removed directories to be deleted", func() bool {
		ents, _ := os.ReadDir(e.svc.WorktreeDir("demo", ""))
		for _, ent := range ents {
			if ports.IsTrash(ent.Name()) {
				return false
			}
		}
		return true
	})
}

// scratch adds a detached checkout of main under the project's worktrees.
func (e *env) scratch(name string) string {
	e.t.Helper()
	dir := e.svc.WorktreeDir("demo", name)
	if err := e.p.Ports().Git.AddDetachedWorktree(e.ctx, dir, "origin/main"); err != nil {
		e.t.Fatal(err)
	}
	return dir
}

func (e *env) setStatus(id string, st domain.Status) {
	e.t.Helper()
	if _, err := e.svc.mutateItem(e.ctx, e.p, id, domain.EvItemUpdated, "test", func(it *domain.Item) error {
		it.Status = st
		return nil
	}, domain.Payload{}); err != nil {
		e.t.Fatal(err)
	}
}

// The analyst's checkout is kept while the item is a draft and no longer:
// once it leaves draft (or is gone) nothing reads it again.
func TestTrimRefineWorkspaces(t *testing.T) {
	e := newEnv(t, withGit())
	draft := e.create(NewItem{Title: "vague"})
	busy := e.ready("feature", "being refined right now", "cards")
	done := e.ready("feature", "clear", "cards")
	keepDraft, keepBusy := e.scratch("_refine-"+draft.ID), e.scratch("_refine-"+busy.ID)
	goneReady, goneUnknown := e.scratch("_refine-"+done.ID), e.scratch("_refine-I-999")
	got := e.svc.trimWorkspaces(e.ctx, e.p, map[string]bool{busy.ID: true})
	if got.Refine != 2 || got.total() != 2 {
		t.Fatalf("trimmed %+v, want the ready item's and the unknown one's", got)
	}
	if !exists(keepDraft) || !exists(keepBusy) || exists(goneReady) || exists(goneUnknown) {
		t.Fatalf("draft %v busy %v ready %v unknown %v", exists(keepDraft), exists(keepBusy), exists(goneReady), exists(goneUnknown))
	}
}

// The sweep removes the worktrees nothing uses again, each after its time:
// a finished item's, one of an item this machine does not know, a scratch
// checkout left by a crash, and what was renamed for removal and never deleted.
func TestTrimReconcilesWorktreesWithItems(t *testing.T) {
	e := newEnv(t, withGit())
	live := e.ready("simple", "still going", "x")
	e.start(live.ID)
	liveDir := e.workspace(live.ID, nil)
	closed := e.ready("simple", "given up", "x")
	e.start(closed.ID)
	closedDir := e.workspace(closed.ID, nil)
	e.setStatus(closed.ID, domain.StatusClosed) // as a crash between the event and the removal leaves it

	unknown, unknownAlt, notOurs := e.scratch("I-900"), e.scratch("I-901~b"), e.scratch("notes")
	remote, repro := e.scratch("_remote-I-3-abc"), filepath.Join(e.svc.d.DataDir, "repro", "demo", "I-4-xyz")
	if err := e.p.Ports().Git.AddDetachedWorktree(e.ctx, repro, "origin/main"); err != nil {
		t.Fatal(err)
	}
	trash := e.svc.WorktreeDir("demo", ports.TrashPrefix+"I-7-abc")
	testkit.WriteFile(t, trash, "node_modules/x/index.js", "x\n")
	reproTrash := filepath.Join(e.svc.d.DataDir, "repro", "demo", ports.TrashPrefix+"I-8-abc")
	testkit.WriteFile(t, reproTrash, "a.txt", "a\n")
	for _, dir := range []string{unknown, unknownAlt, notOurs, remote, repro} {
		e.age(dir, time.Minute)
	}

	// Too soon for everything but the trash, which nothing ever uses.
	got := e.svc.trimWorkspaces(e.ctx, e.p, nil)
	if got.Trash != 2 || got.total() != 2 {
		t.Fatalf("first sweep trimmed %+v, want only the two trash directories", got)
	}
	if exists(trash) || exists(reproTrash) {
		t.Fatal("trash left behind")
	}

	e.clock.Advance(2 * time.Hour) // past finishedKeep
	got = e.svc.trimWorkspaces(e.ctx, e.p, nil)
	if got.Finished != 1 || got.total() != 1 || exists(closedDir) {
		t.Fatalf("after two hours trimmed %+v, want the closed item's worktree", got)
	}

	e.noTrash()
	e.clock.Advance(25 * time.Hour) // past unknownKeep and scratchKeep
	got = e.svc.trimWorkspaces(e.ctx, e.p, map[string]bool{"I-901": true})
	if got.Unknown != 1 || got.Scratch != 2 || got.total() != 3 {
		t.Fatalf("after a day trimmed %+v, want one unknown worktree and two scratch checkouts", got)
	}
	if exists(unknown) || exists(remote) || exists(repro) {
		t.Fatal("old leftovers survived")
	}
	if !exists(unknownAlt) {
		t.Fatal("an item with a session running here was touched")
	}
	if !exists(notOurs) {
		t.Fatal("a directory that is not an item's was removed")
	}
	if !exists(liveDir) {
		t.Fatal("the worktree of an item in progress was removed")
	}
	// git forgot the removed worktrees: their branches are free again.
	if out := testkit.RunGit(t, e.root, "worktree", "list"); strings.Contains(out, "I-900") || strings.Contains(out, closedDir) {
		t.Fatalf("git still lists removed worktrees:\n%s", out)
	}
}

// Closing an item removes what it left on this machine at once.
func TestCloseItemRemovesItsWorktrees(t *testing.T) {
	e := newEnv(t, withGit())
	it := e.ready("simple", "given up", "x")
	e.start(it.ID)
	dir := e.workspace(it.ID, map[string]string{"a.txt": "a\n"})
	alt, refine := e.scratch(it.ID+"~b"), e.scratch("_refine-"+it.ID)
	other := e.ready("simple", "kept", "x")
	e.start(other.ID)
	otherDir := e.workspace(other.ID, nil)
	if _, err := e.svc.CloseItem(e.ctx, "demo", owner(), it.ID, "abandoned", "", ""); err != nil {
		t.Fatal(err)
	}
	if exists(dir) || exists(alt) || exists(refine) {
		t.Fatalf("worktree %v, second attempt %v, refine %v still there", exists(dir), exists(alt), exists(refine))
	}
	if !exists(otherDir) {
		t.Fatal("another item's worktree was removed")
	}
	// The same item can be worked on again in the same place (a branch is not held by a ghost).
	if out := testkit.RunGit(t, e.root, "worktree", "list"); strings.Contains(out, dir) {
		t.Fatalf("git still lists the closed item's worktree:\n%s", out)
	}
}

// worktree.seed: the directories of the project root arrive in a new
// worktree before its setup runs, with their times, and never over what is
// already there.
func TestSeedWorktreeBeforeSetup(t *testing.T) {
	var copies []string
	e := newEnv(t, withGit(), withExec(), withCfg(func(c *domain.ProjectConfig) {
		c.WorktreeSetup = "pnpm install"
		c.WorktreeSeed = []string{"web/node_modules", "missing", "tracked"}
	}))
	e.svc.d.CopyTree = func(ctx context.Context, src, dst string) error {
		copies = append(copies, filepath.Base(filepath.Dir(src))+"/"+filepath.Base(src))
		return copyTree(ctx, src, dst)
	}
	testkit.WriteFile(t, e.root, "web/node_modules/left-pad/index.js", "module.exports = 1\n")
	if err := os.Symlink("left-pad", filepath.Join(e.root, "web/node_modules/alias")); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(e.root, "web/node_modules/left-pad/index.js"), old, old); err != nil {
		t.Fatal(err)
	}
	it := e.ready("simple", "needs its dependencies", "x")
	e.start(it.ID)
	dir := e.workspace(it.ID, nil)

	seeded := filepath.Join(dir, "web/node_modules/left-pad/index.js")
	fi, err := os.Stat(seeded)
	if err != nil {
		t.Fatalf("the seed did not arrive: %v", err)
	}
	if !fi.ModTime().Equal(old) {
		t.Fatalf("the copy lost the file's time: %s", fi.ModTime())
	}
	if target, err := os.Readlink(filepath.Join(dir, "web/node_modules/alias")); err != nil || target != "left-pad" {
		t.Fatalf("a link must stay a link: %q %v", target, err)
	}
	if len(copies) != 1 {
		t.Fatalf("copied %v, want only the directory that exists in the root", copies)
	}
	// The setup still runs, after the seed, in the same worktree.
	ran := 0
	for _, c := range e.exec.Cmds() {
		if c.Dir == dir && len(c.Args) == 2 && strings.Contains(c.Args[1], "pnpm install") {
			ran++
		}
	}
	if ran != 1 {
		t.Fatalf("setup ran %d times after the seed", ran)
	}
	// Nothing seeded is offered to git when the project ignores it, and a
	// second use neither copies nor installs again.
	e.workspace(it.ID, nil)
	if len(copies) != 1 {
		t.Fatalf("seeded again: %v", copies)
	}
}

// A seed that fails leaves no half copy and never fails the worktree.
func TestSeedFailureLeavesTheSetupToInstall(t *testing.T) {
	e := newEnv(t, withGit(), withExec(), withCfg(func(c *domain.ProjectConfig) {
		c.WorktreeSetup = "pnpm install"
		c.WorktreeSeed = []string{"node_modules"}
	}))
	e.svc.d.CopyTree = func(_ context.Context, _, dst string) error {
		testkit.WriteFile(t, dst, "half/copied.js", "x\n")
		return errors.New("no space left on device")
	}
	testkit.WriteFile(t, e.root, "node_modules/a/index.js", "a\n")
	it := e.ready("simple", "needs its dependencies", "x")
	e.start(it.ID)
	dir := e.workspace(it.ID, nil)
	if exists(filepath.Join(dir, "node_modules")) {
		t.Fatal("half a copy stayed in the worktree")
	}
	if !exists(filepath.Join(dir, filepath.FromSlash(setupMarker))) {
		t.Fatal("the setup did not run after the failed seed")
	}
}

// Seeding alone (no setup command) also marks the worktree ready.
func TestSeedWithoutSetupCommand(t *testing.T) {
	e := newEnv(t, withGit(), withCfg(func(c *domain.ProjectConfig) { c.WorktreeSeed = []string{"vendor"} }))
	testkit.WriteFile(t, e.root, "vendor/lib/a.go", "package lib\n")
	it := e.ready("simple", "needs its dependencies", "x")
	e.start(it.ID)
	dir := e.workspace(it.ID, nil)
	if !exists(filepath.Join(dir, "vendor/lib/a.go")) || !exists(filepath.Join(dir, filepath.FromSlash(setupMarker))) {
		t.Fatal("seed without a setup command did not prepare the worktree")
	}
}

// worktree.stores: one directory per store for the whole project, on the
// data directory, handed to the setup, the checks and the agent by the
// environment variable the project named, and mounted in the container.
func TestSharedStores(t *testing.T) {
	e := newEnv(t, withGit(), withExec(), withCfg(func(c *domain.ProjectConfig) {
		c.WorktreeSetup = "pnpm install"
		c.WorktreeStores = map[string]string{"pnpm": "npm_config_store_dir", "gradle": "GRADLE_USER_HOME"}
	}))
	env := e.svc.StoreEnv(e.p)
	pnpm := filepath.Join(e.svc.d.DataDir, "stores", "demo", "pnpm")
	if env["npm_config_store_dir"] != pnpm || env["GRADLE_USER_HOME"] != filepath.Join(e.svc.d.DataDir, "stores", "demo", "gradle") {
		t.Fatalf("store environment: %v", env)
	}
	if fi, err := os.Stat(pnpm); err != nil || !fi.IsDir() {
		t.Fatalf("the store directory was not created: %v", err)
	}
	a, b := e.ready("simple", "one", "x"), e.ready("simple", "two", "x")
	e.start(a.ID)
	e.start(b.ID)
	dirs := map[string]bool{e.workspace(a.ID, nil): true, e.workspace(b.ID, nil): true}
	setups := 0
	for _, c := range e.exec.Cmds() {
		if !dirs[c.Dir] || len(c.Args) != 2 || !strings.Contains(c.Args[1], "pnpm install") {
			continue
		}
		setups++
		var store, path bool
		for _, kv := range c.Env {
			store = store || kv == "npm_config_store_dir="+pnpm
			path = path || strings.HasPrefix(kv, "PATH=")
		}
		if !store || !path {
			t.Fatalf("the setup must get the store and keep the rest of the environment (store %v, PATH %v)", store, path)
		}
	}
	if setups != 2 {
		t.Fatalf("%d setups, want one per worktree, both with the same store", setups)
	}
	// The container sees the stores at the same path as the host.
	acc := &domain.Account{ID: "a", ConfigDir: t.TempDir()}
	cs, err := e.svc.Runtime().containerSpec(e.p, domain.ContainerDef{Image: "img"}, t.TempDir(), t.TempDir(), acc, "fake", map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if !domain.Contains(cs.Mounts, pnpm) {
		t.Fatalf("the store is not mounted in the agent's container: %v", cs.Mounts)
	}
	// A project without stores changes nothing.
	plain := newEnv(t, withGit())
	if got := plain.svc.StoreEnv(plain.p); got != nil {
		t.Fatalf("no stores configured, got %v", got)
	}
}

// worktree.strip: the heavy directories of an item that waits leave its
// worktree after worktree.strip_after; the worktree and its work stay, and
// the setup runs again when the item moves.
func TestStripWaitingWorktrees(t *testing.T) {
	e := newEnv(t, withGit(), withExec(), withCfg(func(c *domain.ProjectConfig) {
		c.WorktreeSetup = "pnpm install"
		c.WorktreeStrip = []string{"node_modules", "app/build", "never-there"}
		c.WorktreeStripAfter = time.Hour
	}))
	heavy := map[string]string{"node_modules/a/index.js": "a\n", "app/build/out.bin": "b\n", "src/work.txt": "unsaved work\n"}
	mk := func(title string) (string, string) {
		it := e.ready("simple", title, "x")
		e.start(it.ID)
		return it.ID, e.workspace(it.ID, heavy)
	}
	waitID, waitDir := mk("waits for the owner")
	blockID, blockDir := mk("blocked by another item")
	busyID, busyDir := mk("waits but has a session")
	_, liveDir := mk("in progress")
	e.setStatus(waitID, domain.StatusWaitingOwner)
	e.setStatus(blockID, domain.StatusBlocked)
	e.setStatus(busyID, domain.StatusWaitingOwner)
	busy := map[string]bool{busyID: true}

	if got := e.svc.trimWorkspaces(e.ctx, e.p, busy); got.total() != 0 {
		t.Fatalf("stripped before strip_after: %+v", got)
	}
	e.clock.Advance(90 * time.Minute)
	got := e.svc.trimWorkspaces(e.ctx, e.p, busy)
	if got.Stripped != 2 || got.total() != 2 {
		t.Fatalf("trimmed %+v, want the two waiting worktrees stripped", got)
	}
	for _, dir := range []string{waitDir, blockDir} {
		if exists(filepath.Join(dir, "node_modules")) || exists(filepath.Join(dir, "app/build")) {
			t.Fatalf("%s kept its heavy directories", dir)
		}
		if b, err := os.ReadFile(filepath.Join(dir, "src/work.txt")); err != nil || string(b) != "unsaved work\n" {
			t.Fatalf("%s lost the item's work: %v", dir, err)
		}
		if exists(filepath.Join(dir, filepath.FromSlash(setupMarker))) {
			t.Fatalf("%s still says its setup is done", dir)
		}
		if out := testkit.RunGit(t, dir, "status", "--porcelain"); strings.Contains(out, ports.TrashPrefix) {
			t.Fatalf("the removal left something for git to see: %q", out)
		}
	}
	for _, dir := range []string{busyDir, liveDir} {
		if !exists(filepath.Join(dir, "node_modules/a/index.js")) {
			t.Fatalf("%s was stripped", dir)
		}
	}
	// Nothing left to strip: a second sweep does nothing.
	e.noTrash()
	if got := e.svc.trimWorkspaces(e.ctx, e.p, busy); got.total() != 0 {
		t.Fatalf("second sweep trimmed %+v", got)
	}
	// The item moves: the next use of the worktree sets it up again.
	before := len(e.exec.Cmds())
	e.setStatus(waitID, domain.StatusInProgress)
	e.workspace(waitID, nil)
	again := 0
	for _, c := range e.exec.Cmds()[before:] {
		if c.Dir == waitDir && len(c.Args) == 2 && strings.Contains(c.Args[1], "pnpm install") {
			again++
		}
	}
	if again != 1 {
		t.Fatalf("setup ran %d times after the strip, want once", again)
	}
}

// An item that moved between the sweep's look and the strip keeps its directories.
func TestStripLooksAgainUnderTheLock(t *testing.T) {
	e := newEnv(t, withGit(), withCfg(func(c *domain.ProjectConfig) {
		c.WorktreeStrip = []string{"node_modules"}
		c.WorktreeStripAfter = time.Hour
	}))
	it := e.ready("simple", "answered just now", "x")
	e.start(it.ID)
	dir := e.workspace(it.ID, map[string]string{"node_modules/a/index.js": "a\n"})
	e.setStatus(it.ID, domain.StatusWaitingOwner)
	e.clock.Advance(2 * time.Hour)
	e.setStatus(it.ID, domain.StatusInProgress)
	if e.svc.stripWorktree(e.p, it.ID, dir) {
		t.Fatal("stripped the worktree of an item that is moving again")
	}
	if !exists(filepath.Join(dir, "node_modules/a/index.js")) {
		t.Fatal("heavy directory removed")
	}
}
