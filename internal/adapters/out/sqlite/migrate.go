package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Migration is one versioned schema step. Versions only grow; a step that
// shipped is never edited, a new one is added instead.
type Migration struct {
	Version int
	SQL     string
}

var storeMigrations = []Migration{
	{1, `
CREATE TABLE events (
	seq     INTEGER PRIMARY KEY AUTOINCREMENT,
	id      TEXT UNIQUE,
	project TEXT NOT NULL,
	type    TEXT NOT NULL,
	subject TEXT NOT NULL DEFAULT '',
	machine TEXT NOT NULL DEFAULT '',
	actor   TEXT NOT NULL DEFAULT '',
	time    INTEGER NOT NULL,
	data    BLOB
);
CREATE INDEX events_project_seq ON events(project, seq);
CREATE INDEX events_subject ON events(project, subject, seq);
CREATE INDEX events_type ON events(project, type, seq);
CREATE INDEX events_time ON events(time);

CREATE TABLE kv (
	key     TEXT PRIMARY KEY,
	value   TEXT NOT NULL,
	updated INTEGER NOT NULL
);

CREATE TABLE tokens (
	id      TEXT PRIMARY KEY,
	name    TEXT NOT NULL,
	hash    TEXT NOT NULL UNIQUE,
	project TEXT NOT NULL DEFAULT '',
	scopes  TEXT NOT NULL DEFAULT '[]',
	expires INTEGER NOT NULL DEFAULT 0,
	revoked INTEGER NOT NULL DEFAULT 0,
	created INTEGER NOT NULL
);

CREATE TABLE audit (
	seq      INTEGER PRIMARY KEY AUTOINCREMENT,
	time     INTEGER NOT NULL,
	actor    TEXT NOT NULL DEFAULT '',
	token_id TEXT NOT NULL DEFAULT '',
	action   TEXT NOT NULL,
	target   TEXT NOT NULL DEFAULT '',
	project  TEXT NOT NULL DEFAULT '',
	detail   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX audit_project_time ON audit(project, time);
CREATE INDEX audit_time ON audit(time);
`},
}

// migrate applies the pending migrations of a component inside one
// transaction each, recording them in schema_migrations.
func migrate(ctx context.Context, db *sql.DB, component string, steps []Migration) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		component TEXT NOT NULL,
		version   INTEGER NOT NULL,
		applied   INTEGER NOT NULL,
		PRIMARY KEY (component, version))`); err != nil {
		return fmt.Errorf("migrations table: %w", err)
	}
	var cur int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations WHERE component = ?`, component).Scan(&cur); err != nil {
		return err
	}
	for _, m := range steps {
		if m.Version <= cur {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		var done int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE component = ? AND version = ?`, component, m.Version).Scan(&done); err != nil {
			tx.Rollback()
			return err
		}
		if done > 0 { // another process applied it meanwhile
			tx.Rollback()
			continue
		}
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s/%d: %w", component, m.Version, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(component, version, applied) VALUES(?, ?, ?)`,
			component, m.Version, time.Now().UTC().Unix()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// SchemaVersion returns the applied migration version of a component.
func (s *Store) SchemaVersion(ctx context.Context, component string) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations WHERE component = ?`, component).Scan(&v)
	return v, err
}
