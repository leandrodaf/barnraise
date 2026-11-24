package app

import (
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
