package git

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// machines returns two arbiters simulating two machines, on a shared bare
// remote (remote mode) or on the same disk (local mode).
func machines(t *testing.T, remote bool) (a1, a2 *Arbiter, c1, c2 *fakeClock, r1, r2 *Repo) {
	t.Helper()
	c1, c2 = newFakeClock(), newFakeClock()
	if remote {
		f := newFixture(t)
		r1, r2 = New(f.c1, "origin", "main"), New(f.c2, "origin", "main")
	} else {
		dir := newLocal(t)
		r1, r2 = New(dir, "", "main"), New(dir, "", "main")
	}
	return NewArbiter(r1, c1), NewArbiter(r2, c2), c1, c2, r1, r2
}

func forModes(t *testing.T, fn func(t *testing.T, remote bool)) {
	for _, remote := range []bool{true, false} {
		name := "local"
		if remote {
			name = "remote"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fn(t, remote)
		})
	}
}

func TestClaimRenewRelease(t *testing.T) {
	forModes(t, func(t *testing.T, remote bool) {
		ctx := context.Background()
		a1, a2, _, _, _, _ := machines(t, remote)

		var wg sync.WaitGroup
		leases := make([]ports.Lease, 2)
		errs := make([]error, 2)
		for i, a := range []*Arbiter{a1, a2} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				leases[i], errs[i] = a.Claim(ctx, "claims/I-42", fmt.Sprintf("m%d", i+1))
			}()
		}
		wg.Wait()
		win := -1
		for i, err := range errs {
			switch {
			case err == nil:
				if win != -1 {
					t.Fatal("both machines won the claim")
				}
				win = i
			case !errors.Is(err, ports.ErrTaken):
				t.Fatalf("claim %d: %v", i, err)
			}
		}
		if win == -1 {
			t.Fatal("nobody won the claim")
		}
		arbs := []*Arbiter{a1, a2}
		winner, loser := arbs[win], arbs[1-win]
		wl := leases[win]

		info, err := loser.Inspect(ctx, "claims/I-42")
		if err != nil || !info.Held || info.Holder != wl.Holder || info.Version != wl.Version {
			t.Fatalf("inspect = %+v, %v", info, err)
		}

		renewed, err := winner.Renew(ctx, wl)
		if err != nil {
			t.Fatalf("renew by winner: %v", err)
		}
		if renewed.Version == wl.Version {
			t.Fatal("renew kept the version")
		}
		// The loser forging a lease with the stale version must fail.
		if _, err := loser.Renew(ctx, ports.Lease{Key: wl.Key, Holder: "intruder", Version: wl.Version}); !errors.Is(err, ports.ErrLost) {
			t.Fatalf("renew by loser = %v, want ErrLost", err)
		}
		if err := winner.Release(ctx, wl); !errors.Is(err, ports.ErrNotHeld) {
			t.Fatalf("release with stale version = %v", err)
		}

		keys, err := loser.List(ctx, "claims/")
		if err != nil || !slices.Equal(keys, []string{"claims/I-42"}) {
			t.Fatalf("list = %v, %v", keys, err)
		}

		if err := winner.Release(ctx, renewed); err != nil {
			t.Fatalf("release: %v", err)
		}
		if info, err := loser.Inspect(ctx, "claims/I-42"); err != nil || info.Held {
			t.Fatalf("inspect after release = %+v, %v", info, err)
		}
		if _, err := loser.Claim(ctx, "I-42", "other"); err != nil {
			t.Fatalf("claim after release: %v", err)
		}
		if _, err := winner.Claim(ctx, "claims/I-42", "again"); !errors.Is(err, ports.ErrTaken) {
			t.Fatalf("bare key and claims/ key must be the same lease: %v", err)
		}
	})
}

func TestTakeOver(t *testing.T) {
	forModes(t, func(t *testing.T, remote bool) {
		ctx := context.Background()
		a1, a2, c1, c2, _, _ := machines(t, remote)
		const key = "lease/housekeeping"
		const ttl = 2 * time.Minute

		// The holder's clock is far in the future (clock skew).
		c1.Advance(24 * time.Hour)
		l, err := a1.Claim(ctx, key, "m1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a2.TakeOver(ctx, key, "m2", ttl); !errors.Is(err, ports.ErrTaken) {
			t.Fatalf("takeover at first sight = %v, want ErrTaken", err)
		}
		c2.Advance(time.Minute)
		if _, err := a2.TakeOver(ctx, key, "m2", ttl); !errors.Is(err, ports.ErrTaken) {
			t.Fatalf("takeover before ttl = %v, want ErrTaken", err)
		}
		// A renewal resets the local observation.
		if l, err = a1.Renew(ctx, l); err != nil {
			t.Fatal(err)
		}
		c2.Advance(90 * time.Second)
		if _, err := a2.TakeOver(ctx, key, "m2", ttl); !errors.Is(err, ports.ErrTaken) {
			t.Fatalf("takeover after renewal = %v, want ErrTaken", err)
		}
		c2.Advance(ttl + time.Second)
		nl, err := a2.TakeOver(ctx, key, "m2", ttl)
		if err != nil {
			t.Fatalf("takeover after ttl: %v", err)
		}
		if _, err := a1.Renew(ctx, l); !errors.Is(err, ports.ErrLost) {
			t.Fatalf("old holder renew = %v, want ErrLost", err)
		}
		info, err := a1.Inspect(ctx, key)
		if err != nil || info.Holder != "m2" || info.Version != nl.Version {
			t.Fatalf("inspect = %+v, %v", info, err)
		}
		// No lease at all: takeover is a plain claim.
		if _, err := a2.TakeOver(ctx, "lease/other", "m2", ttl); err != nil {
			t.Fatalf("takeover of free key: %v", err)
		}
	})
}

func TestNextUnique(t *testing.T) {
	forModes(t, func(t *testing.T, remote bool) {
		ctx := context.Background()
		a1, a2, _, _, _, _ := machines(t, remote)
		const perClone = 10
		var mu sync.Mutex
		var got []int
		var wg sync.WaitGroup
		for _, a := range []*Arbiter{a1, a2} {
			for range perClone {
				wg.Add(1)
				go func() {
					defer wg.Done()
					n, err := a.Next(ctx, "items")
					if err != nil {
						t.Error(err)
						return
					}
					mu.Lock()
					got = append(got, n)
					mu.Unlock()
				}()
			}
		}
		wg.Wait()
		slices.Sort(got)
		for i, n := range got {
			if n != i+1 {
				t.Fatalf("numbers not unique and dense: %v", got)
			}
		}
		if len(got) != 2*perClone {
			t.Fatalf("got %d numbers", len(got))
		}
	})
}

func TestIntegrate(t *testing.T) {
	forModes(t, func(t *testing.T, remote bool) {
		ctx := context.Background()
		a1, a2, _, _, r1, r2 := machines(t, remote)
		main0, err := r1.MainHead(ctx)
		if err != nil {
			t.Fatal(err)
		}
		mk := func(r *Repo, parent, msg string) string {
			tree := g(t, r.Root(), "rev-parse", parent+"^{tree}")
			return g(t, r.Root(), "commit-tree", tree, "-p", parent, "-m", msg)
		}
		next1 := mk(r1, main0, "item one")
		if err := a1.Integrate(ctx, main0, next1); err != nil {
			t.Fatalf("integrate: %v", err)
		}
		if h, _ := r1.MainHead(ctx); h != next1 {
			t.Fatalf("main head = %s, want %s", h, next1)
		}
		// The other machine still believes main is main0.
		next2 := mk(r2, main0, "item two")
		if err := a2.Integrate(ctx, main0, next2); !errors.Is(err, ports.ErrStaleMain) {
			t.Fatalf("stale integrate = %v, want ErrStaleMain", err)
		}
		if err := r2.Fetch(ctx); err != nil {
			t.Fatal(err)
		}
		head, _ := r2.MainHead(ctx)
		if head != next1 {
			t.Fatalf("after fetch main = %s, want %s", head, next1)
		}
		// next2 is not based on the current main.
		if err := a2.Integrate(ctx, head, next2); !errors.Is(err, ErrNotDescendant) {
			t.Fatalf("non-descendant = %v, want ErrNotDescendant", err)
		}
		next3 := mk(r2, head, "item two rebased")
		if err := a2.Integrate(ctx, head, next3); err != nil {
			t.Fatalf("integrate rebased: %v", err)
		}
		if !remote {
			// The checked-out main follows, with its files.
			if got := g(t, r1.Root(), "status", "--porcelain"); got != "" {
				t.Fatalf("worktree dirty after integrate: %q", got)
			}
		}
	})
}

func TestInvalidKeys(t *testing.T) {
	for _, k := range []string{"", "/x", "a..b", "a b", "x.lock", "a//b", "claims/"} {
		if _, err := KeyRef(k); err == nil {
			t.Errorf("key %q accepted", k)
		}
	}
	if ref, _ := KeyRef("I-1"); ref != "refs/barnraise/claims/I-1" {
		t.Errorf("bare key maps to %s", ref)
	}
}

func BenchmarkClaimLocal(b *testing.B) {
	ctx := context.Background()
	a := NewArbiter(New(newLocal(b), "", "main"), nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := a.Claim(ctx, fmt.Sprintf("claims/I-%d", i), "m1"); err != nil {
			b.Fatal(err)
		}
	}
}
