package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// The hash chain of the event log (ADR-0029). Every stored event carries
// prev_hash (the hash of the event stored before it, "" for the first) and
// hash (domain.ChainHash over prev_hash and its content). Editing, removing
// or inserting a row breaks the chain at that row; VerifyChain finds where.

// chainMigrationSQL is store migration 2: the two columns. The Go step
// (chainExisting) fills them for the rows already stored.
const chainMigrationSQL = `
ALTER TABLE events ADD COLUMN prev_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN hash TEXT NOT NULL DEFAULT '';
`

// KV keys of the chain.
const (
	// kvChainGenesis records the migration: events up to its seq existed
	// before the chain and were hashed by the migration, so the chain vouches
	// for them only from that moment on.
	kvChainGenesis = "events.chain.genesis"
	// KVChainAnchor holds the last signed anchor (domain.ChainAnchor).
	KVChainAnchor = "events.chain.anchor"
)

// chainGenesis is the value under kvChainGenesis.
type chainGenesis struct {
	Seq  int64     `json:"seq"`
	Time time.Time `json:"time"`
}

// chainExisting computes the chain of the events stored before migration 2,
// in seq order, and records the genesis marker.
func chainExisting(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT `+eventCols+` FROM events ORDER BY seq`)
	if err != nil {
		return err
	}
	var evs []domain.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			rows.Close()
			return err
		}
		evs = append(evs, e)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	prev := ""
	var last int64
	for _, e := range evs {
		if prev, err = chainLink(ctx, tx, prev, e); err != nil {
			return err
		}
		last = e.Seq
	}
	g, _ := json.Marshal(chainGenesis{Seq: last, Time: time.Now().UTC()})
	_, err = tx.ExecContext(ctx, `INSERT INTO kv(key, value, updated) VALUES(?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated = excluded.updated`,
		kvChainGenesis, string(g), time.Now().UTC().UnixNano())
	return err
}

// chainTip is the hash of the last stored event ("" for an empty log).
func chainTip(ctx context.Context, tx *sql.Tx) (string, error) {
	var h string
	err := tx.QueryRowContext(ctx, `SELECT hash FROM events ORDER BY seq DESC LIMIT 1`).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return h, err
}

// chainLink writes the links of a stored event and returns its hash.
func chainLink(ctx context.Context, tx *sql.Tx, prev string, e domain.Event) (string, error) {
	h := domain.ChainHash(prev, e)
	if _, err := tx.ExecContext(ctx, `UPDATE events SET prev_hash = ?, hash = ? WHERE seq = ?`, prev, h, e.Seq); err != nil {
		return "", fmt.Errorf("chain: %w", err)
	}
	return h, nil
}

func scanEvent(sc interface{ Scan(...any) error }, extra ...any) (domain.Event, error) {
	var e domain.Event
	var t int64
	var data []byte
	dest := append([]any{&e.Seq, &e.ID, &e.Project, &e.Type, &e.Subject, &e.Machine, &e.Actor, &t, &data}, extra...)
	if err := sc.Scan(dest...); err != nil {
		return e, err
	}
	e.Time = time.Unix(0, t).UTC()
	if len(data) > 0 {
		e.Data = data
	}
	return e, nil
}

// ChainHead returns the seq and hash of the last stored event (0, "" when
// the log is empty).
func (s *Store) ChainHead(ctx context.Context) (int64, string, error) {
	var seq int64
	var h string
	err := s.db.QueryRowContext(ctx, `SELECT seq, hash FROM events ORDER BY seq DESC LIMIT 1`).Scan(&seq, &h)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	return seq, h, err
}

// ChainHashAt returns the stored hash of the event at seq.
func (s *Store) ChainHashAt(ctx context.Context, seq int64) (string, bool, error) {
	var h string
	err := s.db.QueryRowContext(ctx, `SELECT hash FROM events WHERE seq = ?`, seq).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return h, err == nil, err
}

// VerifyChain walks the chain from the event at fromSeq (0: the first) to
// the last one and reports the first break. Starting past the first event,
// the walk links to the event stored just before fromSeq, so checking only
// the tail still catches a removed or edited row inside it.
func (s *Store) VerifyChain(ctx context.Context, fromSeq int64) (domain.ChainReport, error) {
	var rep domain.ChainReport
	if v, ok, err := s.kv.Get(ctx, kvChainGenesis); err == nil && ok {
		var g chainGenesis
		if json.Unmarshal([]byte(v), &g) == nil {
			rep.Genesis = g.Seq
		}
	}
	prev := ""
	if fromSeq > 0 {
		err := s.db.QueryRowContext(ctx, `SELECT hash FROM events WHERE seq < ? ORDER BY seq DESC LIMIT 1`, fromSeq).Scan(&prev)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return rep, err
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+eventCols+`, prev_hash, hash FROM events WHERE seq >= ? ORDER BY seq`, fromSeq)
	if err != nil {
		return rep, err
	}
	defer rows.Close()
	for rows.Next() {
		var ph, h string
		e, err := scanEvent(rows, &ph, &h)
		if err != nil {
			return rep, err
		}
		if rep.Checked == 0 {
			rep.From = e.Seq
		}
		rep.Checked++
		rep.To, rep.Head = e.Seq, h
		if rep.Break != nil {
			continue // keep counting to the head; the first break is what matters
		}
		switch want := domain.ChainHash(ph, e); {
		case h == "":
			rep.Break = &domain.ChainBreak{Seq: e.Seq, Reason: "missing", Want: want}
		case ph != prev:
			rep.Break = &domain.ChainBreak{Seq: e.Seq, Reason: "link", Want: prev, Got: ph}
		case h != want:
			rep.Break = &domain.ChainBreak{Seq: e.Seq, Reason: "hash", Want: want, Got: h}
		}
		prev = h
	}
	return rep, rows.Err()
}

// CheckAnchor compares the stored anchor with the log: the anchored event
// must still be there with the anchored hash. verify checks the signature
// (nil skips it). Returns nil when no anchor was ever written.
func (s *Store) CheckAnchor(ctx context.Context, verify func(domain.ChainAnchor) error) (*domain.ChainAnchorCheck, error) {
	v, ok, err := s.kv.Get(ctx, KVChainAnchor)
	if err != nil || !ok {
		return nil, err
	}
	var a domain.ChainAnchor
	if err := json.Unmarshal([]byte(v), &a); err != nil {
		return &domain.ChainAnchorCheck{Reason: "the stored anchor cannot be read: " + err.Error()}, nil
	}
	c := &domain.ChainAnchorCheck{Seq: a.Seq}
	if verify != nil {
		if err := verify(a); err != nil {
			c.Reason = "the anchor's signature does not verify: " + err.Error()
			return c, nil
		}
	}
	h, found, err := s.ChainHashAt(ctx, a.Seq)
	switch {
	case err != nil:
		return nil, err
	case !found:
		c.Reason = "event " + strconv.FormatInt(a.Seq, 10) + " of the anchor is no longer in the log (events were cut off)"
	case h != a.Hash:
		c.Reason = "event " + strconv.FormatInt(a.Seq, 10) + " no longer has the anchored hash"
	default:
		c.OK = true
	}
	return c, nil
}

// SaveAnchor stores a signed anchor.
func (s *Store) SaveAnchor(ctx context.Context, a domain.ChainAnchor) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return s.kv.Set(ctx, KVChainAnchor, string(b))
}

// OpenReadOnly opens an existing database without migrating or writing it:
// for checking the log of a stopped daemon (`barnraise events verify
// --offline`). The schema must already be at migration 2.
func OpenReadOnly(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	s := &Store{db: db}
	s.kv = &KV{s: s}
	v, err := s.SchemaVersion(context.Background(), "store")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if v < 2 {
		db.Close()
		return nil, fmt.Errorf("the state at %s predates the event chain (schema %d): start the daemon once to migrate it", path, v)
	}
	return s, nil
}
