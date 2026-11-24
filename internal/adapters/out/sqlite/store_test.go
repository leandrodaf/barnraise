package sqlite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

func open(t testing.TB, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func ev(id, project, typ, subject string, at time.Time) domain.Event {
	return domain.NewEvent(id, typ, project, subject, "m1", "agent", at, domain.Payload{Note: id})
}

var t0 = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

func TestAppendLoadDedupe(t *testing.T) {
	ctx := context.Background()
	for _, path := range []string{":memory:", filepath.Join(t.TempDir(), "b.db")} {
		s := open(t, path)
		got, err := s.Append(ctx, ev("e1", "p", domain.EvItemCreated, "I-1", t0), ev("e2", "p", domain.EvItemUpdated, "I-1", t0.Add(time.Second)))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 2 {
			t.Fatalf("seq: %+v", got)
		}
		got, err = s.Append(ctx, ev("e2", "p", domain.EvItemUpdated, "I-1", t0), ev("e3", "q", domain.EvItemCreated, "I-1", t0.Add(2*time.Second)))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].ID != "e3" || got[0].Seq != 3 {
			t.Fatalf("dedupe: %+v", got)
		}
		all, err := s.Load(ctx, "p", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(all) != 2 || all[0].ID != "e1" || all[1].ID != "e2" {
			t.Fatalf("load: %+v", all)
		}
		if !all[1].Time.Equal(t0.Add(time.Second)) || all[0].Machine != "m1" || all[0].Subject != "I-1" {
			t.Fatalf("roundtrip: %+v", all[1])
		}
		p, _ := all[0].DecodePayload()
		if p.Note != "e1" {
			t.Fatalf("payload: %+v", p)
		}
		after, _ := s.Load(ctx, "p", 1)
		if len(after) != 1 || after[0].ID != "e2" {
			t.Fatalf("after: %+v", after)
		}
		every, _ := s.Load(ctx, "", 0)
		if len(every) != 3 {
			t.Fatalf("all projects: %d", len(every))
		}
		if n, _ := s.LastSeq(ctx); n != 3 {
			t.Fatalf("last seq %d", n)
		}
	}
}

func TestQuery(t *testing.T) {
	ctx := context.Background()
	s := open(t, ":memory:")
	for i := 1; i <= 10; i++ {
		typ := domain.EvItemUpdated
		if i%2 == 0 {
			typ = domain.EvItemClaimed
		}
		subj := "I-1"
		if i > 5 {
			subj = "I-2"
		}
		if _, err := s.Append(ctx, ev(fmt.Sprint("e", i), "p", typ, subj, t0.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(evs []domain.Event) string {
		var out string
		for _, e := range evs {
			out += e.ID + " "
		}
		return out
	}
	cases := []struct {
		q    ports.EventQuery
		want string
	}{
		{ports.EventQuery{Project: "p", Subject: "I-1"}, "e1 e2 e3 e4 e5 "},
		{ports.EventQuery{Project: "p", Types: []string{domain.EvItemClaimed}}, "e2 e4 e6 e8 e10 "},
		{ports.EventQuery{Project: "p", Since: t0.Add(8 * time.Minute)}, "e8 e9 e10 "},
		{ports.EventQuery{Project: "p", Limit: 3}, "e8 e9 e10 "},
		{ports.EventQuery{Project: "p", Subject: "I-2", Types: []string{domain.EvItemUpdated, domain.EvItemClaimed}, Limit: 2}, "e9 e10 "},
		{ports.EventQuery{Project: "other"}, ""},
	}
	for _, c := range cases {
		got, err := s.Query(ctx, c.q)
		if err != nil {
			t.Fatal(err)
		}
		if ids(got) != c.want {
			t.Errorf("%+v: got %q want %q", c.q, ids(got), c.want)
		}
	}
}

func TestConcurrentAppendFile(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "c.db"))
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range 50 {
				if _, err := s.Append(ctx, ev(fmt.Sprintf("w%d-%d", w, i), "p", domain.EvJournal, "", t0)); err != nil {
					t.Error(err)
					return
				}
				if _, err := s.Load(ctx, "p", int64(i)); err != nil {
					t.Error(err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	all, _ := s.Load(ctx, "p", 0)
	if len(all) != 400 {
		t.Fatalf("got %d", len(all))
	}
	for i, e := range all {
		if e.Seq != int64(i+1) {
			t.Fatalf("gap at %d: %d", i, e.Seq)
		}
	}
}

func TestReopenKeepsDataAndMigrations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "r.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Append(ctx, ev("e1", "p", domain.EvJournal, "", t0))
	s.Close()
	s = open(t, path)
	if v, _ := s.SchemaVersion(ctx, "store"); v != storeMigrations[len(storeMigrations)-1].Version {
		t.Fatalf("version %d", v)
	}
	got, _ := s.Load(ctx, "p", 0)
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	var mode string
	s.DB().QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	if mode != "wal" {
		t.Fatalf("journal mode %s", mode)
	}
}

func TestKV(t *testing.T) {
	ctx := context.Background()
	s := open(t, ":memory:")
	kv := s.KV()
	if _, ok, _ := kv.Get(ctx, "a"); ok {
		t.Fatal("unexpected")
	}
	kv.Set(ctx, "a", "1")
	kv.Set(ctx, "a", "2")
	if v, ok, _ := kv.Get(ctx, "a"); !ok || v != "2" {
		t.Fatalf("got %q %v", v, ok)
	}
	kv.Delete(ctx, "a")
	if _, ok, _ := kv.Get(ctx, "a"); ok {
		t.Fatal("not deleted")
	}
}

func TestTokens(t *testing.T) {
	ctx := context.Background()
	s := open(t, ":memory:")
	exp := t0.Add(24 * time.Hour)
	if err := s.CreateToken(ctx, Token{ID: "t1", Name: "ci", Hash: "h1", Project: "p", Scopes: []string{"read", "items:write"}, Expires: exp, Created: t0}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateToken(ctx, Token{ID: "t2", Name: "admin", Hash: "h2", Scopes: []string{"*"}, Created: t0.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateToken(ctx, Token{ID: "t3", Name: "dup", Hash: "h2"}); err == nil {
		t.Fatal("duplicate hash accepted")
	}
	tk, err := s.GetTokenByHash(ctx, "h1")
	if err != nil {
		t.Fatal(err)
	}
	if tk.Name != "ci" || !tk.Expires.Equal(exp) || !tk.HasScope("read") || tk.HasScope("admin") || !tk.Valid(t0) || tk.Valid(exp) {
		t.Fatalf("token: %+v", tk)
	}
	if _, err := s.GetTokenByHash(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err %v", err)
	}
	if err := s.RevokeToken(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeToken(ctx, "zz"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err %v", err)
	}
	list, _ := s.ListTokens(ctx)
	if len(list) != 2 || list[0].ID != "t1" || !list[0].Revoked || list[0].Valid(t0) || !list[1].HasScope("anything") {
		t.Fatalf("list: %+v", list)
	}
}

func TestAudit(t *testing.T) {
	ctx := context.Background()
	s := open(t, ":memory:")
	for i := range 5 {
		proj := "p"
		if i == 4 {
			proj = "q"
		}
		s.AppendAudit(ctx, AuditEntry{Time: t0.Add(time.Duration(i) * time.Minute), Actor: "owner", TokenID: "t1", Action: fmt.Sprint("a", i), Target: "I-1", Project: proj})
	}
	got, err := s.ListAudit(ctx, AuditQuery{Project: "p", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Action != "a2" || got[1].Action != "a3" || !got[1].Time.Equal(t0.Add(3*time.Minute)) {
		t.Fatalf("audit: %+v", got)
	}
	all, _ := s.ListAudit(ctx, AuditQuery{Since: t0.Add(time.Minute)})
	if len(all) != 4 || all[3].Project != "q" {
		t.Fatalf("since: %+v", all)
	}
}

func BenchmarkAppend(b *testing.B) {
	for _, batch := range []int{1, 100} {
		b.Run(fmt.Sprintf("batch=%d", batch), func(b *testing.B) {
			ctx := context.Background()
			s := open(b, filepath.Join(b.TempDir(), "bench.db"))
			evs := make([]domain.Event, batch)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for j := range evs {
					evs[j] = ev(fmt.Sprintf("e%d-%d", i, j), "p", domain.EvItemUpdated, "I-1", t0)
				}
				if _, err := s.Append(ctx, evs...); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*batch), "ns/event")
		})
	}
}

func BenchmarkLoad(b *testing.B) {
	ctx := context.Background()
	s := open(b, filepath.Join(b.TempDir(), "bench.db"))
	const n = 10000
	evs := make([]domain.Event, 0, 1000)
	for i := range n {
		evs = append(evs, ev(fmt.Sprint("e", i), "p", domain.EvItemUpdated, "I-1", t0))
		if len(evs) == cap(evs) {
			if _, err := s.Append(ctx, evs...); err != nil {
				b.Fatal(err)
			}
			evs = evs[:0]
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := s.Load(ctx, "p", 0)
		if err != nil || len(got) != n {
			b.Fatal(err, len(got))
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*n), "ns/event")
}

func TestStateFileIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "barnraise.db")
	// A file left world-readable by an older version is tightened on open.
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Append(context.Background(), domain.Event{ID: "e1", Project: "demo", Type: "item.created"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		st, err := os.Stat(name)
		if err != nil {
			continue // the journal files exist only while there is something in them
		}
		if perm := st.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s is %o: other users must not read the state", filepath.Base(name), perm)
		}
	}
}
