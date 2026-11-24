package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// staleState is what the stale-read warning remembers between passes: the
// main each item was last compared against, and the files already noted
// (so a file read a hundred times is written once).
type staleState struct {
	mu     sync.Mutex // one pass at a time
	base   sync.Map   // project/item -> main sha last compared
	noted  sync.Map   // project/item/path/write -> true
	passes sync.WaitGroup
}

// readLog is the store's file log when it keeps one (SQLite does). Without
// one, the warning knows only the files the item's branch changes.
func (s *Service) readLog() ports.FileReadLog {
	rl, _ := s.d.Store.(ports.FileReadLog)
	return rl
}

// noteToolFiles records the files a tool call of an item's session read or
// changed. It runs for every tool call of the session, so it does nothing
// for the other tools and writes each file once.
func (s *Service) noteToolFiles(ctx context.Context, p *Project, itemID, worktree, tool string, input json.RawMessage) {
	if !p.Cfg().StaleReads.Enabled || itemID == "" {
		return
	}
	files, write := domain.ToolFiles(tool, input)
	var fresh []ports.FileTouch
	now := s.d.Clock.Now().UTC()
	for _, f := range files {
		rel, ok := domain.RepoPath(worktree, f)
		if !ok {
			continue
		}
		key := fmt.Sprintf("%s/%s/%s/%v", p.Name, itemID, rel, write)
		if _, seen := s.stale.noted.LoadOrStore(key, true); seen {
			continue
		}
		fresh = append(fresh, ports.FileTouch{Path: rel, Write: write, At: now})
	}
	rl := s.readLog()
	if len(fresh) == 0 || rl == nil {
		return
	}
	if err := rl.NoteFiles(ctx, p.Name, itemID, fresh); err != nil {
		s.d.Log.Debug("note the files a session read", "item", itemID, "err", err)
		for _, f := range fresh {
			s.stale.noted.Delete(fmt.Sprintf("%s/%s/%s/%v", p.Name, itemID, f.Path, f.Write))
		}
	}
}

// forgetReads drops what was noted for a finished item.
func (s *Service) forgetReads(ctx context.Context, p *Project, itemID string) {
	if rl := s.readLog(); rl != nil {
		_ = rl.ForgetFiles(ctx, p.Name, itemID)
	}
	s.stale.base.Delete(p.Name + "/" + itemID)
	prefix := p.Name + "/" + itemID + "/"
	s.stale.noted.Range(func(k, _ any) bool {
		if strings.HasPrefix(k.(string), prefix) {
			s.stale.noted.Delete(k)
		}
		return true
	})
}

// StaleRead is one warning: main moved and changed files the item read or changed.
type StaleRead struct {
	ItemID   string            `json:"item_id"`
	Main     string            `json:"main"`
	From     string            `json:"from"`
	Files    []domain.FileDiff `json:"files"`
	More     int               `json:"more,omitempty"` // stale files beyond max_files
	Rebased  bool              `json:"rebased,omitempty"`
	Conflict string            `json:"conflict,omitempty"` // why the early rebase did not apply
}

// mainMovedAsync runs the stale-read pass in the background after main
// moved: newMain is the new head ("" fetches and reads it), done lists
// the items that just landed.
func (s *Service) mainMovedAsync(p *Project, newMain string, done []string, cause string) {
	if !p.Cfg().StaleReads.Enabled || p.Ports().Git == nil {
		return
	}
	s.stale.passes.Add(1)
	go func() {
		defer s.stale.passes.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if _, err := s.mainMoved(ctx, p, newMain, done, cause); err != nil {
			s.d.Log.Warn("stale-read pass", "cause", cause, "err", err)
		}
	}()
}

// mainMoved compares, for every item in progress on this machine, what main
// changed since the item last looked with the files its sessions read or
// changed and the files its branch changes. Each item with an overlap gets a
// message (delivered between tool calls and in the next step's prompt) and
// an item.stale_read event; with stale_reads.rebase its branch is rebased on
// the new main when that applies cleanly.
func (s *Service) mainMoved(ctx context.Context, p *Project, newMain string, done []string, cause string) ([]StaleRead, error) {
	g := p.Ports().Git
	if g == nil || !p.Cfg().StaleReads.Enabled {
		return nil, nil
	}
	s.stale.mu.Lock()
	defer s.stale.mu.Unlock()
	snap := p.Snapshot()
	for _, id := range done {
		s.forgetReads(ctx, p, id)
	}
	if rl := s.readLog(); rl != nil {
		ids, _ := rl.ReadItems(ctx, p.Name)
		for _, id := range ids {
			if it := snap.Items[id]; it == nil || it.Status.Terminal() {
				s.forgetReads(ctx, p, id)
			}
		}
	}
	type candidate struct {
		it  *domain.Item
		dir string
	}
	var cands []candidate
	for _, it := range snap.ItemList() {
		if it.Status != domain.StatusInProgress || it.Claim == nil || it.Claim.Machine != s.d.Machine || domain.Contains(done, it.ID) {
			continue
		}
		dir := s.WorktreeDir(p.Name, it.ID)
		if _, err := os.Stat(dir); err == nil {
			cands = append(cands, candidate{it, dir})
		}
	}
	if len(cands) == 0 {
		return nil, nil // nothing in progress here: no fetch
	}
	if newMain == "" {
		if err := g.Fetch(ctx); err != nil {
			s.d.Log.Debug("fetch for the stale-read pass", "err", err)
		}
		var err error
		if newMain, err = g.MainHead(ctx); err != nil {
			return nil, err
		}
	}
	var out []StaleRead
	for _, c := range cands {
		w, err := s.staleCheck(ctx, p, c.it, c.dir, newMain, cause)
		if err != nil {
			s.d.Log.Debug("stale-read check", "item", c.it.ID, "err", err)
			continue
		}
		if w != nil {
			out = append(out, *w)
		}
	}
	return out, nil
}

func (s *Service) staleCheck(ctx context.Context, p *Project, it *domain.Item, dir, newMain, cause string) (*StaleRead, error) {
	g := p.Ports().Git
	head, err := g.Head(ctx, dir)
	if err != nil {
		return nil, err
	}
	key := p.Name + "/" + it.ID
	base := ""
	if v, ok := s.stale.base.Load(key); ok {
		base = v.(string)
	}
	if base == "" {
		if base, err = g.MergeBase(ctx, head, newMain); err != nil {
			return nil, err
		}
	}
	if base == newMain {
		return nil, nil
	}
	mainChanged, err := g.ChangedFiles(ctx, base, newMain)
	if err != nil {
		return nil, err
	}
	s.stale.base.Store(key, newMain)
	touched, _ := s.itemFiles(ctx, p, base, head)
	if rl := s.readLog(); rl != nil {
		noted, _ := rl.TouchedFiles(ctx, p.Name, it.ID)
		for _, f := range noted {
			touched = append(touched, f.Path)
		}
	}
	ignore := append([]string(nil), ItemFiles...)
	if cl := p.Cfg().Merge.Changelog; cl != "" {
		ignore = append(ignore, cl) // Barnraise writes it at integration
	}
	stale := domain.StaleFiles(mainChanged, touched, ignore)
	if len(stale) == 0 {
		return nil, nil
	}
	w := &StaleRead{ItemID: it.ID, Main: newMain, From: base}
	if limit := p.Cfg().StaleReads.MaxFiles; limit > 0 && len(stale) > limit {
		w.More = len(stale) - limit
		stale = stale[:limit]
	}
	diff, _ := g.Diff(ctx, base, newMain, 400000)
	sum := map[string]domain.FileDiff{}
	for _, f := range domain.SummarizeDiff(diff, stale, 3) {
		sum[f.File] = f
	}
	for _, f := range stale {
		fd, ok := sum[f]
		if !ok {
			fd = domain.FileDiff{File: f}
		}
		w.Files = append(w.Files, fd)
	}
	if p.Cfg().StaleReads.Rebase {
		s.earlyRebase(ctx, p, it, dir, newMain, w)
	}
	text := staleText(w, cause)
	_, _ = s.SendMessage(ctx, p.Name, System(), domain.Message{To: "item:" + it.ID, ItemID: it.ID, Text: text})
	files := make([]string, len(w.Files))
	for i, f := range w.Files {
		files[i] = f.File
	}
	_, _ = s.emit(ctx, p, domain.EvItemStaleRead, it.ID, "barnraise", domain.Payload{Cause: cause, Note: text,
		Extra: map[string]any{"main": newMain, "from": base, "files": files, "more": w.More, "rebased": w.Rebased, "conflict": w.Conflict}})
	return w, nil
}

// earlyRebase puts the item's branch on the new main right away, when it
// applies without a conflict. Pending changes are committed first; a
// conflict leaves the branch as it was (the merge queue deals with it).
func (s *Service) earlyRebase(ctx context.Context, p *Project, it *domain.Item, dir, newMain string, w *StaleRead) {
	g := p.Ports().Git
	if _, err := s.commitStep(ctx, p, it.ID, "chore("+it.ID+"): pending changes before an early rebase"); err != nil {
		w.Conflict = "pending changes could not be committed: " + err.Error()
		return
	}
	if err := g.Rebase(ctx, dir, newMain); err != nil {
		if errors.Is(err, ports.ErrConflict) {
			w.Conflict = "it conflicts: " + strings.TrimPrefix(err.Error(), ports.ErrConflict.Error()+": ")
		} else {
			w.Conflict = err.Error()
		}
		return
	}
	w.Rebased = true
	if it.Branch != "" {
		if err := g.Push(ctx, dir, it.Branch); err != nil {
			s.d.Log.Debug("push after an early rebase", "item", it.ID, "err", err)
		}
	}
}

// staleText is the message the agent gets.
func staleText(w *StaleRead, cause string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Main moved to %s (%s) and changed files you read or changed:\n", short(w.Main), cause)
	for _, f := range w.Files {
		b.WriteString("- " + f.String() + "\n")
	}
	if w.More > 0 {
		fmt.Fprintf(&b, "- and %d more\n", w.More)
	}
	fmt.Fprintf(&b, "What you saw of them may be out of date: `git diff %s %s -- <file>` shows what changed. ", short(w.From), short(w.Main))
	switch {
	case w.Rebased:
		b.WriteString("Barnraise rebased your branch on the new main: read these files again before relying on them.")
	case w.Conflict != "":
		b.WriteString("An early rebase did not apply (" + w.Conflict + "): read these files again, keep your change compatible with main, and expect the merge queue to rebase it.")
	default:
		b.WriteString("Read them again before relying on them; the merge queue rebases your branch on main when it lands.")
	}
	return b.String()
}
