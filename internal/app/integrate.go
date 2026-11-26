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
	if on, _ := s.runtime.Standby(); snap.Paused || on {
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
	if s.hostChecksApply(ctx, p) {
		// The host checks one pull request, on top of main: one item at a time.
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
	s.queueState(p, items, "working", "rebasing on main")
	defer s.queueState(p, items, "done", "")
	for range 3 {
		ok, failed, reason, err := s.tryIntegrate(ctx, p, items)
		if errors.Is(err, errHostWait) {
			return 0, nil // the host's checks still run: the next pass asks again
		}
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
	viaPR := len(items) == 1 && items[0].PR != "" && items[0].Branch != "" && s.hostChecksApply(ctx, p)
	if viaPR {
		// A commit of this item may already be on its pull request, waiting
		// for the host's checks: nothing is built again while they run.
		if w := s.hostWaiting(ctx, p, items[0], main); w != nil {
			pass, why, err := s.hostVerdict(ctx, p, items[0], w)
			if err != nil || !pass {
				return false, items[0], why, err
			}
			return s.land(ctx, p, main, w.sha, items, []string{w.sha}, true)
		}
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
	var reapplied []string // files whose conflicts git resolved from a recorded resolution
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
		if _, err := s.commitStep(ctx, p, it.ID, "chore("+it.ID+"): pending changes before integration"); err != nil && strings.Contains(err.Error(), "secret") {
			return false, it, err.Error(), nil
		}
		if h, err := g.Head(ctx, dir); err == nil {
			origins = append(origins, origin{dir: dir, branch: it.Branch, head: h})
		}
		again, err := s.rebaseItem(ctx, p, it, dir, head)
		reapplied = append(reapplied, again...)
		if err != nil {
			if errors.Is(err, ports.ErrConflict) {
				return false, it, "rebase on main conflicts: " + err.Error(), nil
			}
			return false, it, "rebase failed: " + err.Error(), nil
		}
		// On top of main as it is now: the changelog line of this item.
		if err := s.writeChangelog(ctx, p, dir, it); err != nil {
			return false, it, "changelog: " + err.Error(), nil
		}
		s.writeRepoMap(ctx, p, dir, it)
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
	// Check the combined result: every local check over the whole tree.
	if pass, why := s.runQueueChecksWith(ctx, p, lastDir, head, items, len(reapplied) > 0); !pass {
		why += reappliedHint(reapplied)
		if len(items) == 1 {
			return false, items[0], why, nil
		}
		return false, nil, why, nil
	}
	shas := make([]string, len(done))
	for i, b := range done {
		shas[i] = b.sha
	}
	if viaPR {
		s.queueState(p, items, "working", "waiting for the pull request's checks")
		// The commit that would land goes to the pull request first: people
		// see the change there, and the repository's own CI runs on it.
		it := items[0]
		if err := g.PushRev(ctx, head, it.Branch); err != nil {
			return false, nil, "", fmt.Errorf("push %s to its pull request: %w", it.ID, err)
		}
		_ = p.Ports().CodeHost.MarkReady(ctx, prFromRef(it.PR))
		w := &hostWait{main: main, sha: head, since: s.d.Clock.Now()}
		if len(origins) > 0 {
			w.orig = origins[0].head
		}
		p.mu.Lock()
		if p.hostWait == nil {
			p.hostWait = map[string]*hostWait{}
		}
		p.hostWait[it.ID] = w
		p.mu.Unlock()
		pass, why, err := s.hostVerdict(ctx, p, it, w)
		if err != nil || !pass {
			return false, it, why, err
		}
		return s.land(ctx, p, main, head, items, shas, true)
	}
	for _, b := range done {
		if b.it.Branch != "" {
			// The branch moves to the integrated commit (force: it was rebased).
			if err := g.PushRev(ctx, b.sha, b.it.Branch); err != nil {
				s.d.Log.Debug("push branch to integrated commit", "item", b.it.ID, "err", err)
			}
		}
	}
	return s.land(ctx, p, main, head, items, shas, false)
}

// land moves main to head and closes the flow of the items, each at its
// own commit. With viaPR, a main the host does not let Barnraise push to
// (a protected branch) is moved by merging the item's pull request.
func (s *Service) land(ctx context.Context, p *Project, main, head string, items []*domain.Item, shas []string, viaPR bool) (bool, *domain.Item, string, error) {
	g := p.Ports().Git
	s.queueState(p, items, "working", "moving main")
	if err := p.Ports().Arbiter.Integrate(ctx, main, head); err != nil {
		if !viaPR || errors.Is(err, ports.ErrStaleMain) {
			s.forgetHostWait(p, items)
			return false, nil, "", err
		}
		sha, merr := p.Ports().CodeHost.Merge(ctx, prFromRef(items[0].PR), "rebase")
		if merr != nil {
			return false, nil, "", fmt.Errorf("main was not moved (%v) and the pull request was not merged: %w", err, merr)
		}
		if ferr := g.Fetch(ctx); ferr != nil {
			s.d.Log.Warn("fetch after the pull request was merged", "err", ferr)
		}
		if m, merr := g.MainHead(ctx); merr == nil && m != "" && m != main {
			sha = m
		}
		head, shas = sha, []string{sha}
	}
	s.forgetHostWait(p, items)
	changed, _ := g.ChangedFiles(ctx, main, head)
	for i, it := range items {
		s.markIntegrated(ctx, p, it, shas[i])
	}
	s.revalidateAsync(p, domain.Change{Kind: "integration", Cause: "main " + short(head), Commit: head, Files: changed, Summary: integrationSummary(items)})
	s.mainMovedAsync(p, head, itemIDs(items), strings.Join(itemIDs(items), ", ")+" integrated")
	return true, nil, "", nil
}

func itemIDs(items []*domain.Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
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

// changeNote is how an item is described on main: what its delivery said,
// or what its kind, area and title give. As a last resort (an item delivered
// before descriptions existed, with a title that is not a valid header) it
// is recorded as upkeep, so the history keeps one format.
func changeNote(it *domain.Item) domain.ChangeNote {
	if it.Change != nil && it.Change.Validate() == nil {
		return *it.Change
	}
	if c := domain.DefaultChangeNote(it); c.Validate() == nil {
		return c
	}
	return domain.ChangeNote{Type: "chore", Scope: domain.Slug(it.Area, 20), Subject: "integrate " + it.ID}
}

// squashMessage is the message of the one commit an item becomes on main:
// a Conventional Commits header, the body and the item's trailer.
func squashMessage(it *domain.Item) string {
	return changeNote(it).CommitMessage(it.ID)
}

// writeChangelog adds the item's line to the Unreleased part of the
// project's changelog, in the item's worktree, as a commit of its branch:
// the line lands inside the item's own commit. Changes people do not read
// about (tests, build, upkeep) add nothing.
func (s *Service) writeChangelog(ctx context.Context, p *Project, dir string, it *domain.Item) error {
	name := p.Cfg().Merge.Changelog
	note := changeNote(it)
	section := note.ChangelogSection()
	if name == "" || section == "" {
		return nil
	}
	path := filepath.Join(dir, filepath.FromSlash(name))
	cur, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	next := domain.InsertChangelog(string(cur), section, note.ChangelogEntry(it.ID))
	if next == string(cur) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
		return err
	}
	_, _, err = p.Ports().Git.CommitAll(ctx, dir, "docs(changelog): add "+it.ID+"\n\n"+domain.CommitTrailer(it.ID))
	return err
}

// sameTreeAsChecked reports whether the tree about to land is exactly the
// one the item's own check step already verified: a single item, every
// check of that step green on one commit, and no difference between that
// commit and the squash besides the item's own memory files. That is the
// case whenever main did not move in between, and running the same checks
// on the same files again would only cost time.
func (s *Service) sameTreeAsChecked(ctx context.Context, p *Project, head string, items []*domain.Item) bool {
	g := p.Ports().Git
	if g == nil || head == "" || len(items) != 1 {
		return false
	}
	it := items[0]
	results := s.checkResultsSince(ctx, p, it.ID, nil)
	checked := ""
	for _, r := range results {
		if !r.Passed || r.Flaky || r.Commit == "" || (checked != "" && r.Commit != checked) {
			return false
		}
		checked = r.Commit
	}
	if checked == "" {
		return false
	}
	files, err := g.ChangedFiles(ctx, checked, head)
	if err != nil {
		return false
	}
	for _, f := range files {
		if !domain.Contains(ItemFiles, f) && f != p.Cfg().Merge.Changelog && f != domain.RepoMapFile {
			return false
		}
	}
	return true
}

// runQueueChecks runs the local checks over the combined tree.
func (s *Service) runQueueChecks(ctx context.Context, p *Project, dir, head string, items []*domain.Item) (bool, string) {
	return s.runQueueChecksWith(ctx, p, dir, head, items, false)
}

// runQueueChecksWith is runQueueChecks; full runs every local check over
// the whole tree (conflicts were resolved by git from a recorded
// resolution, so nothing about the result is taken as already checked).
func (s *Service) runQueueChecksWith(ctx context.Context, p *Project, dir, head string, items []*domain.Item, full bool) (bool, string) {
	if s.d.Checks == nil || dir == "" {
		return true, ""
	}
	if !full && s.sameTreeAsChecked(ctx, p, head, items) {
		return true, "" // main did not move since the item's own checks: nothing new to check
	}
	// What the batch changes, from git (deliveries may list fewer files).
	// The combined tree is main plus the batch, so only the checks the batch
	// concerns need to run again; what main changed was checked when it landed.
	var touched []string
	for _, it := range items {
		touched = append(touched, it.FilesTouched...)
	}
	fromGit := false
	if g := p.Ports().Git; g != nil && head != "" {
		if main, err := g.MainHead(ctx); err == nil && main != "" {
			if files, err := s.itemFiles(ctx, p, main, head); err == nil && len(files) > 0 {
				touched, fromGit = domain.Dedupe(append(touched, files...)), true
			}
			// The changelog line and the map Barnraise wrote are not a change of the product.
			cl := p.Cfg().Merge.Changelog
			kept := touched[:0:0]
			for _, f := range touched {
				if (cl == "" || f != cl) && f != domain.RepoMapFile {
					kept = append(kept, f)
				}
			}
			touched = kept
		}
	}
	if !fromGit || full {
		touched = nil // the change is unknown (or reapplied by git): never skip blind
	}
	for _, dec := range p.Cfg().SelectChecks(sortedKeys(p.Cfg().Checks), touched, nil) {
		name := dec.Name
		def := p.Cfg().Checks[name]
		if def.RunsOn != "local" || !dec.Run {
			continue // remote checks already ran in the item flow; the others do not concern this batch
		}
		def.Scope = "all" // the selection above already decided
		s.queueState(p, items, "working", "running check "+name+" on the combined result")
		res, pending, err := s.d.Checks.Run(ctx, ports.CheckSpec{Def: def, ItemID: items[0].ID, Commit: head, Dir: dir, Touched: touched,
			Env: s.worktreeCmdEnv(p, items[0].ID, dir)})
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
		s.retireWorktree(ctx, p, it.ID, s.WorktreeDir(p.Name, it.ID), retireIntegrate)
	}
	if p.Ports().CodeHost != nil && upd != nil && upd.PR != "" {
		go func() {
			// The squash lands on main as a new commit, so the host does not
			// see the pull request as merged: say where it went and close it.
			bg := context.Background()
			_ = p.Ports().CodeHost.Comment(bg, prFromRef(upd.PR), fmt.Sprintf("Integrated into %s as %s by the Barnraise merge queue.", p.Cfg().MainBranch, sha))
			_ = p.Ports().CodeHost.Close(bg, prFromRef(upd.PR), "")
		}()
	}
	if g := p.Ports().Git; g != nil && upd != nil && upd.Branch != "" {
		// The item's branch did its job; main has the change.
		if err := g.DeleteBranch(ctx, upd.Branch); err != nil {
			s.d.Log.Debug("delete the item's branch", "item", it.ID, "err", err)
		}
	}
	s.unblockDependents(ctx, p)
	s.syncTracker(p, it.ID, false)
}
