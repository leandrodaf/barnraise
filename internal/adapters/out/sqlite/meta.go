package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// KV is a small key/value table for daemon metadata (last synced seq,
// install id, and the like).
type KV struct{ s *Store }

// KV returns the metadata table.
func (s *Store) KV() *KV { return s.kv }

// Get returns the value of key and whether it exists.
func (k *KV) Get(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := k.s.db.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// Set stores value under key, replacing any previous value.
func (k *KV) Set(ctx context.Context, key, value string) error {
	return k.s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO kv(key, value, updated) VALUES(?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated = excluded.updated`,
			key, value, time.Now().UTC().UnixNano())
		return err
	})
}

// Delete removes key; deleting a missing key is not an error.
func (k *KV) Delete(ctx context.Context, key string) error {
	return k.s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM kv WHERE key = ?`, key)
		return err
	})
}

// Token is an API token. Only the hash of the secret is stored.
type Token struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Hash    string    `json:"-"`
	Project string    `json:"project,omitempty"` // empty: every project
	Scopes  []string  `json:"scopes"`
	Expires time.Time `json:"expires"` // zero: never
	Revoked bool      `json:"revoked"`
	Created time.Time `json:"created"`
}

// Valid reports whether the token can be used at now.
func (t *Token) Valid(now time.Time) bool {
	return !t.Revoked && (t.Expires.IsZero() || now.Before(t.Expires))
}

// HasScope reports whether the token grants scope ("*" grants all).
func (t *Token) HasScope(scope string) bool {
	for _, s := range t.Scopes {
		if s == scope || s == "*" {
			return true
		}
	}
	return false
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

// CreateToken stores a new token. Created defaults to now.
func (s *Store) CreateToken(ctx context.Context, t Token) error {
	if t.Created.IsZero() {
		t.Created = time.Now().UTC()
	}
	if t.Scopes == nil {
		t.Scopes = []string{}
	}
	scopes, _ := json.Marshal(t.Scopes)
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO tokens(id, name, hash, project, scopes, expires, revoked, created)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, t.ID, t.Name, t.Hash, t.Project, string(scopes),
			unixOrZero(t.Expires), boolInt(t.Revoked), unixOrZero(t.Created))
		return err
	})
}

const tokenCols = `id, name, hash, project, scopes, expires, revoked, created`

func scanToken(sc interface{ Scan(...any) error }) (*Token, error) {
	var t Token
	var scopes string
	var exp, created int64
	var revoked int
	if err := sc.Scan(&t.ID, &t.Name, &t.Hash, &t.Project, &scopes, &exp, &revoked, &created); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(scopes), &t.Scopes)
	t.Expires, t.Created, t.Revoked = timeOrZero(exp), timeOrZero(created), revoked != 0
	return &t, nil
}

// GetTokenByHash finds a token by the hash of its secret (revoked or not;
// check Valid). Returns ErrNotFound when missing.
func (s *Store) GetTokenByHash(ctx context.Context, hash string) (*Token, error) {
	t, err := scanToken(s.db.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM tokens WHERE hash = ?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// ListTokens returns all tokens, oldest first.
func (s *Store) ListTokens(ctx context.Context) ([]*Token, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tokenCols+` FROM tokens ORDER BY created, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeToken marks a token revoked. Returns ErrNotFound when missing.
func (s *Store) RevokeToken(ctx context.Context, id string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE tokens SET revoked = 1 WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// AuditEntry is one line of the audit trail.
type AuditEntry struct {
	Seq     int64     `json:"seq"`
	Time    time.Time `json:"time"`
	Actor   string    `json:"actor"`
	TokenID string    `json:"token_id,omitempty"`
	Action  string    `json:"action"`
	Target  string    `json:"target,omitempty"`
	Project string    `json:"project,omitempty"`
	Detail  string    `json:"detail,omitempty"`
}

// AppendAudit records an entry. Time defaults to now.
func (s *Store) AppendAudit(ctx context.Context, a AuditEntry) error {
	if a.Time.IsZero() {
		a.Time = time.Now().UTC()
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO audit(time, actor, token_id, action, target, project, detail)
			VALUES(?, ?, ?, ?, ?, ?, ?)`, a.Time.UTC().UnixNano(), a.Actor, a.TokenID, a.Action, a.Target, a.Project, a.Detail)
		return err
	})
}

// AuditQuery filters the audit trail.
type AuditQuery struct {
	Project string
	Actor   string
	Since   time.Time
	Limit   int // most recent N, still returned oldest first
}

// ListAudit returns matching entries in time order.
func (s *Store) ListAudit(ctx context.Context, q AuditQuery) ([]AuditEntry, error) {
	sqlq := `SELECT seq, time, actor, token_id, action, target, project, detail FROM audit WHERE 1=1`
	var args []any
	if q.Project != "" {
		sqlq += ` AND project = ?`
		args = append(args, q.Project)
	}
	if q.Actor != "" {
		sqlq += ` AND actor = ?`
		args = append(args, q.Actor)
	}
	if !q.Since.IsZero() {
		sqlq += ` AND time >= ?`
		args = append(args, q.Since.UTC().UnixNano())
	}
	sqlq += ` ORDER BY seq DESC`
	if q.Limit > 0 {
		sqlq += ` LIMIT ?`
		args = append(args, q.Limit)
	}
	rows, err := s.db.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var a AuditEntry
		var t int64
		if err := rows.Scan(&a.Seq, &t, &a.Actor, &a.TokenID, &a.Action, &a.Target, &a.Project, &a.Detail); err != nil {
			return nil, err
		}
		a.Time = time.Unix(0, t).UTC()
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
