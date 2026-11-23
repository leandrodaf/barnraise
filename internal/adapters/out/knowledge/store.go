package knowledge

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// docRow is one document as stored.
type docRow struct {
	ID           string
	Kind         string
	Title        string
	Summary      string
	Body         string
	Path         string
	Status       string
	Areas        []string // area names without the A- prefix
	Goal         string
	Meta         map[string]string
	Updated      time.Time
	ReviewBy     time.Time
	SourceSHA    string
	Commit       string
	SupersededBy string
	Origin       string // file that produced it, or "items"
	Links        []linkRow
	Chunks       []chunk
}

type linkRow struct {
	Dst, Kind string
}

// chunk is a searchable piece of a document: a heading section or a symbol.
type chunk struct {
	Heading string
	Line    int
	Text    string
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().UnixNano()
}

func timeOrZero(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

// putDoc replaces a document, its areas, chunks and outgoing links.
func putDoc(ctx context.Context, tx *txw, project string, d *docRow) error {
	return putDocFresh(ctx, tx, project, d, false)
}

// putDocFresh skips the delete when the caller already dropped the origin
// and the id cannot belong to another origin (code ids embed the path).
func putDocFresh(ctx context.Context, tx *txw, project string, d *docRow, fresh bool) error {
	if !fresh {
		if err := dropDoc(ctx, tx, project, d.ID); err != nil {
			return err
		}
	}
	areas := normAreas(d.Areas)
	area := ""
	if len(areas) > 0 {
		area = areas[0]
	}
	aj, _ := json.Marshal(areas)
	if d.Meta == nil {
		d.Meta = map[string]string{}
	}
	mj, _ := json.Marshal(d.Meta)
	if _, err := tx.exec(ctx, `INSERT INTO docs(project, id, kind, title, summary, body, path, status, area, areas, goal, meta,
		updated, review_by, source_sha, commit_sha, superseded_by, tokens, origin)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		project, d.ID, d.Kind, d.Title, d.Summary, d.Body, d.Path, strings.ToLower(d.Status), area, string(aj), d.Goal, string(mj),
		unixOrZero(d.Updated), unixOrZero(d.ReviewBy), d.SourceSHA, d.Commit, d.SupersededBy, domain.EstimateTokens(d.Body), d.Origin); err != nil {
		return err
	}
	for _, a := range areas {
		if _, err := tx.exec(ctx, `INSERT OR IGNORE INTO doc_areas(project, doc_id, area) VALUES(?, ?, ?)`, project, d.ID, a); err != nil {
			return err
		}
	}
	for _, l := range d.Links {
		if l.Dst == "" || l.Dst == d.ID {
			continue
		}
		if _, err := tx.exec(ctx, `INSERT OR IGNORE INTO links(project, src, dst, kind, origin) VALUES(?, ?, ?, ?, ?)`,
			project, d.ID, l.Dst, l.Kind, d.Origin); err != nil {
			return err
		}
	}
	chunks := d.Chunks
	if len(chunks) == 0 {
		chunks = []chunk{{Text: d.Body}}
	}
	for i, c := range chunks {
		res, err := tx.exec(ctx, `INSERT INTO chunks(project, doc_id, heading, line) VALUES(?, ?, ?, ?)`, project, d.ID, c.Heading, c.Line)
		if err != nil {
			return err
		}
		rowid, _ := res.LastInsertId()
		title := d.Title
		if c.Heading != "" && c.Heading != d.Title {
			title += " " + c.Heading
		}
		summary := ""
		if i == 0 {
			summary = d.Summary
		}
		if _, err := tx.exec(ctx, `INSERT INTO docs_fts(rowid, title, summary, body) VALUES(?, ?, ?, ?)`,
			rowid, title+" "+splitIdent(d.Title), summary, c.Text); err != nil {
			return err
		}
	}
	return nil
}

// dropDoc removes a document with its chunks, areas and outgoing links.
func dropDoc(ctx context.Context, tx *txw, project, id string) error {
	if err := dropFTS(ctx, tx, `SELECT id FROM chunks WHERE project = ?1 AND doc_id = ?2`, project, id); err != nil {
		return err
	}
	stmts := []string{
		`DELETE FROM chunks WHERE project = ?1 AND doc_id = ?2`,
		`DELETE FROM doc_areas WHERE project = ?1 AND doc_id = ?2`,
		`DELETE FROM links WHERE project = ?1 AND src = ?2`,
		`DELETE FROM docs WHERE project = ?1 AND id = ?2`,
	}
	for _, s := range stmts {
		if _, err := tx.exec(ctx, s, project, id); err != nil {
			return err
		}
	}
	return nil
}

// dropOrigin removes everything a source file produced.
func dropOrigin(ctx context.Context, tx *txw, project, origin string) error {
	if err := dropFTS(ctx, tx, `SELECT c.id FROM docs d CROSS JOIN chunks c ON c.project = d.project AND c.doc_id = d.id
		WHERE d.project = ?1 AND d.origin = ?2`, project, origin); err != nil {
		return err
	}
	stmts := []string{
		`DELETE FROM chunks WHERE project = ?1 AND doc_id IN (SELECT id FROM docs WHERE project = ?1 AND origin = ?2)`,
		`DELETE FROM doc_areas WHERE project = ?1 AND doc_id IN (SELECT id FROM docs WHERE project = ?1 AND origin = ?2)`,
		`DELETE FROM links WHERE project = ?1 AND origin = ?2`,
		`DELETE FROM docs WHERE project = ?1 AND origin = ?2`,
		`DELETE FROM symbols WHERE project = ?1 AND file = ?2`,
		`DELETE FROM calls WHERE project = ?1 AND file = ?2`,
	}
	for _, s := range stmts {
		if _, err := tx.exec(ctx, s, project, origin); err != nil {
			return err
		}
	}
	return nil
}

// dropFTS deletes FTS rows one rowid at a time: a "rowid IN (subquery)"
// delete makes FTS5 scan the whole table.
func dropFTS(ctx context.Context, tx *txw, q string, args ...any) error {
	rows, err := tx.query(ctx, q, args...)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.exec(ctx, `DELETE FROM docs_fts WHERE rowid = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

func normAreas(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, a := range in {
		a = strings.TrimSpace(domain.AreaName(strings.TrimSpace(a)))
		if a != "" && !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}

// splitIdent turns "PaymentService.chargeCard" into "Payment Service charge
// Card" so text search finds identifiers by their words.
func splitIdent(s string) string {
	var b strings.Builder
	var prev rune
	for i, r := range s {
		switch {
		case r == '.' || r == '_' || r == '#' || r == '/' || r == ':' || r == '-':
			b.WriteByte(' ')
		case i > 0 && unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsDigit(prev)):
			b.WriteByte(' ')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
		prev = r
	}
	return b.String()
}
