package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// How long a directory is left alone before the sweep removes it. A person
// may still be looking at a finished item's worktree; an item this machine
// does not know may only be late in the event log; a scratch checkout (a
// check for another machine, a reproduction) never lives a day.
var (
	finishedKeep = time.Hour
	unknownKeep  = 24 * time.Hour
	scratchKeep  = 24 * time.Hour
)

// worktreeLock serializes the setup of a worktree with the stripping of its
// heavy directories: one never sees the other half done.
func (s *Service) worktreeLock(dir string) *sync.Mutex {
	m, _ := s.wtLocks.LoadOrStore(dir, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// storeDir is where a shared store of a project lives. It is in the data
// directory, on the same filesystem as the worktrees: a package manager can
// hard-link from it into each worktree instead of copying.
func (s *Service) storeDir(project, name string) string {
	return filepath.Join(s.d.DataDir, "stores", project, name)
}

// StoreEnv returns the environment that points tools at the project's shared
// stores (worktree.stores), creating the directories. Paths are of this
// machine: an environment received from another machine is replaced by it.
func (s *Service) StoreEnv(p *Project) map[string]string {
	stores := p.Cfg().WorktreeStores
	if len(stores) == 0 {
		return nil
	}
	env := make(map[string]string, len(stores))
	for name, key := range stores {
		dir := s.storeDir(p.Name, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			s.d.Log.Warn("shared store", "project", p.Name, "store", name, "err", err)
			continue
		}
		env[key] = dir
	}
	return env
}

// storeDirs lists the project's store directories, sorted.
func (s *Service) storeDirs(p *Project) []string {
	var dirs []string
	for _, dir := range s.StoreEnv(p) {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	return dirs
}

// copyTree copies the directory src to dst, which must not exist. Where the
// filesystem can share blocks (btrfs, XFS with reflink, APFS) the copy takes
// no space and almost no time; elsewhere it is a plain copy. Times are kept:
// build tools decide what is stale by them.
func copyTree(ctx context.Context, src, dst string) error {
	var args []string
	switch runtime.GOOS {
	case "linux":
		args = []string{"-a", "--reflink=auto", src, dst}
	case "darwin":
		args = []string{"-Rpc", src, dst}
	default:
		args = []string{"-Rp", src, dst}
	}
	if out, err := exec.CommandContext(ctx, "cp", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("cp %s: %w: %s", strings.Join(args, " "), err, truncate(string(out), 400))
	}
	return nil
}

// seedWorktree copies the project's worktree.seed directories from the
// project root into dir, where they are missing. The setup command that
// follows then finds its dependencies in place and only checks them. A
// directory that could not be copied is left to the setup: seeding saves
// time, it is never a reason for a worktree to fail.
func (s *Service) seedWorktree(ctx context.Context, p *Project, dir string) {
	root := p.Cfg().Root
	if samePath(root, dir) {
		return
	}
	for _, rel := range p.Cfg().WorktreeSeed {
		src, dst := filepath.Join(root, filepath.FromSlash(rel)), filepath.Join(dir, filepath.FromSlash(rel))
		if st, err := os.Stat(src); err != nil || !st.IsDir() {
			continue
		}
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			continue
		}
		started := time.Now()
		if err := s.d.CopyTree(ctx, src, dst); err != nil {
			_ = ports.RemoveTree(dst) // half a copy would pass for the whole
			s.d.Log.Warn("seed the worktree; the setup installs from scratch", "dir", dir, "path", rel, "err", err)
			continue
		}
		s.d.Log.Info("worktree seeded", "dir", dir, "path", rel, "took", time.Since(started).Round(time.Millisecond))
	}
}

func samePath(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// dropItemWorkspaces removes what an item left on this machine: its
// worktree, the second attempt's and the analyst's checkout.
func (s *Service) dropItemWorkspaces(ctx context.Context, p *Project, id string) {
	g := p.Ports().Git
	if g == nil {
		return
	}
	for _, dir := range []string{s.WorktreeDir(p.Name, id), s.altDir(p.Name, id), s.WorktreeDir(p.Name, "_refine-"+id)} {
		if _, err := os.Lstat(dir); err != nil {
			continue
		}
		if err := g.RemoveWorktree(ctx, dir); err != nil {
			s.d.Log.Warn("remove the worktree of a finished item", "item", id, "err", err)
		}
	}
}

// Trimmed counts what one sweep of a project's directories removed.
type Trimmed struct {
	Finished int // worktrees of integrated or closed items
	Unknown  int // worktrees of items this machine does not know
	Refine   int // analyst checkouts of items that left draft
	Scratch  int // leftover scratch checkouts (checks, reproductions)
	Trash    int // directories renamed for removal and never deleted
	Stripped int // worktrees of waiting items that lost their heavy directories
}

func (t Trimmed) total() int {
	return t.Finished + t.Unknown + t.Refine + t.Scratch + t.Trash + t.Stripped
}

// trimWorkspaces reconciles the project's directories on this machine with
// what the items say: it removes the worktrees nothing will use again and
// empties the heavy directories of the ones that wait. busy lists the items
// with a session running here; nothing of theirs is touched.
func (s *Service) trimWorkspaces(ctx context.Context, p *Project, busy map[string]bool) Trimmed {
	var out Trimmed
	g := p.Ports().Git
	if g == nil {
		return out
	}
	now := s.d.Clock.Now()
	st := p.Snapshot()
	old := func(dir string, keep time.Duration) bool {
		fi, err := os.Lstat(dir)
		return err == nil && now.Sub(fi.ModTime()) > keep
	}
	remove := func(dir string, n *int) {
		if err := g.RemoveWorktree(ctx, dir); err != nil {
			s.d.Log.Warn("remove a leftover worktree", "dir", dir, "err", err)
			return
		}
		*n++
	}
	entries, _ := os.ReadDir(s.WorktreeDir(p.Name, ""))
	for _, e := range entries {
		name := e.Name()
		dir := s.WorktreeDir(p.Name, name)
		switch {
		case !e.IsDir():
		case ports.IsTrash(name):
			if err := ports.RemoveTree(dir); err == nil {
				out.Trash++
			}
		case strings.HasPrefix(name, "_refine-"):
			id := strings.TrimPrefix(name, "_refine-")
			if it := st.Items[id]; busy[id] || (it != nil && it.Status == domain.StatusDraft) {
				continue
			}
			remove(dir, &out.Refine)
		case strings.HasPrefix(name, "_"):
			// _remote-*, _main-*: scratch checkouts, removed by who made them.
			if old(dir, scratchKeep) {
				remove(dir, &out.Scratch)
			}
		default:
			id := strings.TrimSuffix(name, "~b")
			if _, _, err := domain.ParseID(id); err != nil || busy[id] {
				continue // not ours to judge, or in use
			}
			it := st.Items[id]
			switch {
			case it == nil:
				if old(dir, unknownKeep) {
					remove(dir, &out.Unknown)
				}
			case it.Status.Terminal():
				if now.Sub(it.UpdatedAt) > finishedKeep {
					remove(dir, &out.Finished)
				}
			case it.Status == domain.StatusWaitingOwner || it.Status == domain.StatusBlocked:
				if s.stripWorktree(p, id, dir) {
					out.Stripped++
				}
			}
		}
	}
	repro := filepath.Join(s.d.DataDir, "repro", p.Name)
	entries, _ = os.ReadDir(repro)
	for _, e := range entries {
		dir := filepath.Join(repro, e.Name())
		switch {
		case !e.IsDir():
		case ports.IsTrash(e.Name()):
			if err := ports.RemoveTree(dir); err == nil {
				out.Trash++
			}
		case old(dir, scratchKeep):
			remove(dir, &out.Scratch)
		}
	}
	return out
}

// stripWorktree removes the worktree.strip directories from the worktree of
// an item that has waited for worktree.strip_after, and forgets that the
// setup ran there: the next use of the worktree seeds and sets it up again.
// It reports whether anything was removed.
func (s *Service) stripWorktree(p *Project, id, dir string) bool {
	paths := p.Cfg().WorktreeStrip
	if len(paths) == 0 {
		return false
	}
	lock := s.worktreeLock(dir)
	lock.Lock()
	defer lock.Unlock()
	// Read again under the lock: the item may have moved since the sweep looked.
	p.mu.Lock()
	it := p.state.Items[id]
	waiting := it != nil && (it.Status == domain.StatusWaitingOwner || it.Status == domain.StatusBlocked) &&
		s.d.Clock.Now().Sub(it.UpdatedAt) > p.Cfg().WorktreeStripAfter
	p.mu.Unlock()
	if !waiting {
		return false
	}
	stripped := false
	for _, rel := range paths {
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if fi, err := os.Lstat(target); err != nil || !fi.IsDir() {
			continue
		}
		if !stripped {
			// First, so a strip cut in the middle is still followed by a setup.
			if err := os.Remove(filepath.Join(dir, filepath.FromSlash(setupMarker))); err != nil && !os.IsNotExist(err) {
				return false
			}
		}
		// Out of the worktree, so a leftover is never offered to git and the
		// sweep of the project's worktrees finds it.
		trash := s.WorktreeDir(p.Name, fmt.Sprintf("%s%s-%d", ports.TrashPrefix, id, time.Now().UnixNano()))
		if err := os.Rename(target, trash); err != nil {
			s.d.Log.Warn("strip a waiting worktree", "item", id, "path", rel, "err", err)
			continue
		}
		stripped = true
		go func() { _ = ports.RemoveTree(trash) }()
	}
	if stripped {
		s.d.Log.Info("heavy directories of a waiting item removed; its setup runs again when it moves", "project", p.Name, "item", id)
	}
	return stripped
}
