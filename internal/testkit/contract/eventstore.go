// Package contract holds the behavior every implementation of a port must
// show. The suites run against the in-memory fakes of testkit and against
// the real adapters, so a fake cannot drift from what it stands for.
package contract

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

var t0 = time.Date(2026, 3, 2, 10, 0, 0, 123456789, time.UTC)

func ev(id, project, typ, subject string, at time.Time, data string) domain.Event {
	e := domain.Event{ID: id, Project: project, Type: typ, Subject: subject, Machine: "m1", Actor: "tester", Time: at}
	if data != "" {
		e.Data = json.RawMessage(data)
	}
	return e
}

func ids(evs []domain.Event) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.ID)
	}
	return out
}

func sameIDs(t *testing.T, what string, got []domain.Event, want ...string) {
	t.Helper()
	g := ids(got)
	if fmt.Sprint(g) != fmt.Sprint(want) {
		t.Fatalf("%s = %v, want %v", what, g, want)
	}
}

// EventStore checks the event log semantics. newStore returns a fresh, empty store.
func EventStore(t *testing.T, newStore func(t *testing.T) ports.EventStore) {
	ctx := context.Background()
	open := func(t *testing.T) ports.EventStore {
		s := newStore(t)
		t.Cleanup(func() { _ = s.Close() })
		return s
	}

	t.Run("append assigns increasing seq from 1", func(t *testing.T) {
		s := open(t)
		out, err := s.Append(ctx, ev("a", "p", "x", "I-1", t0, `{"note":"1"}`), ev("b", "p", "x", "I-1", t0, ""))
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 2 || out[0].Seq != 1 || out[1].Seq != 2 || out[0].ID != "a" || out[1].ID != "b" {
			t.Fatalf("append = %+v", out)
		}
		out, err = s.Append(ctx, ev("c", "q", "x", "", t0, ""))
		if err != nil || len(out) != 1 || out[0].Seq != 3 {
			t.Fatalf("seq is global across projects: %+v %v", out, err)
		}
	})

	t.Run("append of nothing", func(t *testing.T) {
		s := open(t)
		out, err := s.Append(ctx)
		if err != nil || len(out) != 0 {
			t.Fatalf("%v %v", out, err)
		}
	})

	t.Run("duplicate ids are skipped, also inside one batch", func(t *testing.T) {
		s := open(t)
		if _, err := s.Append(ctx, ev("a", "p", "x", "", t0, "")); err != nil {
			t.Fatal(err)
		}
		out, err := s.Append(ctx, ev("a", "p", "x", "", t0, ""), ev("b", "p", "x", "", t0, ""), ev("b", "p", "y", "", t0, ""))
		if err != nil {
			t.Fatal(err)
		}
		sameIDs(t, "stored", out, "b")
		if out[0].Seq != 2 || out[0].Type != "x" {
			t.Fatalf("first copy wins with the next seq: %+v", out[0])
		}
		all, _ := s.Load(ctx, "", 0)
		sameIDs(t, "log", all, "a", "b")
	})

	t.Run("events without id are always stored", func(t *testing.T) {
		s := open(t)
		out, err := s.Append(ctx, ev("", "p", "x", "", t0, ""), ev("", "p", "x", "", t0, ""))
		if err != nil || len(out) != 2 || out[0].Seq == out[1].Seq {
			t.Fatalf("%+v %v", out, err)
		}
		all, _ := s.Load(ctx, "p", 0)
		if len(all) != 2 || all[0].ID != "" {
			t.Fatalf("load %+v", all)
		}
	})

	t.Run("round trip keeps fields, UTC nanoseconds and data", func(t *testing.T) {
		s := open(t)
		local := t0.In(time.FixedZone("BRT", -3*3600))
		in := ev("a", "p", "item.created", "I-7", local, `{"note":"hello"}`)
		in.Machine, in.Actor = "alpha", "owner"
		if _, err := s.Append(ctx, in, ev("b", "p", "x", "", t0, "")); err != nil {
			t.Fatal(err)
		}
		all, err := s.Load(ctx, "p", 0)
		if err != nil || len(all) != 2 {
			t.Fatalf("%v %v", all, err)
		}
		got := all[0]
		if got.Project != "p" || got.Type != "item.created" || got.Subject != "I-7" || got.Machine != "alpha" || got.Actor != "owner" || got.Seq != 1 {
			t.Fatalf("fields %+v", got)
		}
		if !got.Time.Equal(t0) || got.Time.Location() != time.UTC || got.Time.Nanosecond() != t0.Nanosecond() {
			t.Fatalf("time %v (%v), want %v UTC", got.Time, got.Time.Location(), t0)
		}
		if string(got.Data) != `{"note":"hello"}` {
			t.Fatalf("data %q", got.Data)
		}
		if all[1].Data != nil {
			t.Fatalf("empty data must come back nil, got %q", all[1].Data)
		}
		pl, err := got.DecodePayload()
		if err != nil || pl.Note != "hello" {
			t.Fatalf("payload %+v %v", pl, err)
		}
	})

	t.Run("load by project and after seq", func(t *testing.T) {
		s := open(t)
		_, err := s.Append(ctx, ev("a", "p", "x", "", t0, ""), ev("b", "q", "x", "", t0, ""), ev("c", "p", "x", "", t0, ""), ev("d", "p", "x", "", t0, ""))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := s.Load(ctx, "p", 0)
		sameIDs(t, "project p", got, "a", "c", "d")
		got, _ = s.Load(ctx, "p", 1)
		sameIDs(t, "after 1", got, "c", "d")
		got, _ = s.Load(ctx, "", 2)
		sameIDs(t, "all projects after 2", got, "c", "d")
		got, _ = s.Load(ctx, "none", 0)
		if len(got) != 0 {
			t.Fatalf("unknown project %v", ids(got))
		}
		got, _ = s.Load(ctx, "", 99)
		if len(got) != 0 {
			t.Fatalf("after the end %v", ids(got))
		}
	})

	t.Run("query filters", func(t *testing.T) {
		s := open(t)
		_, err := s.Append(ctx,
			ev("a", "p", "item.created", "I-1", t0, ""),
			ev("b", "p", "check.completed", "I-1", t0.Add(time.Minute), ""),
			ev("c", "p", "check.completed", "I-2", t0.Add(2*time.Minute), ""),
			ev("d", "q", "check.completed", "I-1", t0.Add(3*time.Minute), ""),
			ev("e", "p", "delivery.submitted", "I-1", t0.Add(4*time.Minute), ""),
			ev("f", "p", "check.completed", "I-1", t0.Add(5*time.Minute), ""),
		)
		if err != nil {
			t.Fatal(err)
		}
		cases := []struct {
			name string
			q    ports.EventQuery
			want []string
		}{
			{"everything", ports.EventQuery{}, []string{"a", "b", "c", "d", "e", "f"}},
			{"project", ports.EventQuery{Project: "p"}, []string{"a", "b", "c", "e", "f"}},
			{"subject", ports.EventQuery{Project: "p", Subject: "I-1"}, []string{"a", "b", "e", "f"}},
			{"subject in all projects", ports.EventQuery{Subject: "I-1"}, []string{"a", "b", "d", "e", "f"}},
			{"one type", ports.EventQuery{Project: "p", Types: []string{"check.completed"}}, []string{"b", "c", "f"}},
			{"two types", ports.EventQuery{Project: "p", Subject: "I-1", Types: []string{"check.completed", "delivery.submitted"}}, []string{"b", "e", "f"}},
			{"since is inclusive", ports.EventQuery{Project: "p", Since: t0.Add(2 * time.Minute)}, []string{"c", "e", "f"}},
			{"since in another zone", ports.EventQuery{Project: "p", Since: t0.Add(4 * time.Minute).In(time.FixedZone("X", 7200))}, []string{"e", "f"}},
			{"limit takes the newest, oldest first", ports.EventQuery{Project: "p", Limit: 2}, []string{"e", "f"}},
			{"limit with filters", ports.EventQuery{Project: "p", Types: []string{"check.completed"}, Limit: 2}, []string{"c", "f"}},
			{"limit larger than the result", ports.EventQuery{Project: "p", Subject: "I-2", Limit: 10}, []string{"c"}},
			{"no match", ports.EventQuery{Project: "p", Types: []string{"nope"}}, nil},
		}
		for _, c := range cases {
			got, err := s.Query(ctx, c.q)
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			g := ids(got)
			if len(g) == 0 {
				g = nil
			}
			if fmt.Sprint(g) != fmt.Sprint(c.want) {
				t.Errorf("%s = %v, want %v", c.name, g, c.want)
			}
			for i := 1; i < len(got); i++ {
				if got[i].Seq <= got[i-1].Seq {
					t.Errorf("%s: not in seq order", c.name)
				}
			}
		}
	})

	t.Run("concurrent appends get unique seqs", func(t *testing.T) {
		s := open(t)
		var wg sync.WaitGroup
		var mu sync.Mutex
		seqs := map[int64]bool{}
		for w := 0; w < 4; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 20; i++ {
					out, err := s.Append(ctx, ev(fmt.Sprintf("w%d-%d", w, i), "p", "x", "", t0, ""))
					if err != nil || len(out) != 1 {
						t.Errorf("append: %v %v", out, err)
						return
					}
					mu.Lock()
					seqs[out[0].Seq] = true
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		all, _ := s.Load(ctx, "p", 0)
		if len(seqs) != 80 || len(all) != 80 {
			t.Fatalf("seqs %d, events %d", len(seqs), len(all))
		}
	})
}
