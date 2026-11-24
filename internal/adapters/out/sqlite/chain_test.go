package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func appendN(t *testing.T, s *Store, n int) {
	t.Helper()
	for i := range n {
		if _, err := s.Append(context.Background(), ev("", "p", domain.EvJournal, "I-1", t0.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestChainIntactAndLinks(t *testing.T) {
	ctx := context.Background()
	s := open(t, ":memory:")
	rep, err := s.VerifyChain(ctx, 0)
	if err != nil || !rep.OK() || rep.Checked != 0 {
		t.Fatalf("empty: %+v %v", rep, err)
	}
	appendN(t, s, 5)
	// A batch is chained in order too.
	if _, err := s.Append(ctx, ev("a", "p", domain.EvJournal, "", t0), ev("b", "q", domain.EvJournal, "", t0)); err != nil {
		t.Fatal(err)
	}
	rep, err = s.VerifyChain(ctx, 0)
	if err != nil || !rep.OK() || rep.Checked != 7 || rep.From != 1 || rep.To != 7 || rep.Head == "" {
		t.Fatalf("intact: %+v %v", rep, err)
	}
	seq, head, err := s.ChainHead(ctx)
	if err != nil || seq != 7 || head != rep.Head {
		t.Fatalf("head %d %s %v", seq, head, err)
	}
	// The tail alone links to the event before it.
	rep, err = s.VerifyChain(ctx, 5)
	if err != nil || !rep.OK() || rep.Checked != 3 || rep.From != 5 {
		t.Fatalf("tail: %+v %v", rep, err)
	}
	// The first event links to "".
	var ph string
	if err := s.db.QueryRow(`SELECT prev_hash FROM events WHERE seq = 1`).Scan(&ph); err != nil || ph != "" {
		t.Fatalf("genesis prev %q %v", ph, err)
	}
}

func TestChainDetectsTampering(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		sql    string
		seq    int64
		reason string
	}{
		"edited data":    {`UPDATE events SET data = '{"note":"forged"}' WHERE seq = 3`, 3, "hash"},
		"edited actor":   {`UPDATE events SET actor = 'owner' WHERE seq = 4`, 4, "hash"},
		"removed row":    {`DELETE FROM events WHERE seq = 3`, 4, "link"},
		"missing hash":   {`UPDATE events SET hash = '' WHERE seq = 2`, 2, "missing"},
		"rewritten hash": {`UPDATE events SET data = '{}', hash = 'abc' WHERE seq = 2`, 2, "hash"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := open(t, ":memory:")
			appendN(t, s, 6)
			if _, err := s.db.Exec(c.sql); err != nil {
				t.Fatal(err)
			}
			rep, err := s.VerifyChain(ctx, 0)
			if err != nil {
				t.Fatal(err)
			}
			if rep.OK() || rep.Break.Seq != c.seq || rep.Break.Reason != c.reason {
				t.Fatalf("break: %+v", rep.Break)
			}
			if rep.To != 6 {
				t.Fatalf("walk stops at the head: %+v", rep)
			}
		})
	}
	// A recomputed row still breaks the link of the next one.
	s := open(t, ":memory:")
	appendN(t, s, 4)
	evs, _ := s.Load(ctx, "", 0)
	forged := evs[1]
	forged.Data = []byte(`{"note":"forged"}`)
	var prev string
	_ = s.db.QueryRow(`SELECT prev_hash FROM events WHERE seq = 2`).Scan(&prev)
	if _, err := s.db.Exec(`UPDATE events SET data = ?, hash = ? WHERE seq = 2`, []byte(forged.Data), domain.ChainHash(prev, forged)); err != nil {
		t.Fatal(err)
	}
	rep, _ := s.VerifyChain(ctx, 0)
	if rep.OK() || rep.Break.Seq != 3 || rep.Break.Reason != "link" {
		t.Fatalf("recomputed: %+v", rep.Break)
	}
}

func TestChainMigrationOfExistingRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, "store", storeMigrations[:1]); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := db.Exec(`INSERT INTO events(id, project, type, time, data) VALUES(?, 'p', 'journal.entry', ?, ?)`,
			"old"+string(rune('a'+i)), t0.UnixNano()+int64(i), []byte(`{"note":"x"}`)); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	if _, err := OpenReadOnly(path); err == nil {
		t.Fatal("read-only open of a pre-chain state must refuse")
	}
	s := open(t, path)
	if v, _ := s.SchemaVersion(ctx, "store"); v < 2 {
		t.Fatalf("schema %d", v)
	}
	appendN(t, s, 2)
	rep, err := s.VerifyChain(ctx, 0)
	if err != nil || !rep.OK() || rep.Checked != 5 || rep.Genesis != 3 {
		t.Fatalf("migrated: %+v %v", rep, err)
	}
	s.Close()
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	rep, err = ro.VerifyChain(ctx, 0)
	if err != nil || !rep.OK() || rep.Checked != 5 {
		t.Fatalf("read-only: %+v %v", rep, err)
	}
	if err := ro.KV().Set(ctx, "x", "y"); err == nil {
		t.Fatal("read-only store accepted a write")
	}
}

func TestChainAnchor(t *testing.T) {
	ctx := context.Background()
	s := open(t, ":memory:")
	if c, err := s.CheckAnchor(ctx, nil); c != nil || err != nil {
		t.Fatalf("no anchor: %+v %v", c, err)
	}
	appendN(t, s, 4)
	seq, h, _ := s.ChainHead(ctx)
	if err := s.SaveAnchor(ctx, domain.ChainAnchor{Seq: seq, Hash: h, Time: t0}); err != nil {
		t.Fatal(err)
	}
	c, err := s.CheckAnchor(ctx, func(domain.ChainAnchor) error { return nil })
	if err != nil || !c.OK || c.Seq != 4 {
		t.Fatalf("anchor ok: %+v %v", c, err)
	}
	c, _ = s.CheckAnchor(ctx, func(domain.ChainAnchor) error { return errors.New("bad") })
	if c.OK {
		t.Fatal("bad signature accepted")
	}
	// Cutting off the tail is what the links alone cannot see.
	if _, err := s.db.Exec(`DELETE FROM events WHERE seq >= 3`); err != nil {
		t.Fatal(err)
	}
	if rep, _ := s.VerifyChain(ctx, 0); !rep.OK() {
		t.Fatalf("links still hold after a cut: %+v", rep)
	}
	c, _ = s.CheckAnchor(ctx, nil)
	if c.OK {
		t.Fatal("cut tail not seen by the anchor")
	}
	_ = s.KV().Set(ctx, KVChainAnchor, "{")
	if c, _ := s.CheckAnchor(ctx, nil); c.OK {
		t.Fatal("unreadable anchor accepted")
	}
}
