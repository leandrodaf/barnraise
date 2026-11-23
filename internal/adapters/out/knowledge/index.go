// Package knowledge indexes what Barnraise knows for cheap, exact-first
// retrieval: the .barnraise/ documents (YAML front matter + first-line
// summary), the code map (symbols, signatures, one-level calls) and the
// items and proposals of the log. Everything lives in SQLite tables next to
// the event store; text search uses FTS5 with an accent-folding tokenizer.
//
// Search is layered from cheap to expensive: exact (ids, paths, symbol
// names), filters (kind, area, status, goal, date) and full text (bm25).
// The layer rankings are fused with domain.RRF.
package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// ErrNotFound is returned by Get when no document matches.
var ErrNotFound = errors.New("knowledge: not found")

// Index implements ports.Knowledge on a *sql.DB. It may share the database
// of the event store; it only touches its own tables.
type Index struct {
	db  *sql.DB
	wmu sync.Mutex // one writer at a time inside this process
	now func() time.Time
}

var _ ports.Knowledge = (*Index)(nil)

// New creates the knowledge tables (if needed) and returns the index.
func New(db *sql.DB) (*Index, error) {
	ix := &Index{db: db, now: time.Now}
	if err := ix.migrate(context.Background()); err != nil {
		return nil, err
	}
	return ix, nil
}

// SetClock replaces the time source used to expire memories (tests).
func (ix *Index) SetClock(now func() time.Time) { ix.now = now }

var migrations = []string{
	// 1: initial schema.
	`
CREATE TABLE docs (
	project       TEXT NOT NULL,
	id            TEXT NOT NULL,
	kind          TEXT NOT NULL,
	title         TEXT NOT NULL DEFAULT '',
	summary       TEXT NOT NULL DEFAULT '',
	body          TEXT NOT NULL DEFAULT '',
	path          TEXT NOT NULL DEFAULT '',
	status        TEXT NOT NULL DEFAULT '',
	area          TEXT NOT NULL DEFAULT '',
	areas         TEXT NOT NULL DEFAULT '[]',
	goal          TEXT NOT NULL DEFAULT '',
	meta          TEXT NOT NULL DEFAULT '{}',
	updated       INTEGER NOT NULL DEFAULT 0,
	review_by     INTEGER NOT NULL DEFAULT 0,
	source_sha    TEXT NOT NULL DEFAULT '',
	commit_sha    TEXT NOT NULL DEFAULT '',
	superseded_by TEXT NOT NULL DEFAULT '',
	tokens        INTEGER NOT NULL DEFAULT 0,
	origin        TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (project, id)
);
CREATE INDEX docs_origin ON docs(project, origin);
CREATE INDEX docs_path ON docs(project, path);
CREATE INDEX docs_kind ON docs(project, kind, updated);
CREATE INDEX docs_id_nocase ON docs(project, id COLLATE NOCASE);

CREATE TABLE doc_areas (
	project TEXT NOT NULL,
	doc_id  TEXT NOT NULL,
	area    TEXT NOT NULL,
	PRIMARY KEY (project, area, doc_id)
);
CREATE INDEX doc_areas_doc ON doc_areas(project, doc_id);

CREATE TABLE chunks (
	id      INTEGER PRIMARY KEY,
	project TEXT NOT NULL,
	doc_id  TEXT NOT NULL,
	heading TEXT NOT NULL DEFAULT '',
	line    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX chunks_doc ON chunks(project, doc_id);

CREATE VIRTUAL TABLE docs_fts USING fts5(title, summary, body, tokenize = 'unicode61 remove_diacritics 2');

CREATE TABLE links (
	project TEXT NOT NULL,
	src     TEXT NOT NULL,
	dst     TEXT NOT NULL,
	kind    TEXT NOT NULL,
	origin  TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (project, src, dst, kind)
);
CREATE INDEX links_dst ON links(project, dst, kind);
CREATE INDEX links_origin ON links(project, origin);

CREATE TABLE symbols (
	project    TEXT NOT NULL,
	file       TEXT NOT NULL,
	name       TEXT NOT NULL,
	short      TEXT NOT NULL,
	kind       TEXT NOT NULL,
	line       INTEGER NOT NULL,
	end_line   INTEGER NOT NULL,
	signature  TEXT NOT NULL DEFAULT '',
	doc_id     TEXT NOT NULL,
	commit_sha TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (project, file, name, line)
);
CREATE INDEX symbols_name ON symbols(name);
CREATE INDEX symbols_short ON symbols(short);

CREATE TABLE calls (
	project TEXT NOT NULL,
	file    TEXT NOT NULL,
	caller  TEXT NOT NULL,
	callee  TEXT NOT NULL,
	PRIMARY KEY (project, file, caller, callee)
);
CREATE INDEX calls_callee ON calls(callee, project);

CREATE TABLE file_hashes (
	project    TEXT NOT NULL,
	path       TEXT NOT NULL,
	sha        TEXT NOT NULL,
	commit_sha TEXT NOT NULL DEFAULT '',
	indexed    INTEGER NOT NULL,
	PRIMARY KEY (project, path)
);

CREATE TABLE areas (
	project   TEXT NOT NULL,
	area      TEXT NOT NULL,
	paths     TEXT NOT NULL DEFAULT '[]',
	screens   TEXT NOT NULL DEFAULT '[]',
	contracts TEXT NOT NULL DEFAULT '[]',
	standard  TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (project, area)
);

CREATE TABLE index_state (
	project    TEXT PRIMARY KEY,
	commit_sha TEXT NOT NULL DEFAULT '',
	indexed    INTEGER NOT NULL
);
`,
}

func (ix *Index) migrate(ctx context.Context) error {
	if _, err := ix.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS knowledge_migrations (version INTEGER PRIMARY KEY, applied INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("knowledge migrations: %w", err)
	}
	for i, m := range migrations {
		v := i + 1
		tx, err := ix.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		var done int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_migrations WHERE version = ?`, v).Scan(&done); err != nil {
			tx.Rollback()
			return err
		}
		if done > 0 {
			tx.Rollback()
			continue
		}
		if _, err := tx.ExecContext(ctx, m); err != nil {
			tx.Rollback()
			return fmt.Errorf("knowledge migration %d: %w", v, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_migrations(version, applied) VALUES(?, ?)`, v, time.Now().Unix()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// IndexedCommit returns the commit of the last Reindex of a project.
func (ix *Index) IndexedCommit(ctx context.Context, project string) (string, time.Time, error) {
	var c string
	var at int64
	err := ix.db.QueryRowContext(ctx, `SELECT commit_sha, indexed FROM index_state WHERE project = ?`, project).Scan(&c, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, nil
	}
	return c, time.Unix(0, at).UTC(), err
}

// write runs fn in a transaction under the writer mutex.
func (ix *Index) write(ctx context.Context, fn func(tx *txw) error) error {
	ix.wmu.Lock()
	defer ix.wmu.Unlock()
	sqlTx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	tx := &txw{tx: sqlTx, stmts: map[string]*sql.Stmt{}}
	defer tx.close()
	if err := fn(tx); err != nil {
		sqlTx.Rollback()
		return err
	}
	return sqlTx.Commit()
}

// txw is a write transaction that prepares each statement once: a reindex
// runs the same few statements thousands of times.
type txw struct {
	tx    *sql.Tx
	stmts map[string]*sql.Stmt
}

func (t *txw) exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	st, ok := t.stmts[q]
	if !ok {
		var err error
		if st, err = t.tx.PrepareContext(ctx, q); err != nil {
			return nil, err
		}
		t.stmts[q] = st
	}
	return st.ExecContext(ctx, args...)
}

func (t *txw) query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, q, args...)
}

func (t *txw) close() {
	for _, st := range t.stmts {
		st.Close()
	}
}

// Graph returns a call graph across every project, keyed by symbol name.
// Use GraphFor to scope it to one project.
func (ix *Index) Graph() domain.CallGraph { return graph{ix: ix} }

// GraphFor returns the call graph of one project.
func (ix *Index) GraphFor(project string) domain.CallGraph {
	return graph{ix: ix, project: project, scoped: true}
}
