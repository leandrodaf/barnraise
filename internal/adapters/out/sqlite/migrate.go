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
	{2, chainMigrationSQL},
	// 3: the files each item's agent sessions read or changed on this
	// machine (the stale-read warning). Local, never replicated.
	{3, `
CREATE TABLE file_reads (
	project TEXT NOT NULL,
	item    TEXT NOT NULL,
	path    TEXT NOT NULL,
	write   INTEGER NOT NULL DEFAULT 0,
	time    INTEGER NOT NULL,
	PRIMARY KEY (project, item, path)
);
`},
}

// migrationCode holds the steps a migration takes in Go after its SQL, in
// the same transaction (component -> version -> step): what SQL alone
// cannot compute, like the hash chain of the events already stored.
var migrationCode = map[string]map[int]func(context.Context, *sql.Tx) error{
	"store": {2: chainExisting},
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
		if fn := migrationCode[component][m.Version]; fn != nil {
			if err := fn(ctx, tx); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %s/%d: %w", component, m.Version, err)
			}
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
