package knowledge

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// RepoMapHeader starts every repository map Barnraise writes.
const RepoMapHeader = "# Repository map\n"

const (
	mapSymbols         = 6    // symbols named per file
	defaultMapMaxFiles = 5000 // lines when the caller gives no bound
)

type mapEntry struct {
	path    string
	purpose string
	symbols []string
	more    int
}

func (e *mapEntry) line() string {
	var b strings.Builder
	b.WriteString("- " + e.path)
	if e.purpose != "" {
		b.WriteString(" — " + e.purpose)
	}
	if len(e.symbols) > 0 {
		b.WriteString(" · " + strings.Join(e.symbols, ", "))
		if e.more > 0 {
			fmt.Fprintf(&b, " (+%d)", e.more)
		}
	}
	return b.String()
}

// mainSymbols names the declarations a reader looks for first: the
// top-level ones in file order, or the methods when there are none.
func mainSymbols(syms []codeSymbol) ([]string, int) {
	// Exported-looking names first (capitalized: Go's exports, types
	// elsewhere), then the others, and names marked private last.
	var top, lower, private, methods []string
	for _, s := range syms {
		n := s.Name
		switch {
		case strings.Contains(n, "."):
			methods = append(methods, n)
		case n[0] == '_':
			private = append(private, n)
		case n[0] >= 'a' && n[0] <= 'z':
			lower = append(lower, n)
		default:
			top = append(top, n)
		}
	}
	top = append(append(top, lower...), private...)
	names := top
	if len(names) == 0 {
		names = methods
	}
	if len(names) > mapSymbols {
		return names[:mapSymbols], len(names) - mapSymbols
	}
	return names, 0
}

// inMap reports whether a file gets a line in the map: every indexed file
// but Barnraise's own documents (found with find) and the map itself.
func inMap(rel string, skip map[string]bool) bool {
	return !skip[rel] && rel != domain.RepoMapFile && !strings.HasPrefix(rel, docsDir+"/") && rel != docsDir
}

// RepoMap renders the repository map of a project: one line per file with
// what it is for and its main symbols. It comes from the index, except for
// the changed paths, which are read again from root (an item's worktree,
// whose files the index has not seen yet; a missing one is left out).
// Files in skip are left out. Past maxFiles lines, the least described
// files are counted per folder instead. The output depends only on the
// files, so an unchanged repository gives the same map.
func (ix *Index) RepoMap(ctx context.Context, project, root string, changed, skip []string, maxFiles int) (string, error) {
	if maxFiles <= 0 {
		maxFiles = defaultMapMaxFiles
	}
	skipSet := map[string]bool{}
	for _, s := range skip {
		skipSet[s] = true
	}
	entries := map[string]*mapEntry{}
	rows, err := ix.db.QueryContext(ctx, `SELECT path, purpose FROM grep_files WHERE project = ?`, project)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		e := &mapEntry{}
		if err := rows.Scan(&e.path, &e.purpose); err != nil {
			rows.Close()
			return "", err
		}
		if inMap(e.path, skipSet) {
			entries[e.path] = e
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	bySym := map[string][]codeSymbol{}
	srows, err := ix.db.QueryContext(ctx, `SELECT file, name FROM symbols WHERE project = ? ORDER BY file, line, name`, project)
	if err != nil {
		return "", err
	}
	for srows.Next() {
		var f, n string
		if err := srows.Scan(&f, &n); err != nil {
			srows.Close()
			return "", err
		}
		if entries[f] != nil {
			bySym[f] = append(bySym[f], codeSymbol{Name: n})
		}
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return "", err
	}
	for f, syms := range bySym {
		entries[f].symbols, entries[f].more = mainSymbols(syms)
	}
	if root != "" && len(changed) > 0 {
		abs, err := filepath.Abs(root)
		if err != nil {
			return "", err
		}
		var work []fileWork
		for _, c := range changed {
			rel := relPath(abs, c)
			if rel == "" || !inMap(rel, skipSet) {
				continue
			}
			delete(entries, rel)
			if !tracked(rel) {
				continue
			}
			src, ok, err := readSource(filepath.Join(abs, filepath.FromSlash(rel)))
			if err != nil {
				return "", err
			}
			if ok {
				work = append(work, fileWork{rel: rel, src: src})
			}
		}
		pre := ix.astSymbols(ctx, project, abs, work)
		for _, w := range work {
			e := &mapEntry{path: w.rel, purpose: filePurpose(w.rel, w.src)}
			if langOf(w.rel) != "" {
				syms, _ := symbolsOf(w, pre)
				e.symbols, e.more = mainSymbols(syms)
			}
			entries[w.rel] = e
		}
	}
	return renderMap(entries, maxFiles), nil
}

func renderMap(entries map[string]*mapEntry, maxFiles int) string {
	all := make([]*mapEntry, 0, len(entries))
	for _, e := range entries {
		all = append(all, e)
	}
	var rest map[string]int
	if len(all) > maxFiles {
		// Keep the best described files, shallow ones first.
		sort.Slice(all, func(i, j int) bool {
			si, sj := mapScore(all[i]), mapScore(all[j])
			if si != sj {
				return si > sj
			}
			return all[i].path < all[j].path
		})
		rest = map[string]int{}
		for _, e := range all[maxFiles:] {
			rest[path.Dir(e.path)+"/"]++
		}
		all = all[:maxFiles]
	}
	sort.Slice(all, func(i, j int) bool { return all[i].path < all[j].path })
	var b strings.Builder
	b.WriteString(RepoMapHeader)
	b.WriteString("\n<!-- Written by Barnraise from its knowledge index when an item is integrated. Do not edit: it is rewritten. -->\n\n")
	b.WriteString("One line per file: path — what it is for · its main symbols.\n\n")
	for _, e := range all {
		b.WriteString(e.line() + "\n")
	}
	if len(rest) > 0 {
		dirs := make([]string, 0, len(rest))
		for d := range rest {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		b.WriteString("\n## Not listed (over knowledge.map.max_files)\n\n")
		for _, d := range dirs {
			fmt.Fprintf(&b, "- %s — %d more files\n", strings.TrimPrefix(d, "./"), rest[d])
		}
	}
	return b.String()
}

func mapScore(e *mapEntry) int {
	s := 0
	if e.purpose != "" {
		s += 2
	}
	if len(e.symbols) > 0 {
		s += 2
	}
	return s*16 - min(strings.Count(e.path, "/"), 15)
}
