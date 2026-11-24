package domain

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// StaleReadsDef configures the stale-read warning: when main moves under an
// item in progress, the agent hears which of the files it read or changed
// were changed on main, before it finds out at the rebase.
type StaleReadsDef struct {
	Enabled bool `json:"enabled"`
	// Rebase rebases the item's branch on the new main right away when that
	// applies without a conflict (off by default: the merge queue rebases anyway).
	Rebase bool `json:"rebase"`
	// MaxFiles bounds the files listed in one warning.
	MaxFiles int `json:"max_files"`
}

// DefaultStaleReads is on, without the early rebase.
func DefaultStaleReads() StaleReadsDef { return StaleReadsDef{Enabled: true, MaxFiles: 10} }

// readTools and writeTools name the file tools of the supported providers
// (Claude Code, Codex, Gemini CLI). Searches (Grep, Glob, LS) are not reads
// of a file: a search over the repository would match every change.
var (
	readTools  = map[string]bool{"Read": true, "NotebookRead": true, "read_file": true, "read_many_files": true, "view": true}
	writeTools = map[string]bool{"Write": true, "Edit": true, "MultiEdit": true, "NotebookEdit": true, "apply_patch": true,
		"str_replace_based_edit_tool": true, "write_file": true, "replace": true, "edit": true, "edit_file": true, "file_change": true}
)

var patchHeaderRe = regexp.MustCompile(`(?m)^\*\*\* (?:Add|Update|Delete) File: (.+)$`)

// ToolFiles returns the files a tool call reads or changes, as the agent
// named them, and whether it changes them. Tools that are not file tools
// give nothing.
func ToolFiles(tool string, input json.RawMessage) (files []string, write bool) {
	write = writeTools[tool]
	if !write && !readTools[tool] {
		return nil, false
	}
	var in map[string]any
	if json.Unmarshal(input, &in) != nil {
		return nil, write
	}
	for _, k := range []string{"file_path", "notebook_path", "path", "absolute_path"} {
		if v, ok := in[k].(string); ok && strings.TrimSpace(v) != "" {
			files = append(files, strings.TrimSpace(v))
		}
	}
	for _, k := range []string{"paths", "file_paths"} {
		if arr, ok := in[k].([]any); ok {
			for _, a := range arr {
				if v, ok := a.(string); ok && strings.TrimSpace(v) != "" {
					files = append(files, strings.TrimSpace(v))
				}
			}
		}
	}
	if arr, ok := in["changes"].([]any); ok { // Codex file_change
		for _, a := range arr {
			if m, ok := a.(map[string]any); ok {
				if v, ok := m["path"].(string); ok && v != "" {
					files = append(files, v)
				}
			}
		}
	}
	for _, k := range []string{"input", "patch"} {
		if v, ok := in[k].(string); ok {
			for _, m := range patchHeaderRe.FindAllStringSubmatch(v, -1) {
				files = append(files, strings.TrimSpace(m[1]))
			}
		}
	}
	return dedupe(files), write
}

// RepoPath turns a path an agent used into a path relative to the root of
// its worktree. Paths outside the worktree, inside .git and Barnraise's own
// session folder are not files of the repository.
func RepoPath(worktree, p string) (string, bool) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if p == "" {
		return "", false
	}
	root := path.Clean(strings.ReplaceAll(worktree, "\\", "/"))
	if path.IsAbs(p) {
		p = path.Clean(p)
		if root == "" || root == "." || (p != root && !strings.HasPrefix(p, root+"/")) {
			return "", false
		}
		p = strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
	} else {
		p = path.Clean(p)
	}
	if p == "" || p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return "", false
	}
	for _, skip := range []string{".git", ".barnraise-session"} {
		if p == skip || strings.HasPrefix(p, skip+"/") {
			return "", false
		}
	}
	return p, true
}

// FileDiff is the short summary of one file of a diff.
type FileDiff struct {
	File    string   `json:"file"`
	Added   int      `json:"added"`
	Removed int      `json:"removed"`
	Hunks   []string `json:"hunks,omitempty"` // the context of the first hunks (@@ … @@ func Foo)
	New     bool     `json:"new,omitempty"`
	Deleted bool     `json:"deleted,omitempty"`
}

// String renders "api/wallet.go: +12 -3 (func Load; type Balance)".
func (f FileDiff) String() string {
	s := fmt.Sprintf("%s: +%d -%d", f.File, f.Added, f.Removed)
	switch {
	case f.New:
		s += " (new file)"
	case f.Deleted:
		s += " (deleted)"
	case len(f.Hunks) > 0:
		s += " (" + strings.Join(f.Hunks, "; ") + ")"
	}
	return s
}

// SummarizeDiff counts the lines each file of a unified diff adds and
// removes, with the context of its first hunks, for the files asked (all
// when files is empty). It is deterministic: no model reads the diff.
func SummarizeDiff(diff string, files []string, maxHunks int) []FileDiff {
	want := map[string]bool{}
	for _, f := range files {
		want[f] = true
	}
	var out []FileDiff
	var cur *FileDiff
	flush := func() {
		if cur != nil && (len(want) == 0 || want[cur.File]) {
			out = append(out, *cur)
		}
		cur = nil
	}
	for line := range strings.SplitSeq(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			name := line[len("diff --git "):]
			if i := strings.LastIndex(name, " b/"); i >= 0 {
				name = name[i+3:]
			}
			cur = &FileDiff{File: name}
		case cur == nil:
		case strings.HasPrefix(line, "new file mode"):
			cur.New = true
		case strings.HasPrefix(line, "deleted file mode"):
			cur.Deleted = true
		case strings.HasPrefix(line, "+++ "), strings.HasPrefix(line, "--- "):
		case strings.HasPrefix(line, "@@"):
			if ctx := hunkContext(line); ctx != "" && len(cur.Hunks) < maxHunks && !contains(cur.Hunks, ctx) {
				cur.Hunks = append(cur.Hunks, ctx)
			}
		case strings.HasPrefix(line, "+"):
			cur.Added++
		case strings.HasPrefix(line, "-"):
			cur.Removed++
		}
	}
	flush()
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}

// hunkContext is what git prints after a hunk header: the enclosing function.
func hunkContext(h string) string {
	i := strings.Index(h[2:], "@@")
	if i < 0 {
		return ""
	}
	ctx := strings.TrimSpace(SanitizeExternal(h[2+i+2:]))
	if len(ctx) > 60 {
		ctx = ctx[:60] + "…"
	}
	return ctx
}

// StaleFiles is what the stale-read warning is about: the files main
// changed that the item also read or changed, without the item's own
// memory files. The result is sorted.
func StaleFiles(mainChanged, itemTouched, ignore []string) []string {
	touched := map[string]bool{}
	for _, f := range itemTouched {
		touched[f] = true
	}
	var out []string
	for _, f := range dedupe(mainChanged) {
		if touched[f] && !contains(ignore, f) {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}
