package git

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
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
			wg.Go(func() {
				leases[i], errs[i] = a.Claim(ctx, "claims/I-42", fmt.Sprintf("m%d", i+1))
			})
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
				wg.Go(func() {
					n, err := a.Next(ctx, "items")
					if err != nil {
						t.Error(err)
						return
					}
					mu.Lock()
					got = append(got, n)
					mu.Unlock()
				})
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

// TestIntegrateLeavesABusyCheckout: the person's checkout of main follows an
// integration only when its files can. One with an edit where the
// integration changed the same file stays where it was, behind the remote,
// instead of showing every integrated change as a local edit undoing it.
func TestIntegrateLeavesABusyCheckout(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	r := New(f.c1, "origin", "main")
	a := NewArbiter(r, newFakeClock())
	// commit writes files on top of parent in a scratch worktree, as an item would.
	scratch := filepath.Join(t.TempDir(), "scratch")
	g(t, f.c1, "worktree", "add", "-q", "--detach", scratch, "main")
	commit := func(parent string, files map[string]string) string {
		g(t, scratch, "checkout", "-q", "--detach", parent)
		for name, body := range files {
			write(t, scratch, name, body)
		}
		g(t, scratch, "add", "-A")
		g(t, scratch, "commit", "-q", "-m", "item")
		return g(t, scratch, "rev-parse", "HEAD")
	}
	head := func() string { return g(t, f.c1, "rev-parse", "refs/heads/main") }

	// A clean checkout follows, files included.
	main0 := head()
	next1 := commit(main0, map[string]string{"a.txt": "one\ntwo\nthree\nfour\n"})
	if err := a.Integrate(ctx, main0, next1); err != nil {
		t.Fatal(err)
	}
	if head() != next1 || g(t, f.c1, "status", "--porcelain") != "" || read(t, filepath.Join(f.c1, "a.txt")) != "one\ntwo\nthree\nfour\n" {
		t.Fatalf("clean checkout did not follow: head %s, status %q", head(), g(t, f.c1, "status", "--porcelain"))
	}

	// An unrelated local edit is kept, and the checkout still follows.
	write(t, f.c1, "notes.txt", "mine\n")
	next2 := commit(next1, map[string]string{"b.txt": "b\n"})
	if err := a.Integrate(ctx, next1, next2); err != nil {
		t.Fatal(err)
	}
	if head() != next2 || g(t, f.c1, "status", "--porcelain") != "?? notes.txt" {
		t.Fatalf("unrelated edit: head %s, status %q", head(), g(t, f.c1, "status", "--porcelain"))
	}

	// An edit of a file the integration also changes: the checkout stays put,
	// twice in a row, with nothing but that edit to show.
	write(t, f.c1, "a.txt", "local edit\n")
	next3 := commit(next2, map[string]string{"a.txt": "integrated\n"})
	next4 := commit(next3, map[string]string{"c.txt": "c\n"})
	for _, step := range [][2]string{{next2, next3}, {next3, next4}} {
		if err := a.Integrate(ctx, step[0], step[1]); err != nil {
			t.Fatal(err)
		}
	}
	if head() != next2 {
		t.Fatalf("busy checkout moved to %s, want it left at %s", head(), next2)
	}
	if got := g(t, f.c1, "status", "--porcelain"); got != "M a.txt\n?? notes.txt" && got != " M a.txt\n?? notes.txt" {
		t.Fatalf("busy checkout shows %q, want only its own edits", got)
	}
	if read(t, filepath.Join(f.c1, "a.txt")) != "local edit\n" {
		t.Fatal("the local edit was lost")
	}
	if h, _ := r.MainHead(ctx); h != next4 {
		t.Fatalf("main on the remote = %s, want %s", h, next4)
	}
}
