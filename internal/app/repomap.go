package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// repoMapper is the optional knowledge extension that renders the
// repository map (ADR-0039).
type repoMapper interface {
	RepoMap(ctx context.Context, project, root string, changed, skip []string, maxFiles int) (string, error)
	IndexedCommit(ctx context.Context, project string) (string, time.Time, error)
}

// writeRepoMap refreshes the repository map in the item's worktree, on top
// of main as it is now, as a commit of the item's branch: like the
// changelog line, the map lands inside the item's own commit on main.
// Agents cannot write .barnraise/, so the map is always Barnraise's. A
// map that cannot be made never holds the integration back.
func (s *Service) writeRepoMap(ctx context.Context, p *Project, dir string, it *domain.Item) {
	cfg := p.Cfg().Knowledge.Map
	rm, ok := s.d.Knowledge.(repoMapper)
	g := p.Ports().Git
	if !cfg.Enabled || !ok || g == nil {
		return
	}
	head, err := g.Head(ctx, dir)
	if err != nil {
		s.d.Log.Warn("repository map: head of the item", "item", it.ID, "err", err)
		return
	}
	// The index knows main at the commit it last saw; everything that
	// changed since, on main and in the item, is read from the worktree.
	base, _, _ := rm.IndexedCommit(ctx, p.Name)
	changed, err := g.ChangedFiles(ctx, base, head)
	if base == "" || err != nil {
		main, merr := g.MainHead(ctx)
		if merr != nil {
			s.d.Log.Warn("repository map: main", "item", it.ID, "err", merr)
			return
		}
		if changed, err = g.ChangedFiles(ctx, main, head); err != nil {
			s.d.Log.Warn("repository map: changed files", "item", it.ID, "err", err)
			return
		}
	}
	text, err := rm.RepoMap(ctx, p.Name, dir, changed, ItemFiles, cfg.MaxFiles)
	if err != nil {
		s.d.Log.Warn("repository map", "item", it.ID, "err", err)
		return
	}
	file := filepath.Join(dir, filepath.FromSlash(domain.RepoMapFile))
	if cur, err := os.ReadFile(file); err == nil && string(cur) == text {
		return
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		s.d.Log.Warn("repository map", "item", it.ID, "err", err)
		return
	}
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		s.d.Log.Warn("repository map", "item", it.ID, "err", err)
		return
	}
	if _, _, err := g.CommitAll(ctx, dir, "docs(map): refresh the repository map for "+it.ID+"\n\n"+domain.CommitTrailer(it.ID)); err != nil {
		s.d.Log.Warn("repository map: commit", "item", it.ID, "err", err)
	}
}

// restoreBarnraiseFiles puts every file under .barnraise/ of an item's
// worktree (the repository map, INDEX.md, the documents...) back to the one
// at the branch's base when it is neither that one nor main's now. Agents
// cannot write .barnraise/; when a session's sandbox enforces that on the
// commands too, a git rebase or reset the agent runs cannot update a file
// main changed and leaves the old copy behind, as a change of the worktree.
// Committed with the step, that copy would revert main's file in the item's
// pull request and refuse every later session of the item as an instruction
// file changed outside Barnraise. What the owner accepted for the item stays.
func (s *Service) restoreBarnraiseFiles(ctx context.Context, p *Project, itemID, dir string) {
	g := p.Ports().Git
	if g == nil || dir == "" || filepath.Clean(dir) == filepath.Clean(p.Cfg().Root) {
		return
	}
	var files []string
	_ = filepath.WalkDir(filepath.Join(dir, ".barnraise"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if rel, err := filepath.Rel(dir, path); err == nil {
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if len(files) == 0 {
		return
	}
	main, err := g.MainHead(ctx)
	if err != nil || main == "" {
		return
	}
	base := main
	if head, err := g.Head(ctx, dir); err == nil && head != "" {
		if b, err := g.MergeBase(ctx, head, main); err == nil && b != "" {
			base = b
		}
	}
	var acc map[string]bool
	for _, rel := range files {
		file := filepath.Join(dir, filepath.FromSlash(rel))
		cur, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		want, err := g.Show(ctx, base, rel)
		if err != nil || bytes.Equal(cur, want) {
			continue // not at the base (untracked or new), or the worktree has it
		}
		if base != main {
			if now, err := g.Show(ctx, main, rel); err == nil && bytes.Equal(cur, now) {
				continue
			}
		}
		if acc == nil {
			if acc, err = s.accepted(ctx, p, itemID); err != nil {
				return
			}
		}
		if acc[rel+"\x00"+bytesSHA(cur)] {
			continue
		}
		if err := os.WriteFile(file, want, 0o644); err != nil {
			s.d.Log.Warn("barnraise files: restore", "item", itemID, "file", rel, "err", err)
			continue
		}
		s.d.Log.Info("barnraise file restored to the branch's base", "item", itemID, "file", rel, "base", short(base))
	}
}

// repoMapPart is the repository map as a part of the context pack: all of
// it when it fits knowledge.map.prompt_tokens, otherwise the lines closest
// to the item (its files, their folders, its area, the words of its title).
func (s *Service) repoMapPart(p *Project, it *domain.Item) (domain.ContextPart, bool) {
	cfg := p.Cfg().Knowledge.Map
	if !cfg.Enabled || cfg.PromptTokens <= 0 {
		return domain.ContextPart{}, false
	}
	var data []byte
	for _, root := range []string{s.WorktreeDir(p.Name, it.ID), p.Cfg().Root} {
		if root == "" {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(domain.RepoMapFile))); err == nil {
			data = b
			break
		}
	}
	if len(data) == 0 {
		return domain.ContextPart{}, false
	}
	body := mapSubset(string(data), mapFocus(p.Cfg(), it), cfg.PromptTokens)
	if body == "" {
		return domain.ContextPart{}, false
	}
	return domain.ContextPart{Order: 4, ID: "map", Kind: "map", Title: "Repository map (" + domain.RepoMapFile + ")", Body: body, Relevance: 0.5}, true
}

// focus is what makes a line of the map relevant to an item.
type focus struct {
	files []string // anchors and files touched
	dirs  []string // their folders and the area's folder
	words []string // of the title, lower case
}

func mapFocus(cfg *domain.ProjectConfig, it *domain.Item) focus {
	var f focus
	for _, x := range append(append([]string(nil), it.Anchors.Files...), it.FilesTouched...) {
		x = strings.TrimPrefix(strings.TrimSpace(x), "./")
		if x == "" {
			continue
		}
		f.files = append(f.files, x)
		if d := path.Dir(x); d != "." {
			f.dirs = append(f.dirs, d+"/")
		}
	}
	if a, ok := cfg.Areas[domain.AreaName(it.Area)]; ok && a.Dir != "" {
		f.dirs = append(f.dirs, strings.TrimSuffix(a.Dir, "/")+"/")
	}
	for _, w := range strings.FieldsFunc(strings.ToLower(it.Title), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_')
	}) {
		if len(w) >= 4 {
			f.words = append(f.words, w)
		}
	}
	f.files, f.dirs, f.words = domain.Dedupe(f.files), domain.Dedupe(f.dirs), domain.Dedupe(f.words)
	return f
}

// mapPath is the path of a map line ("- path — purpose · symbols").
func mapPath(line string) string {
	rest := strings.TrimPrefix(line, "- ")
	for _, sep := range []string{" — ", " · "} {
		if i := strings.Index(rest, sep); i >= 0 {
			rest = rest[:i]
		}
	}
	return strings.TrimSpace(rest)
}

func (f focus) score(line string) int {
	p := mapPath(line)
	s := 0
	for _, x := range f.files {
		if p == x {
			s += 100
		}
	}
	for _, d := range f.dirs {
		if strings.HasPrefix(p, d) {
			s += 30
			if path.Dir(p)+"/" == d {
				s += 20
			}
		}
	}
	low := strings.ToLower(line)
	for _, w := range f.words {
		if strings.Contains(low, w) {
			s += 5
		}
	}
	return s
}

// mapSubset keeps the lines of the map that fit the budget, the most
// relevant first, in the map's order.
func mapSubset(m string, f focus, budget int) string {
	var lines []string
	for _, l := range strings.Split(m, "\n") {
		if strings.HasPrefix(l, "## ") {
			break // the folders over max_files say nothing a session can use
		}
		if strings.HasPrefix(l, "- ") {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	all := strings.Join(lines, "\n")
	if domain.EstimateTokens(all) <= budget {
		return all
	}
	idx := make([]int, len(lines))
	for i := range idx {
		idx[i] = i
	}
	scores := make([]int, len(lines))
	for i, l := range lines {
		scores[i] = f.score(l)
	}
	sort.SliceStable(idx, func(a, b int) bool { return scores[idx[a]] > scores[idx[b]] })
	footer := func(n int) string {
		return fmt.Sprintf("(%d of %d files shown; the whole map is %s, readable in the worktree)", n, len(lines), domain.RepoMapFile)
	}
	room := budget - domain.EstimateTokens(footer(len(lines)))
	keep := map[int]bool{}
	used := 0
	for _, i := range idx {
		t := domain.EstimateTokens(lines[i]) + 1
		if used+t > room {
			continue
		}
		keep[i] = true
		used += t
	}
	var out []string
	for i, l := range lines {
		if keep[i] {
			out = append(out, l)
		}
	}
	return strings.Join(append(out, footer(len(out))), "\n")
}
