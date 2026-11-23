package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// QueueEntry is an item waiting in the merge queue.
type QueueEntry struct {
	Item  *domain.Item `json:"item"`
	Since string       `json:"since"`
	Here  bool         `json:"here"` // held by this machine
}

// Queue lists the merge queue.
func (s *Service) Queue(project string, a Actor) ([]QueueEntry, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeItemsRead, p.Name); err != nil {
		return nil, err
	}
	var out []QueueEntry
	for _, it := range p.Snapshot().ItemList() {
		if it.Status == domain.StatusQueued {
			out = append(out, QueueEntry{Item: it, Since: it.StepStartedAt.Format("2006-01-02 15:04"), Here: it.Claim != nil && it.Claim.Machine == s.d.Machine})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Item.StepStartedAt.Before(out[j].Item.StepStartedAt) })
	return out, nil
}

// ProcessQueue integrates queued items held by this machine: rebase on the
// current main, check the combined result again, push main only by
// fast-forward, in batches with bisection when a batch fails.
func (s *Service) ProcessQueue(ctx context.Context, project string) (int, error) {
	p, err := s.Project(project)
	if err != nil {
		return 0, err
	}
	if p.Ports().Git == nil || p.Ports().Arbiter == nil {
		return 0, nil
	}
	if !p.queueMu.TryLock() {
		return 0, nil
	}
	defer p.queueMu.Unlock()
	snap := p.Snapshot()
	if snap.Paused {
		return 0, nil
	}
	var queued []*domain.Item
	for _, it := range snap.Items {
		if it.Status == domain.StatusQueued && it.Claim != nil && it.Claim.Machine == s.d.Machine {
			queued = append(queued, it)
		}
	}
	if len(queued) == 0 {
		return 0, nil
	}
	sort.SliceStable(queued, func(i, j int) bool {
		if queued[i].Priority != queued[j].Priority {
			return queued[i].Priority < queued[j].Priority
		}
		return queued[i].StepStartedAt.Before(queued[j].StepStartedAt)
	})
	batch := p.Cfg().Merge.Batch
	if batch <= 0 {
		batch = 1
	}
	if len(queued) > batch {
		queued = queued[:batch]
	}
	return s.integrateBatch(ctx, p, queued)
}

// integrateBatch tries the whole batch; on failure it splits it in halves
// until the culprit is found.
func (s *Service) integrateBatch(ctx context.Context, p *Project, items []*domain.Item) (int, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		ok, failed, reason, err := s.tryIntegrate(ctx, p, items)
		if errors.Is(err, ports.ErrStaleMain) {
			lastErr = err
			continue // another machine integrated first: rebase and check again
		}
		if err != nil {
			return 0, err
		}
		if ok {
			return len(items), nil
		}
		if len(items) == 1 || failed != nil {
			culprit := items[0]
			if failed != nil {
				culprit = failed
			}
			s.queueFailure(ctx, p, culprit, reason)
			rest := without(items, culprit.ID)
			if len(rest) == 0 {
				return 0, nil
			}
			return s.integrateBatch(ctx, p, rest)
		}
		half := len(items) / 2
		n1, err := s.integrateBatch(ctx, p, items[:half])
		if err != nil {
			return n1, err
		}
		n2, err := s.integrateBatch(ctx, p, refreshItems(p, items[half:]))
		return n1 + n2, err
	}
	return 0, lastErr
}

func without(items []*domain.Item, id string) []*domain.Item {
	var out []*domain.Item
	for _, it := range items {
		if it.ID != id {
			out = append(out, it)
		}
	}
	return out
}

func refreshItems(p *Project, items []*domain.Item) []*domain.Item {
	snap := p.Snapshot()
	var out []*domain.Item
	for _, it := range items {
		if cur, ok := snap.Items[it.ID]; ok && cur.Status == domain.StatusQueued {
			out = append(out, cur)
		}
	}
	return out
}

// tryIntegrate rebases each item on top of the previous one and checks the
// combined tree. failed names an item that could not even be rebased.
func (s *Service) tryIntegrate(ctx context.Context, p *Project, items []*domain.Item) (ok bool, failed *domain.Item, reason string, err error) {
	g := p.Ports().Git
	if err := g.Fetch(ctx); err != nil {
		s.d.Log.Warn("fetch before integration", "err", err)
	}
	main, err := g.MainHead(ctx)
	if err != nil {
		return false, nil, "", err
	}
	head := main
	var lastDir string
	type built struct {
		it  *domain.Item
		sha string
	}
	var done []built
	// A batch that does not land must leave each worktree as it was: otherwise
	// an item rebased on top of a rejected one carries its changes into the
	// next attempt (bisection would then blame the innocent items too).
	type origin struct{ dir, branch, head string }
	var origins []origin
	defer func() {
		if ok {
			return
		}
		for _, o := range origins {
			if o.branch != "" {
				_ = g.ResetBranch(ctx, o.dir, o.branch, o.head)
			} else {
				_ = g.Checkout(ctx, o.dir, o.head)
			}
		}
	}()
	for _, it := range items {
		dir, err := s.ensureWorkspace(ctx, p, it.ID)
		if err != nil {
			return false, it, "workspace: " + err.Error(), nil
		}
		if _, err := s.commitStep(ctx, p, it.ID, it.ID+": pending changes before integration"); err != nil && strings.Contains(err.Error(), "secret") {
			return false, it, err.Error(), nil
		}
		if h, err := g.Head(ctx, dir); err == nil {
			origins = append(origins, origin{dir: dir, branch: it.Branch, head: h})
		}
		if err := g.Rebase(ctx, dir, head); err != nil {
			if errors.Is(err, ports.ErrConflict) {
				return false, it, "rebase on main conflicts: " + err.Error(), nil
			}
			return false, it, "rebase failed: " + err.Error(), nil
		}
		var sha string
		if p.Cfg().Merge.Mode == "steps" {
			sha, err = g.Head(ctx, dir)
		} else {
			sha, err = g.SquashOnto(ctx, dir, head, squashMessage(it), ItemFiles...)
		}
		if err != nil {
			return false, it, "commit: " + err.Error(), nil
		}
		if p.Cfg().Merge.Mode != "steps" {
			// Point the item worktree at its squash commit so the branch head
			// lands in main and the PR shows as merged.
			if err := g.Checkout(ctx, dir, sha); err != nil {
				return false, it, "checkout: " + err.Error(), nil
			}
		}
		head = sha
		lastDir = dir
		done = append(done, built{it, sha})
	}
	if p.Cfg().Merge.Changelog != "" && lastDir != "" {
		if sha, err := s.appendChangelog(ctx, p, lastDir, items); err == nil && sha != "" {
			head = sha
		}
	}
	// Check the combined result: every local check over the whole tree.
	if pass, why := s.runQueueChecks(ctx, p, lastDir, head, items); !pass {
		if len(items) == 1 {
			return false, items[0], why, nil
		}
		return false, nil, why, nil
	}
	for _, b := range done {
		branch := b.it.Branch
		if branch != "" {
			// The branch moves to the integrated commit (force: it was rebased).
			if err := g.PushRev(ctx, b.sha, branch); err != nil {
				s.d.Log.Debug("push branch to integrated commit", "item", b.it.ID, "err", err)
			}
		}
	}
	if err := p.Ports().Arbiter.Integrate(ctx, main, head); err != nil {
		return false, nil, "", err
	}
	changed, _ := g.ChangedFiles(ctx, main, head)
	for _, b := range done {
		s.markIntegrated(ctx, p, b.it, b.sha)
	}
	s.revalidateAsync(p, domain.Change{Kind: "integration", Cause: "main " + short(head), Commit: head, Files: changed, Summary: integrationSummary(items)})
	return true, nil, "", nil
}

func integrationSummary(items []*domain.Item) string {
	var parts []string
	for _, it := range items {
		parts = append(parts, it.ID+" "+it.Title)
	}
	return "integrated: " + strings.Join(parts, "; ")
}

// ItemFiles live in the item's branch (its memory) but never land on main.
var ItemFiles = []string{"plan.md", "criteria.json"}

func squashMessage(it *domain.Item) string {
	return fmt.Sprintf("%s: %s\n\n%s", it.ID, it.Title, domain.CommitTrailer(it.ID))
}

func (s *Service) appendChangelog(ctx context.Context, p *Project, dir string, items []*domain.Item) (string, error) {
	path := filepath.Join(dir, p.Cfg().Merge.Changelog)
	cur, _ := os.ReadFile(path)
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "- %s %s\n", it.ID, it.Title)
	}
	content := b.String() + string(cur)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	sha, _, err := p.Ports().Git.CommitAll(ctx, dir, "changelog: "+integrationSummary(items))
	return sha, err
}

// runQueueChecks runs the local checks over the combined tree.
func (s *Service) runQueueChecks(ctx context.Context, p *Project, dir, head string, items []*domain.Item) (bool, string) {
	if s.d.Checks == nil || dir == "" {
		return true, ""
	}
	var touched []string
	for _, it := range items {
		touched = append(touched, it.FilesTouched...)
	}
	for _, name := range sortedKeys(p.Cfg().Checks) {
		def := p.Cfg().Checks[name]
		if def.RunsOn != "local" {
			continue // remote checks already ran in the item flow
		}
		def.Scope = "all"
		res, pending, err := s.d.Checks.Run(ctx, ports.CheckSpec{Def: def, ItemID: items[0].ID, Commit: head, Dir: dir, Touched: touched})
		if err != nil {
			return false, fmt.Sprintf("check %s could not run: %v", name, err)
		}
		if pending {
			continue
		}
		if !res.Passed {
			return false, fmt.Sprintf("check %s failed on the combined result: %s", name, res.Summary)
		}
	}
	return true, ""
}

// queueFailure sends an item back from the merge queue as rework.
func (s *Service) queueFailure(ctx context.Context, p *Project, it *domain.Item, reason string) {
	p.mu.Lock()
	cur := p.state.Items[it.ID]
	p.mu.Unlock()
	if cur == nil || cur.Status != domain.StatusQueued {
		return
	}
	if _, _, err := s.finishStep(ctx, p, stepOutcome{Item: it.ID, Step: cur.Step, Outcome: domain.OutcomeFail, Reason: "merge queue: " + reason, Actor: "merge-queue"}); err != nil {
		s.d.Log.Warn("queue failure", "item", it.ID, "err", err)
	}
}

// markIntegrated closes the item's flow after main moved.
func (s *Service) markIntegrated(ctx context.Context, p *Project, it *domain.Item, sha string) {
	p.mu.Lock()
	cur := p.state.Items[it.ID]
	p.mu.Unlock()
	if cur == nil {
		return
	}
	// The merge commit is recorded before the item becomes integrated, so
	// whoever sees it integrated also sees where it landed.
	if _, err := s.mutateItem(ctx, p, it.ID, domain.EvItemUpdated, "merge-queue", func(x *domain.Item) error {
		x.MergeCommit = sha
		return nil
	}, domain.Payload{Note: "merged as " + sha}); err != nil {
		s.d.Log.Warn("record merge commit", "item", it.ID, "err", err)
	}
	upd, _, err := s.finishStep(ctx, p, stepOutcome{Item: it.ID, Step: cur.Step, Outcome: domain.OutcomePass, Actor: "merge-queue",
		Payload: domain.Payload{Note: "integrated as " + sha}})
	if err != nil {
		s.d.Log.Warn("mark integrated", "item", it.ID, "err", err)
		return
	}
	_, _ = s.emit(ctx, p, domain.EvItemIntegrated, it.ID, "merge-queue", domain.Payload{Item: upd, Note: "integrated into main"})
	if p.Ports().Git != nil {
		_ = p.Ports().Git.RemoveWorktree(ctx, s.WorktreeDir(p.Name, it.ID))
	}
	if p.Ports().CodeHost != nil && upd != nil && upd.PR != "" {
		go func() {
			_ = p.Ports().CodeHost.Comment(context.Background(), prFromRef(upd.PR), fmt.Sprintf("Integrated into %s as %s by the Barnraise merge queue.", p.Cfg().MainBranch, sha))
		}()
	}
	s.unblockDependents(ctx, p)
	s.syncTracker(p, it.ID, false)
}
