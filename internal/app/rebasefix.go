package app

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// An agent cannot rebase its own branch: its sandbox keeps .barnraise/
// read only, and main changes .barnraise/map.md at every integration, so
// git stops halfway and leaves the worktree broken. When the merge queue
// sends an item back for a rebase conflict, Barnraise rebases the branch
// itself before the session, outside the sandbox, and leaves it stopped at
// the conflicts the agent must resolve. When the session ends Barnraise
// finishes the rebase, or undoes it: a step's commit never records a
// rebase left halfway.

// rebaseSides resolves Barnraise's own files to main and keeps the item's
// memory from the branch: neither is the agent's to resolve.
func rebaseSides() ports.RebaseSides {
	return ports.RebaseSides{Onto: []string{".barnraise/"}, Own: ItemFiles}
}

// conflictRebaser is the Git that can leave a rebase stopped, or nil.
func conflictRebaser(p *Project) ports.ConflictRebaser {
	cr, _ := p.Ports().Git.(ports.ConflictRebaser)
	return cr
}

// sentBackForConflict reports whether the item's last rework round came
// from the merge queue failing to rebase it on main, and sent it back to step.
func sentBackForConflict(it *domain.Item, step string) bool {
	if it == nil || len(it.Rework) == 0 {
		return false
	}
	last := it.Rework[len(it.Rework)-1]
	return strings.Contains(last.Reason, "rebase on main conflicts") && (last.To == "" || last.To == step)
}

// prepareRebase puts the item's branch on main before a session when the
// merge queue sent it back for a conflict, and returns the files the
// agent has to resolve (none: nothing is left to resolve).
func (s *Service) prepareRebase(ctx context.Context, p *Project, it *domain.Item, step, dir string) []string {
	cr := conflictRebaser(p)
	if cr == nil || it == nil || dir == "" {
		return nil
	}
	if cr.Unfinished(ctx, dir) {
		// An earlier session left the rebase stopped: what it resolved is
		// staged, the rest is this session's.
		left, err := cr.ContinueRebase(ctx, dir, rebaseSides())
		if err != nil {
			s.d.Log.Warn("continue a stopped rebase", "item", it.ID, "err", err)
			_ = cr.AbortUnfinished(ctx, dir)
			return nil
		}
		if len(left) == 0 {
			s.pushRebased(ctx, p, it, dir)
		}
		return left
	}
	if !sentBackForConflict(it, step) {
		return nil
	}
	g := p.Ports().Git
	if err := g.Fetch(ctx); err != nil {
		s.d.Log.Debug("fetch before a rebase for the agent", "err", err)
	}
	main, err := g.MainHead(ctx)
	if err != nil || main == "" {
		return nil
	}
	head, err := g.Head(ctx, dir)
	if err != nil || head == "" {
		return nil
	}
	if base, err := g.MergeBase(ctx, head, main); err == nil && base == main {
		return nil // already on main
	}
	// What the last session left uncommitted rides on the branch first.
	if _, err := s.commitStep(ctx, p, it.ID, "chore("+it.ID+"): pending changes before a rebase on main"); err != nil {
		s.d.Log.Warn("commit before a rebase for the agent", "item", it.ID, "err", err)
		return nil
	}
	left, err := cr.StartRebase(ctx, dir, main, rebaseSides())
	if err != nil {
		s.d.Log.Warn("rebase for the agent", "item", it.ID, "err", err)
		_ = cr.AbortUnfinished(ctx, dir)
		return nil
	}
	note := "Barnraise rebased the branch on main " + short(main)
	if len(left) > 0 {
		note += "; it stopped at conflicts in " + strings.Join(left, ", ")
	} else {
		s.pushRebased(ctx, p, it, dir)
	}
	_, _ = s.emit(ctx, p, domain.EvItemUpdated, it.ID, "barnraise", domain.Payload{Note: note, Extra: map[string]any{"rebase_on": main, "conflicts": left}})
	return left
}

func (s *Service) pushRebased(ctx context.Context, p *Project, it *domain.Item, dir string) {
	if it.Branch == "" {
		return
	}
	if err := p.Ports().Git.Push(ctx, dir, it.Branch); err != nil {
		s.d.Log.Debug("push after a rebase for the agent", "item", it.ID, "err", err)
	}
}

// rebaseBrief tells the agent what Barnraise did with its branch.
func rebaseBrief(conflicts []string) string {
	if len(conflicts) == 0 {
		return ""
	}
	return "\n\nBarnraise has already rebased this branch on main for you, and the rebase is stopped at conflicts in: " +
		strings.Join(conflicts, ", ") + ".\n" +
		"- Resolve each of those files by editing it: remove every conflict marker (<<<<<<<, =======, >>>>>>>) and keep main's change together with this item's change.\n" +
		"- Do not run git rebase, git reset, git checkout, git stash or git commit: Barnraise stages your resolution and finishes the rebase when your step ends. Files under .barnraise/ and the item's plan.md and criteria.json were resolved already.\n" +
		"- Then run the checks on the result and deliver as usual."
}

// settleUnfinished finishes, or undoes, a rebase, merge or cherry-pick
// left in dir before anything there is committed. live: a session still
// works in dir, so a rebase still waiting for it is left alone and false
// says not to commit now.
func (s *Service) settleUnfinished(ctx context.Context, p *Project, itemID, dir string, live bool) bool {
	cr := conflictRebaser(p)
	if cr == nil || !cr.Unfinished(ctx, dir) {
		return true
	}
	left, err := cr.ContinueRebase(ctx, dir, rebaseSides())
	if err == nil && len(left) == 0 && !cr.Unfinished(ctx, dir) {
		return true
	}
	if live {
		return false
	}
	why := "conflicts left in " + strings.Join(left, ", ")
	if err != nil {
		why = err.Error()
	}
	if aerr := cr.AbortUnfinished(ctx, dir); aerr != nil {
		s.d.Log.Warn("undo an unfinished rebase", "item", itemID, "err", aerr)
		return false
	}
	_, _ = s.emit(ctx, p, domain.EvItemUpdated, itemID, "barnraise", domain.Payload{Note: "an unfinished rebase was undone before the step's commit: " + truncate(why, 300)})
	return true
}

// sandboxMountNames are the paths the agent's sandbox protects by mounting
// an empty read-only file over them in the working folder. Left behind,
// they are not the item's work.
var sandboxMountNames = map[string]bool{
	".bashrc": true, ".bash_profile": true, ".bash_login": true, ".profile": true, ".zshrc": true, ".zprofile": true,
	".zshenv": true, ".zlogin": true, ".gitconfig": true, ".gitmodules": true, ".ripgreprc": true, ".mcp.json": true,
	".idea": true, ".vscode": true, ".claude": true, ".cc-writes": true,
}

// dropSandboxMounts removes from dir the untracked, empty, read-only files
// the sandbox leaves at protected paths (and its .claude/.cc-writes log),
// so `git add -A` never puts them on the branch.
func (s *Service) dropSandboxMounts(ctx context.Context, p *Project, dir string) {
	g := p.Ports().Git
	if g == nil || dir == "" || filepath.Clean(dir) == filepath.Clean(p.Cfg().Root) {
		return
	}
	head, err := g.Head(ctx, dir)
	if err != nil || head == "" {
		return
	}
	tracked := func(rel string) bool { return g.ExistsAt(ctx, head, rel) }
	drop := func(path, rel string, info fs.FileInfo) {
		if info.Mode().IsRegular() && info.Size() == 0 && info.Mode().Perm()&0o222 == 0 && !tracked(rel) {
			_ = os.Remove(path)
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if !sandboxMountNames[name] {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			drop(path, name, info)
			continue
		}
		if name != ".claude" || tracked(".claude") {
			continue
		}
		sub, _ := os.ReadDir(path)
		for _, c := range sub {
			cp := filepath.Join(path, c.Name())
			rel := ".claude/" + c.Name()
			if c.Name() == ".cc-writes" && c.IsDir() && !tracked(rel) {
				_ = os.RemoveAll(cp)
				continue
			}
			if ci, err := os.Lstat(cp); err == nil {
				drop(cp, rel, ci)
			}
		}
		_ = os.Remove(path) // only when nothing is left in it
	}
}

// mainRepairTitle names the one item that repairs a check red on main.
func mainRepairTitle(check string) string {
	return "Make check " + check + " pass on main again"
}

// isMainRepair reports whether it is the item that repairs check: its own
// check failing is its work not being done yet, not a reason to wait.
func isMainRepair(it *domain.Item, check string) bool {
	return it != nil && it.Title == mainRepairTitle(check)
}

// mainRepairItem returns the open item that repairs check on main, and
// creates it when there is none. Every item held by the same red check
// waits for that one item, instead of each agent repairing main on its own.
func (s *Service) mainRepairItem(ctx context.Context, p *Project, check, seenOn, summary string) string {
	title := mainRepairTitle(check)
	p.mu.Lock()
	for _, it := range p.state.ItemList() {
		if it.Title == title && it.Status != domain.StatusIntegrated && it.Status != domain.StatusClosed {
			p.mu.Unlock()
			return it.ID
		}
	}
	p.mu.Unlock()
	area := "general"
	if def, ok := p.Cfg().Checks[check]; ok && len(def.Areas) == 1 {
		area = def.Areas[0]
	}
	body := "Check " + check + " fails on main too: it failed on " + seenOn + " and on the main that item branched from, so the items behind it wait for this one.\n\n" +
		"Find why it fails on main, on this machine or on every machine (a test that depends on the locale, the user's data folder, a tool version, a missing build artifact), " +
		"and make it pass without weakening it: no test is removed, skipped or loosened. When the cause is the environment, make the check hermetic instead of changing the machine.\n\n" +
		"What the check printed:\n\n```\n" + truncate(summary, 3000) + "\n```"
	res, err := s.CreateItem(ctx, p.Name, System(), NewItem{Title: title, Body: body, Kind: "bug", Priority: "P0", Area: area, Size: "1h"})
	if err != nil || res.Item == nil {
		s.d.Log.Warn("create the item that repairs main", "check", check, "err", err)
		return ""
	}
	return res.Item.ID
}
