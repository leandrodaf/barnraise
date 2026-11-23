package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Layer names reported in SearchHit.Layer.
const (
	LayerExact  = "exact"
	LayerFilter = "filter"
	LayerText   = "text"
)

// exactWeight is how many times the exact layer enters the fusion: an exact
// id, path or symbol hit outranks any text-only hit.
const exactWeight = 2

// Search runs the layered search and fuses the rankings with domain.RRF.
//
//  1. exact: ids (I-142, D-12, A-payments, standard:payments, sym:…), file paths
//     and symbol names;
//  2. filter: when the query is empty, the documents matching the filters,
//     newest first (filters always constrain every layer);
//  3. text: FTS5 bm25 over title, summary and body chunks, accent-folded.
//
// Superseded decisions, expired memories and anything with status
// superseded/expired are left out unless filters.Status is "all" (or
// asks for that status explicitly).
func (ix *Index) Search(ctx context.Context, project, query string, f ports.SearchFilters, limit int) ([]ports.SearchHit, error) {
	if limit <= 0 {
		limit = 20
	}
	query = strings.TrimSpace(query)
	fsql, fargs := ix.filterSQL(f)
	exact, err := ix.exactLayer(ctx, project, query, fsql, fargs, limit*2)
	if err != nil {
		return nil, err
	}
	var text, filt []string
	if query == "" {
		if filt, err = ix.filterLayer(ctx, project, f, fsql, fargs, limit*2); err != nil {
			return nil, err
		}
	} else if text, err = ix.textLayer(ctx, project, query, fsql, fargs, limit*3); err != nil {
		return nil, err
	}
	lists := make([][]string, 0, exactWeight+2)
	for i := 0; i < exactWeight; i++ {
		lists = append(lists, exact)
	}
	lists = append(lists, filt, text)
	fused := domain.RRF(lists...)
	if len(fused) > limit {
		fused = fused[:limit]
	}
	if len(fused) == 0 {
		return nil, nil
	}
	score := map[string]float64{}
	layer := map[string]string{}
	best := map[string]int{}
	for _, l := range []struct {
		name string
		ids  []string
		w    float64
	}{{LayerExact, exact, exactWeight}, {LayerFilter, filt, 1}, {LayerText, text, 1}} {
		for r, id := range l.ids {
			score[id] += l.w / (60 + float64(r+1))
			if b, ok := best[id]; !ok || r < b {
				best[id], layer[id] = r, l.name
			}
		}
	}
	rows, err := ix.db.QueryContext(ctx, `SELECT id, kind, title, summary, area, path, updated, tokens FROM docs
		WHERE project = ? AND id IN (`+placeholders(len(fused))+`)`, append([]any{project}, anySlice(fused)...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[string]ports.SearchHit{}
	for rows.Next() {
		var h ports.SearchHit
		var updated int64
		if err := rows.Scan(&h.ID, &h.Kind, &h.Title, &h.Summary, &h.Area, &h.Path, &updated, &h.Tokens); err != nil {
			return nil, err
		}
		h.Date = timeOrZero(updated)
		if h.Area != "" {
			h.Area = domain.AreaID(h.Area)
		}
		h.Score, h.Layer = score[h.ID], layer[h.ID]
		byID[h.ID] = h
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ports.SearchHit, 0, len(fused))
	for _, id := range fused {
		if h, ok := byID[id]; ok {
			out = append(out, h)
		}
	}
	return out, nil
}

// liveSQL hides superseded decisions and expired memories. One argument: now.
const liveSQL = `d.status NOT IN ('superseded', 'expired') AND d.superseded_by = ''
	AND NOT (d.kind = 'decision' AND EXISTS (SELECT 1 FROM links sl WHERE sl.project = d.project AND sl.dst = d.id AND sl.kind = 'supersedes'))
	AND NOT (d.kind = 'memory' AND d.review_by > 0 AND d.review_by < ?)`

const supersededSQL = `(d.status = 'superseded' OR d.superseded_by <> ''
	OR EXISTS (SELECT 1 FROM links sl WHERE sl.project = d.project AND sl.dst = d.id AND sl.kind = 'supersedes'))`

// filterSQL renders the filters as a condition on alias d.
func (ix *Index) filterSQL(f ports.SearchFilters) (string, []any) {
	var w []string
	var a []any
	if k := strings.ToLower(strings.TrimSpace(f.Kind)); k != "" {
		w, a = append(w, "d.kind = ?"), append(a, k)
	}
	if ar := domain.AreaName(strings.TrimSpace(f.Area)); ar != "" {
		w = append(w, "EXISTS (SELECT 1 FROM doc_areas da WHERE da.project = d.project AND da.doc_id = d.id AND da.area = ?)")
		a = append(a, ar)
	}
	if g := canonicalID(f.Goal); g != "" {
		w = append(w, "(d.goal = ? OR d.id = ? OR EXISTS (SELECT 1 FROM links gl WHERE gl.project = d.project AND gl.src = d.id AND gl.dst = ? AND gl.kind = 'goal'))")
		a = append(a, g, g, g)
	}
	if !f.Since.IsZero() {
		w, a = append(w, "d.updated >= ?"), append(a, f.Since.UTC().UnixNano())
	}
	now := ix.now().UTC().UnixNano()
	switch st := strings.ToLower(strings.TrimSpace(f.Status)); st {
	case "all":
	case "superseded":
		w = append(w, supersededSQL)
	case "expired":
		w = append(w, "(d.status = 'expired' OR (d.kind = 'memory' AND d.review_by > 0 AND d.review_by < ?))")
		a = append(a, now)
	case "":
		w, a = append(w, liveSQL), append(a, now)
	default:
		w = append(w, "d.status = ?", liveSQL)
		a = append(a, st, now)
	}
	if len(w) == 0 {
		return "1=1", nil
	}
	return strings.Join(w, " AND "), a
}

var (
	reShortID = regexp.MustCompile(`(?i)\b[GIPDMQ]-\d+\b`)
	reAreaID  = regexp.MustCompile(`\bA-[A-Za-z0-9_][A-Za-z0-9_-]*`)
	reIdent   = regexp.MustCompile(`^[A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)*$`)
	reLineRef = regexp.MustCompile(`:\d+(?::\d+)?$`)
)

func looksLikePath(s string) bool {
	return strings.Contains(s, "/") || langOf(s) != "" || strings.HasSuffix(strings.ToLower(s), ".md")
}

// exactLayer finds documents by id, path and symbol name.
func (ix *Index) exactLayer(ctx context.Context, project, q, fsql string, fargs []any, limit int) ([]string, error) {
	if q == "" {
		return nil, nil
	}
	var cand []string
	seen := map[string]bool{}
	add := func(ids ...string) {
		for _, id := range ids {
			if id != "" && !seen[id] {
				seen[id] = true
				cand = append(cand, id)
			}
		}
	}
	add(q, canonicalID(q))
	for _, m := range reShortID.FindAllString(q, -1) {
		add(canonicalID(m))
	}
	for _, m := range reAreaID.FindAllString(q, -1) {
		add(m)
	}
	var paths, idents, dirs []string
	for _, tok := range strings.Fields(q) {
		tok = strings.Trim(tok, "\"'`()[]{},;")
		if tok == "" {
			continue
		}
		add(tok, canonicalID(tok))
		if looksLikePath(tok) {
			p := strings.TrimPrefix(reLineRef.ReplaceAllString(tok, ""), "./")
			add("file:" + p)
			paths = append(paths, p)
			if strings.HasSuffix(p, "/") {
				dirs = append(dirs, p)
			}
		}
		if reIdent.MatchString(tok) {
			idents = append(idents, tok)
		}
	}
	if len(paths) > 0 {
		ids, err := ix.column(ctx, `SELECT id FROM docs WHERE project = ? AND path IN (`+placeholders(len(paths))+`) AND kind NOT IN ('symbol', 'file') ORDER BY id`,
			append([]any{project}, anySlice(paths)...)...)
		if err != nil {
			return nil, err
		}
		add(ids...)
	}
	for _, d := range dirs {
		ids, err := ix.column(ctx, `SELECT id FROM docs WHERE project = ? AND kind = 'file' AND path >= ? AND path < ? ORDER BY path LIMIT ?`,
			project, d, d+"￿", limit)
		if err != nil {
			return nil, err
		}
		add(ids...)
	}
	if len(idents) > 0 {
		ids, err := ix.column(ctx, `SELECT doc_id FROM symbols WHERE project = ? AND (name IN (`+placeholders(len(idents))+`) OR short IN (`+placeholders(len(idents))+`))
			ORDER BY CASE WHEN name IN (`+placeholders(len(idents))+`) THEN 0 ELSE 1 END, file, line LIMIT ?`,
			append(append(append(append([]any{project}, anySlice(idents)...), anySlice(idents)...), anySlice(idents)...), limit)...)
		if err != nil {
			return nil, err
		}
		add(ids...)
	}
	if len(cand) == 0 {
		return nil, nil
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

// filterLayer lists documents matching the filters, newest first. Code
// symbols and files only appear when the kind filter asks for them.
func (ix *Index) filterLayer(ctx context.Context, project string, f ports.SearchFilters, fsql string, fargs []any, limit int) ([]string, error) {
	extra := ""
	if f.Kind == "" {
		extra = " AND d.kind NOT IN ('symbol', 'file')"
	}
	return ix.column(ctx, `SELECT id FROM docs d WHERE d.project = ? AND `+fsql+extra+` ORDER BY d.updated DESC, d.id LIMIT ?`,
		append(append([]any{project}, fargs...), limit)...)
}

var reWord = regexp.MustCompile(`[\p{L}\p{N}]+`)

// ftsQuery quotes every word so user text can never inject FTS5 syntax.
// With op " OR " single-character words are dropped (too common to rank).
func ftsQuery(q string, op string) string {
	var words []string
	for _, w := range reWord.FindAllString(q, -1) {
		if op == " OR " && len([]rune(w)) < 2 {
			continue
		}
		words = append(words, `"`+w+`"`)
	}
	return strings.Join(words, op)
}

// textLayer runs bm25 over the chunks: all words first, then any word when
// that finds too little.
func (ix *Index) textLayer(ctx context.Context, project, q, fsql string, fargs []any, limit int) ([]string, error) {
	// ids were handled by the exact layer; their letters and numbers only add noise here
	q = reAreaID.ReplaceAllString(reShortID.ReplaceAllString(q, " "), " ")
	and := ftsQuery(q, " ")
	if and == "" {
		return nil, nil
	}
	run := func(match string) ([]string, error) {
		// bm25 must run on a plain FTS scan, so the matches are materialized
		// first (bounded) and joined to the documents afterwards.
		return ix.column(ctx, `WITH m AS MATERIALIZED (
				SELECT rowid AS rid, bm25(docs_fts, 10.0, 4.0, 1.0) AS s FROM docs_fts WHERE docs_fts MATCH ? ORDER BY s LIMIT 5000)
			SELECT c.doc_id FROM m CROSS JOIN chunks c ON c.id = m.rid
			JOIN docs d ON d.project = c.project AND d.id = c.doc_id
			WHERE c.project = ? AND `+fsql+`
			GROUP BY c.doc_id ORDER BY MIN(m.s), c.doc_id LIMIT ?`,
			append(append([]any{match, project}, fargs...), limit)...)
	}
	out, err := run(and)
	if err != nil {
		return nil, err
	}
	if or := ftsQuery(q, " OR "); len(out) < limit && or != and && len(reWord.FindAllString(q, -1)) > 1 {
		more, err := run(or)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, id := range out {
			seen[id] = true
		}
		for _, id := range more {
			if !seen[id] && len(out) < limit {
				out = append(out, id)
			}
		}
	}
	return out, nil
}

// Get returns a full document by id; also accepts a file path, a symbol
// name ("PaymentService.charge" or "charge") or "sym:<file>#<Name>".
func (ix *Index) Get(ctx context.Context, project, id string) (*ports.Doc, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, ErrNotFound
	}
	tries := []string{id, canonicalID(id)}
	if looksLikePath(id) {
		p := strings.TrimPrefix(reLineRef.ReplaceAllString(id, ""), "./")
		tries = append(tries, "file:"+p)
		ids, err := ix.column(ctx, `SELECT id FROM docs WHERE project = ? AND path = ? AND kind NOT IN ('symbol', 'file') ORDER BY id LIMIT 1`, project, p)
		if err != nil {
			return nil, err
		}
		tries = append(tries, ids...)
	}
	if reIdent.MatchString(id) {
		ids, err := ix.column(ctx, `SELECT doc_id FROM symbols WHERE project = ?1 AND (name = ?2 OR short = ?2)
			ORDER BY CASE WHEN name = ?2 THEN 0 ELSE 1 END, file, line LIMIT 1`, project, id)
		if err != nil {
			return nil, err
		}
		tries = append(tries, ids...)
	}
	for _, t := range tries {
		d, err := ix.loadDoc(ctx, project, t)
		if err != nil {
			return nil, err
		}
		if d != nil {
			return d, nil
		}
	}
	return nil, ErrNotFound
}

func (ix *Index) loadDoc(ctx context.Context, project, id string) (*ports.Doc, error) {
	var d ports.Doc
	var summary, areas, meta, commit, superseded string
	var updated, review int64
	err := ix.db.QueryRowContext(ctx, `SELECT id, kind, title, summary, body, path, status, areas, meta, updated, review_by, source_sha, commit_sha, superseded_by
		FROM docs WHERE project = ? AND id = ?`, project, id).Scan(&d.ID, &d.Kind, &d.Title, &summary, &d.Body, &d.Path, &d.Status,
		&areas, &meta, &updated, &review, &d.SourceSHA, &commit, &superseded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var as []string
	json.Unmarshal([]byte(areas), &as)
	for _, a := range as {
		d.Areas = append(d.Areas, domain.AreaID(a))
	}
	d.Meta = map[string]string{}
	json.Unmarshal([]byte(meta), &d.Meta)
	d.Meta["summary"] = summary
	if commit != "" {
		d.Meta["commit"] = commit
	}
	if superseded != "" {
		d.Meta["superseded_by"] = superseded
	}
	d.Updated, d.ReviewBy = timeOrZero(updated), timeOrZero(review)
	links, err := ix.column(ctx, `SELECT DISTINCT dst FROM links WHERE project = ? AND src = ? ORDER BY kind, dst`, project, id)
	if err != nil {
		return nil, err
	}
	d.Links = links
	if d.Kind == "symbol" {
		if callers := (graph{ix: ix, project: project, scoped: true}).Callers(d.Title); len(callers) > 0 {
			if len(callers) > 20 {
				callers = callers[:20]
			}
			d.Meta["callers"] = strings.Join(callers, ", ")
		}
	}
	if len(d.Meta) == 0 {
		d.Meta = nil
	}
	return &d, nil
}

// Links returns the edges going out of and into a document. A symbol also
// gets its one-level call edges; a file id also matches raw path anchors.
func (ix *Index) Links(ctx context.Context, project, id string) ([]ports.Link, error) {
	id = canonicalID(id)
	aliases := []string{id}
	symName := ""
	switch {
	case strings.HasPrefix(id, "sym:"):
		if i := strings.LastIndexByte(id, '#'); i >= 0 {
			symName = stripLine(id[i+1:])
			aliases = append(aliases, symName)
		}
	case strings.HasPrefix(id, "file:"):
		aliases = append(aliases, strings.TrimPrefix(id, "file:"))
	case looksLikePath(id):
		aliases = append(aliases, "file:"+id)
	}
	ph := placeholders(len(aliases))
	args := append(append([]any{project}, anySlice(aliases)...), anySlice(aliases)...)
	rows, err := ix.db.QueryContext(ctx, `SELECT src, dst, kind FROM links WHERE project = ? AND (src IN (`+ph+`) OR dst IN (`+ph+`))
		ORDER BY CASE WHEN src IN (`+ph+`) THEN 0 ELSE 1 END, kind, src, dst`, append(args, anySlice(aliases)...)...)
	if err != nil {
		return nil, err
	}
	var out []ports.Link
	seen := map[ports.Link]bool{}
	for rows.Next() {
		var l ports.Link
		if err := rows.Scan(&l.From, &l.To, &l.Kind); err != nil {
			rows.Close()
			return nil, err
		}
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if symName != "" {
		callers, err := ix.column(ctx, `SELECT DISTINCT s.doc_id FROM calls c JOIN symbols s ON s.project = c.project AND s.file = c.file AND s.name = c.caller
			WHERE c.project = ? AND c.callee = ? AND c.caller <> ? ORDER BY s.doc_id LIMIT 50`, project, shortName(symName), symName)
		if err != nil {
			return nil, err
		}
		for _, c := range callers {
			out = append(out, ports.Link{From: c, To: id, Kind: "calls"})
		}
		file := strings.TrimPrefix(id[:strings.LastIndexByte(id, '#')], "sym:")
		callees, err := ix.column(ctx, `SELECT DISTINCT s.doc_id FROM calls c JOIN symbols s ON s.project = c.project AND s.short = c.callee
			WHERE c.project = ? AND c.file = ? AND c.caller = ? ORDER BY s.doc_id LIMIT 50`, project, file, symName)
		if err != nil {
			return nil, err
		}
		for _, c := range callees {
			if c != id {
				out = append(out, ports.Link{From: id, To: c, Kind: "calls"})
			}
		}
	}
	return out, nil
}

func stripLine(name string) string {
	if i := strings.LastIndexByte(name, '@'); i > 0 {
		if _, err := strconv.Atoi(name[i+1:]); err == nil {
			return name[:i]
		}
	}
	return name
}

// Symbols returns the code map of a file (every symbol of the project when
// file is empty), in line order.
func (ix *Index) Symbols(ctx context.Context, project, file string) ([]ports.Symbol, error) {
	q := `SELECT name, kind, file, line, signature FROM symbols WHERE project = ?`
	args := []any{project}
	if file != "" {
		q += ` AND file = ?`
		args = append(args, strings.TrimPrefix(file, "./"))
	}
	rows, err := ix.db.QueryContext(ctx, q+` ORDER BY file, line`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ports.Symbol
	for rows.Next() {
		var s ports.Symbol
		if err := rows.Scan(&s.Name, &s.Kind, &s.File, &s.Line, &s.Signature); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Areas returns area → path patterns, from .barnraise/areas.yaml.
func (ix *Index) Areas(project string) map[string][]string {
	areas, err := ix.loadAreas(context.Background(), project)
	if err != nil {
		return nil
	}
	out := make(map[string][]string, len(areas))
	for n, s := range areas {
		out[n] = append([]string{}, s.Paths...)
	}
	return out
}

// AreasOf returns the areas a file or folder belongs to.
func (ix *Index) AreasOf(project, p string) []string {
	areas, err := ix.loadAreas(context.Background(), project)
	if err != nil {
		return nil
	}
	return areasFor(areas, strings.TrimPrefix(reLineRef.ReplaceAllString(p, ""), "./"))
}

// Rules returns what applies to a path, in a fixed order: the
// constitution, the standards of the areas whose paths contain it, then the
// live decisions of those areas.
func (ix *Index) Rules(project, p string) ([]ports.Doc, error) {
	ctx := context.Background()
	p = strings.TrimPrefix(reLineRef.ReplaceAllString(strings.TrimSpace(p), ""), "./")
	areas, err := ix.loadAreas(ctx, project)
	if err != nil {
		return nil, err
	}
	matched := areasFor(areas, p)
	ids := []string{"constitution"}
	for _, a := range matched {
		ids = append(ids, standardID(a, areas[a]))
	}
	if len(matched) > 0 {
		fsql, fargs := ix.filterSQL(ports.SearchFilters{})
		dec, err := ix.column(ctx, `SELECT DISTINCT d.id FROM docs d JOIN doc_areas da ON da.project = d.project AND da.doc_id = d.id
			WHERE d.project = ? AND d.kind = 'decision' AND da.area IN (`+placeholders(len(matched))+`) AND `+fsql,
			append(append([]any{project}, anySlice(matched)...), fargs...)...)
		if err != nil {
			return nil, err
		}
		sort.Slice(dec, func(i, j int) bool { return idLess(dec[i], dec[j]) })
		ids = append(ids, dec...)
	}
	var out []ports.Doc
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		d, err := ix.loadDoc(ctx, project, id)
		if err != nil {
			return nil, err
		}
		if d != nil {
			out = append(out, *d)
		}
	}
	return out, nil
}

func idLess(a, b string) bool {
	_, na, ea := domain.ParseID(a)
	_, nb, eb := domain.ParseID(b)
	if ea == nil && eb == nil && na != nb {
		return na < nb
	}
	return a < b
}

// graph is a domain.CallGraph over the symbols and calls tables.
type graph struct {
	ix      *Index
	project string
	scoped  bool
}

// Callers returns the symbols whose body calls the short name of symbol
// (one level, best effort: calls are matched by name, not by type).
func (g graph) Callers(symbol string) []string {
	name := symbol
	if strings.HasPrefix(name, "sym:") {
		if i := strings.LastIndexByte(name, '#'); i >= 0 {
			name = stripLine(name[i+1:])
		}
	}
	q := `SELECT DISTINCT caller FROM calls WHERE callee = ? AND caller <> ?`
	args := []any{shortName(name), name}
	if g.scoped {
		q += ` AND project = ?`
		args = append(args, g.project)
	}
	out, _ := g.ix.column(context.Background(), q+` ORDER BY caller`, args...)
	return out
}

// SymbolsInFile returns the symbol names declared in a file.
func (g graph) SymbolsInFile(file string) []string {
	q := `SELECT name FROM symbols WHERE file = ?`
	args := []any{strings.TrimPrefix(file, "./")}
	if g.scoped {
		q += ` AND project = ?`
		args = append(args, g.project)
	}
	out, _ := g.ix.column(context.Background(), q+` ORDER BY line`, args...)
	return domain.Dedupe(out)
}

// column runs a query returning one string column.
func (ix *Index) column(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := ix.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return "?" + strings.Repeat(", ?", n-1)
}

func anySlice(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}
