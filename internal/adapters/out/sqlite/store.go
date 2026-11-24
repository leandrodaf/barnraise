// Package sqlite is the local SQLite adapter: the append-only event log
// (ports.EventStore) plus small daemon tables for metadata, API tokens and
// the audit trail. It uses modernc.org/sqlite (pure Go, no cgo).
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Store is the SQLite-backed event log and daemon metadata.
//
// Writes go through a single mutex so only one write transaction of this
// process runs at a time; reads use the pool freely (WAL lets them run
// alongside the writer). Other processes or packages sharing the file are
// serialized by SQLite itself (busy_timeout and immediate transactions).
type Store struct {
	db  *sql.DB
	wmu sync.Mutex
	kv  *KV
}

var _ ports.EventStore = (*Store)(nil)

// Open opens (or creates) the database at path and applies migrations.
// ":memory:" gives a private in-memory database, handy for tests; it uses a
// single connection so every query sees the same data.
func Open(path string) (*Store, error) {
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	s.kv = &KV{s: s}
	if err := migrate(context.Background(), db, "store", storeMigrations); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func openDB(path string) (*sql.DB, error) {
	pragmas := url.Values{}
	pragmas.Add("_pragma", "busy_timeout(5000)")
	pragmas.Add("_pragma", "foreign_keys(1)")
	pragmas.Add("_pragma", "synchronous(NORMAL)")
	pragmas.Set("_txlock", "immediate")
	memory := path == ":memory:" || path == ""
	var dsn string
	if memory {
		dsn = "file::memory:?" + pragmas.Encode()
	} else {
		pragmas.Add("_pragma", "journal_mode(WAL)")
		// The state holds tokens' hashes, the audit trail and item text: only
		// the owner of the daemon reads it. SQLite gives the -wal and -shm
		// files the permissions of the main file.
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return nil, fmt.Errorf("open sqlite: %w", err)
		}
		f.Close()
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("open sqlite: %w", err)
		}
		dsn = "file:" + path + "?" + pragmas.Encode()
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if memory {
		db.SetMaxOpenConns(1)
		db.SetConnMaxLifetime(0)
		db.SetConnMaxIdleTime(0)
	} else {
		db.SetMaxOpenConns(8)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	return db, nil
}

// DB exposes the underlying handle so other adapters (knowledge) can share
// the same file.
func (s *Store) DB() *sql.DB { return s.db }

// WriteLock is the store's writer lock. An adapter that writes to the same
// file takes it too, so writers of this process queue instead of failing
// on SQLite's busy timeout.
func (s *Store) WriteLock() sync.Locker { return &s.wmu }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// write runs fn inside a write transaction under the writer mutex.
func (s *Store) write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Append stores events in one transaction, assigning Seq. Events whose id is
// already stored are skipped silently; only the newly stored events are
// returned, with Seq set. Events without an id are always stored.
func (s *Store) Append(ctx context.Context, events ...domain.Event) ([]domain.Event, error) {
	if len(events) == 0 {
		return nil, nil
	}
	out := make([]domain.Event, 0, len(events))
	err := s.write(ctx, func(tx *sql.Tx) error {
		st, err := tx.PrepareContext(ctx, `INSERT INTO events(id, project, type, subject, machine, actor, time, data)
			SELECT ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8
			WHERE ?1 IS NULL OR NOT EXISTS (SELECT 1 FROM events WHERE id = ?1)
			RETURNING seq`)
		if err != nil {
			return err
		}
		defer st.Close()
		prev, err := chainTip(ctx, tx)
		if err != nil {
			return err
		}
		for _, e := range events {
			var id any
			if e.ID != "" {
				id = e.ID
			}
			var seq int64
			err := st.QueryRowContext(ctx, id, e.Project, e.Type, e.Subject, e.Machine, e.Actor, e.Time.UTC().UnixNano(), []byte(e.Data)).Scan(&seq)
			if errors.Is(err, sql.ErrNoRows) {
				continue // duplicate id
			}
			if err != nil {
				return fmt.Errorf("append %s: %w", e.ID, err)
			}
			e.Seq = seq
			if prev, err = chainLink(ctx, tx, prev, e); err != nil {
				return fmt.Errorf("append %s: %w", e.ID, err)
			}
			out = append(out, e)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

const eventCols = `seq, COALESCE(id, ''), project, type, subject, machine, actor, time, data`

// Load returns the events of a project (all projects if empty) with seq
// greater than afterSeq, in seq order.
func (s *Store) Load(ctx context.Context, project string, afterSeq int64) ([]domain.Event, error) {
	q := `SELECT ` + eventCols + ` FROM events WHERE seq > ?`
	args := []any{afterSeq}
	if project != "" {
		q += ` AND project = ?`
		args = append(args, project)
	}
	q += ` ORDER BY seq`
	return s.queryEvents(ctx, q, args...)
}

// Query filters the log. Results are always in ascending seq order. When
// Limit > 0 the most recent Limit matching events are taken (newest by seq)
// and still returned oldest first, so callers can fold them in order.
func (s *Store) Query(ctx context.Context, q ports.EventQuery) ([]domain.Event, error) {
	var where []string
	var args []any
	if q.Project != "" {
		where = append(where, "project = ?")
		args = append(args, q.Project)
	}
	if q.Subject != "" {
		where = append(where, "subject = ?")
		args = append(args, q.Subject)
	}
	if len(q.Types) > 0 {
		where = append(where, "type IN (?"+strings.Repeat(",?", len(q.Types)-1)+")")
		for _, t := range q.Types {
			args = append(args, t)
		}
	}
	if !q.Since.IsZero() {
		where = append(where, "time >= ?")
		args = append(args, q.Since.UTC().UnixNano())
	}
	sqlq := `SELECT ` + eventCols + ` FROM events`
	if len(where) > 0 {
		sqlq += " WHERE " + strings.Join(where, " AND ")
	}
	if q.Limit > 0 {
		sqlq += " ORDER BY seq DESC LIMIT ?"
		args = append(args, q.Limit)
	} else {
		sqlq += " ORDER BY seq"
	}
	evs, err := s.queryEvents(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	if q.Limit > 0 {
		for i, j := 0, len(evs)-1; i < j; i, j = i+1, j-1 {
			evs[i], evs[j] = evs[j], evs[i]
		}
	}
	return evs, nil
}

// LastSeq returns the highest seq stored (0 when empty).
func (s *Store) LastSeq(ctx context.Context) (int64, error) {
	var n sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(seq) FROM events`).Scan(&n)
	return n.Int64, err
}

func (s *Store) queryEvents(ctx context.Context, q string, args ...any) ([]domain.Event, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Event
	for rows.Next() {
		var e domain.Event
		var t int64
		var data []byte
		if err := rows.Scan(&e.Seq, &e.ID, &e.Project, &e.Type, &e.Subject, &e.Machine, &e.Actor, &t, &data); err != nil {
			return nil, err
		}
		e.Time = time.Unix(0, t).UTC()
		if len(data) > 0 {
			e.Data = data
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
