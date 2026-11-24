package knowledge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"sort"
	"strconv"
	"strings"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// Search kinds that run the trigram search instead of the layered one.
const (
	KindGrep  = "grep"  // exact substring
	KindRegex = "regex" // regular expression (RE2 syntax)
	KindMatch = "match" // the kind of the hits they return
)

// LayerSubstring is the layer of hits found by the trigram index.
const LayerSubstring = "substring"

// ErrBadPattern wraps an invalid regular expression.
var ErrBadPattern = errors.New("knowledge: invalid pattern")

// GrepOptions narrow a trigram search.
type GrepOptions struct {
	Regex      bool   // the pattern is a regular expression; otherwise an exact substring
	IgnoreCase bool   // fold case (ASCII and Unicode, as Go's regexp does)
	PathPrefix string // only files under this path
	Area       string // only files of this area
	Limit      int    // most matching lines; default 50
}

// GrepMatch is one matching line.
type GrepMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

const (
	defaultGrepLimit = 50
	maxGrepLimit     = 1000
	maxMatchText     = 240
)

// Grep finds the lines of a project's files that contain a substring or
// match a regular expression. The trigram index picks the files that can
// match; only those are read and checked line by line.
func (ix *Index) Grep(ctx context.Context, project, pattern string, o GrepOptions) ([]GrepMatch, error) {
	if pattern == "" {
		return nil, fmt.Errorf("%w: empty pattern", ErrBadPattern)
	}
	if o.Limit <= 0 {
		o.Limit = defaultGrepLimit
	}
	o.Limit = min(o.Limit, maxGrepLimit)
	expr := pattern
	if !o.Regex {
		expr = regexp.QuoteMeta(pattern)
	}
	if o.IgnoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadPattern, err)
	}
	// One look at the whole file first, with ^ and $ at line ends as they
	// are for the line by line check.
	whole := regexp.MustCompile("(?m)" + expr)
	var q *triQuery
	if o.Regex {
		parsed, err := syntax.Parse(expr, syntax.Perl)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadPattern, err)
		}
		q = regexQuery(parsed)
	} else {
		q = literalQuery(lowerString(pattern))
	}
	files, err := ix.grepCandidates(ctx, project, q, o)
	if err != nil {
		return nil, err
	}
	var out []GrepMatch
	for _, f := range files {
		content, err := ix.grepContent(ctx, f.id)
		if err != nil {
			return nil, err
		}
		if !whole.Match(content) {
			continue
		}
		for n, line := range bytes.Split(content, []byte("\n")) {
			if re.Match(line) {
				out = append(out, GrepMatch{Path: f.path, Line: n + 1, Text: oneLine(strings.TrimRight(string(line), "\r"), maxMatchText)})
				if len(out) >= o.Limit {
					return out, nil
				}
			}
		}
	}
	return out, nil
}

type grepFile struct {
	id   int64
	path string
}

// grepCandidates lists, in path order, the files of a project that satisfy
// the trigram condition and the path and area filters.
func (ix *Index) grepCandidates(ctx context.Context, project string, q *triQuery, o GrepOptions) ([]grepFile, error) {
	cand, all, err := ix.evalQuery(ctx, q, map[uint32][]int64{})
	if err != nil {
		return nil, err
	}
	if !all && len(cand) == 0 {
		return nil, nil
	}
	where, args := "project = ?", []any{project}
	if p := strings.TrimPrefix(strings.TrimSpace(o.PathPrefix), "./"); p != "" {
		where += " AND path >= ? AND path < ?"
		args = append(args, p, p+"￿")
	}
	rows, err := ix.db.QueryContext(ctx, `SELECT id, path FROM grep_files WHERE `+where+` ORDER BY path`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var areas map[string]areaSpec
	area := ""
	if o.Area != "" {
		area = strings.TrimPrefix(strings.TrimSpace(o.Area), "A-")
	}
	var out []grepFile
	for rows.Next() {
		var f grepFile
		if err := rows.Scan(&f.id, &f.path); err != nil {
			return nil, err
		}
		if !all && !cand[f.id] {
			continue
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if area != "" {
		if areas, err = ix.loadAreas(ctx, project); err != nil {
			return nil, err
		}
		kept := out[:0]
		for _, f := range out {
			for _, a := range areasFor(areas, f.path) {
				if a == area {
					kept = append(kept, f)
					break
				}
			}
		}
		out = kept
	}
	return out, nil
}

func (ix *Index) grepContent(ctx context.Context, id int64) ([]byte, error) {
	var b []byte
	err := ix.db.QueryRowContext(ctx, `SELECT content FROM grep_files WHERE id = ?`, id).Scan(&b)
	return b, err
}

// evalQuery returns the file ids that satisfy q, or all=true when q puts
// no condition. Postings are read once per trigram.
func (ix *Index) evalQuery(ctx context.Context, q *triQuery, cache map[uint32][]int64) (map[int64]bool, bool, error) {
	postings := func(t uint32) ([]int64, error) {
		if p, ok := cache[t]; ok {
			return p, nil
		}
		rows, err := ix.db.QueryContext(ctx, `SELECT file_id FROM grep_postings WHERE tri = ?`, int64(t))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var p []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			p = append(p, id)
		}
		cache[t] = p
		return p, rows.Err()
	}
	switch q.op {
	case qAnd:
		var set map[int64]bool
		lists := make([][]int64, 0, len(q.tri))
		for _, t := range q.tri {
			p, err := postings(t)
			if err != nil {
				return nil, false, err
			}
			if len(p) == 0 {
				return nil, false, nil
			}
			lists = append(lists, p)
		}
		sort.Slice(lists, func(i, j int) bool { return len(lists[i]) < len(lists[j]) })
		for _, p := range lists {
			set = intersect(set, p)
			if len(set) == 0 {
				return nil, false, nil
			}
		}
		for _, s := range q.sub {
			sub, all, err := ix.evalQuery(ctx, s, cache)
			if err != nil {
				return nil, false, err
			}
			if all {
				continue
			}
			if set == nil {
				set = sub
			} else {
				for id := range set {
					if !sub[id] {
						delete(set, id)
					}
				}
			}
			if len(set) == 0 {
				return nil, false, nil
			}
		}
		return set, set == nil, nil
	case qOr:
		set := map[int64]bool{}
		for _, t := range q.tri {
			p, err := postings(t)
			if err != nil {
				return nil, false, err
			}
			for _, id := range p {
				set[id] = true
			}
		}
		for _, s := range q.sub {
			sub, all, err := ix.evalQuery(ctx, s, cache)
			if err != nil {
				return nil, false, err
			}
			if all {
				return nil, true, nil
			}
			for id := range sub {
				set[id] = true
			}
		}
		return set, false, nil
	}
	return nil, true, nil
}

// intersect keeps the ids of set (nil: every id) that are in p.
func intersect(set map[int64]bool, p []int64) map[int64]bool {
	out := make(map[int64]bool, min(len(p), len(set)+1))
	for _, id := range p {
		if set == nil || set[id] {
			out[id] = true
		}
	}
	return out
}

// grepHits runs a trigram search for Search with kind grep or regex. A
// leading "file:<prefix>" word limits it to a path.
func (ix *Index) grepHits(ctx context.Context, project, query string, f ports.SearchFilters, limit int, regex bool) ([]ports.SearchHit, error) {
	prefix := ""
	if rest, ok := strings.CutPrefix(query, "file:"); ok {
		p, q, _ := strings.Cut(rest, " ")
		prefix, query = p, strings.TrimSpace(q)
	}
	ms, err := ix.Grep(ctx, project, query, GrepOptions{Regex: regex, PathPrefix: prefix, Area: f.Area, Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]ports.SearchHit, 0, len(ms))
	for i, m := range ms {
		ref := m.Path + ":" + strconv.Itoa(m.Line)
		out = append(out, ports.SearchHit{ID: ref, Kind: KindMatch, Title: ref, Summary: m.Text, Path: m.Path,
			Score: 1 / float64(60+i+1), Layer: LayerSubstring})
	}
	return out, nil
}

// substringLayer finds the code files that contain a one-word query
// anywhere, even inside longer identifiers, ranked by how often.
func (ix *Index) substringLayer(ctx context.Context, project, q, fsql string, fargs []any, limit int) ([]string, error) {
	if len(q) < 3 || strings.ContainsAny(q, " \t\n") {
		return nil, nil
	}
	lq := []byte(lowerString(q))
	files, err := ix.grepCandidates(ctx, project, literalQuery(string(lq)), GrepOptions{})
	if err != nil {
		return nil, err
	}
	type scored struct {
		id string
		n  int
	}
	var found []scored
	for i, f := range files {
		if i >= maxSubstringFiles {
			break
		}
		content, err := ix.grepContent(ctx, f.id)
		if err != nil {
			return nil, err
		}
		if n := bytes.Count(bytes.Map(func(r rune) rune {
			if r < 0x80 {
				return rune(lowerASCII(byte(r)))
			}
			return r
		}, content), lq); n > 0 {
			found = append(found, scored{"file:" + f.path, n})
		}
	}
	if len(found) == 0 {
		return nil, nil
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].n > found[j].n })
	cand := make([]string, 0, len(found))
	for _, s := range found {
		cand = append(cand, s.id)
	}
	ok, err := ix.column(ctx, `SELECT id FROM docs d WHERE d.project = ? AND d.id IN (`+placeholders(len(cand))+`) AND `+fsql,
		append(append([]any{project}, anySlice(cand)...), fargs...)...)
	if err != nil {
		return nil, err
	}
	keep := map[string]bool{}
	for _, id := range ok {
		keep[id] = true
	}
	var out []string
	for _, id := range cand {
		if keep[id] {
			out = append(out, id)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

// maxSubstringFiles bounds the files read by the substring layer of an
// ordinary search; the grep kind has no such bound.
const maxSubstringFiles = 200
