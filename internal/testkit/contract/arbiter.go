package contract

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/ports"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

// Machines returns a constructor of arbiter handles over one fresh shared
// backend: each call is another machine with its own clock.
type Machines func(t *testing.T) func(clock ports.Clock) ports.Arbiter

// Arbiter checks leases, counters and listing. Every subtest gets a fresh backend.
func Arbiter(t *testing.T, machines Machines) {
	ctx := context.Background()
	two := func(t *testing.T) (a1, a2 ports.Arbiter, c1, c2 *testkit.Clock) {
		mk := machines(t)
		c1, c2 = testkit.NewClock(), testkit.NewClock()
		return mk(c1), mk(c2), c1, c2
	}

	t.Run("claim is exclusive across machines", func(t *testing.T) {
		a1, a2, _, _ := two(t)
		var wg sync.WaitGroup
		leases := make([]ports.Lease, 2)
		errs := make([]error, 2)
		for i, a := range []ports.Arbiter{a1, a2} {
			wg.Go(func() {
				leases[i], errs[i] = a.Claim(ctx, "claims/I-42", fmt.Sprintf("m%d", i+1))
			})
		}
		wg.Wait()
		won := 0
		for i, err := range errs {
			switch {
			case err == nil:
				won++
				if leases[i].Key != "claims/I-42" || leases[i].Holder != fmt.Sprintf("m%d", i+1) || leases[i].Version == "" || leases[i].Renewed.IsZero() {
					t.Fatalf("lease %+v", leases[i])
				}
			case !errors.Is(err, ports.ErrTaken):
				t.Fatalf("loser got %v, want ErrTaken", err)
			}
		}
		if won != 1 {
			t.Fatalf("%d winners", won)
		}
	})

	t.Run("a key without a slash is an item claim", func(t *testing.T) {
		a1, a2, _, _ := two(t)
		l, err := a1.Claim(ctx, "I-7", "m1")
		if err != nil {
			t.Fatal(err)
		}
		if l.Key != "I-7" {
			t.Fatalf("the lease keeps the key as given: %q", l.Key)
		}
		if _, err := a2.Claim(ctx, "claims/I-7", "m2"); !errors.Is(err, ports.ErrTaken) {
			t.Fatalf("claims/I-7 is the same lease: %v", err)
		}
		info, err := a2.Inspect(ctx, "claims/I-7")
		if err != nil || !info.Held || info.Holder != "m1" || info.Version != l.Version {
			t.Fatalf("inspect %+v %v", info, err)
		}
	})

	t.Run("renew moves the version; the old one is lost", func(t *testing.T) {
		a1, a2, c1, _ := two(t)
		l, err := a1.Claim(ctx, "lease/x", "m1")
		if err != nil {
			t.Fatal(err)
		}
		c1.Advance(10 * time.Second)
		nl, err := a1.Renew(ctx, l)
		if err != nil {
			t.Fatal(err)
		}
		if nl.Version == l.Version || nl.Holder != "m1" || nl.Key != "lease/x" || !nl.Renewed.Equal(c1.Now()) {
			t.Fatalf("renewed %+v (was %+v)", nl, l)
		}
		if _, err := a2.Renew(ctx, l); !errors.Is(err, ports.ErrLost) {
			t.Fatalf("renew with an old version = %v, want ErrLost", err)
		}
		if _, err := a1.Renew(ctx, ports.Lease{Key: "lease/x", Holder: "m1"}); !errors.Is(err, ports.ErrLost) {
			t.Fatalf("renew without a version = %v, want ErrLost", err)
		}
		if _, err := a1.Renew(ctx, ports.Lease{Key: "lease/none", Holder: "m1", Version: nl.Version}); !errors.Is(err, ports.ErrLost) {
			t.Fatalf("renew of a missing lease = %v, want ErrLost", err)
		}
		info, err := a2.Inspect(ctx, "lease/x")
		if err != nil || info.Version != nl.Version || !info.Renewed.Equal(nl.Renewed) {
			t.Fatalf("inspect after renew %+v %v", info, err)
		}
	})

	t.Run("release frees the key only at the current version", func(t *testing.T) {
		a1, a2, _, _ := two(t)
		l, err := a1.Claim(ctx, "claims/I-1", "m1")
		if err != nil {
			t.Fatal(err)
		}
		if err := a2.Release(ctx, ports.Lease{Key: "claims/I-1", Holder: "m2"}); !errors.Is(err, ports.ErrNotHeld) {
			t.Fatalf("release without version = %v", err)
		}
		nl, err := a1.Renew(ctx, l)
		if err != nil {
			t.Fatal(err)
		}
		if err := a1.Release(ctx, l); !errors.Is(err, ports.ErrNotHeld) {
			t.Fatalf("release of a stale version = %v", err)
		}
		if err := a1.Release(ctx, nl); err != nil {
			t.Fatal(err)
		}
		if err := a1.Release(ctx, nl); !errors.Is(err, ports.ErrNotHeld) {
			t.Fatalf("second release = %v", err)
		}
		info, err := a2.Inspect(ctx, "claims/I-1")
		if err != nil || info.Held || info.Holder != "" || info.Version != "" {
			t.Fatalf("free key inspect %+v %v", info, err)
		}
		if _, err := a2.Claim(ctx, "claims/I-1", "m2"); err != nil {
			t.Fatalf("claim after release: %v", err)
		}
	})

	t.Run("inspect reports the holder and local observation", func(t *testing.T) {
		a1, a2, c1, c2 := two(t)
		c1.Advance(48 * time.Hour) // the holder's clock is far off: it must not matter
		l, err := a1.Claim(ctx, "lease/housekeeping", "m1")
		if err != nil {
			t.Fatal(err)
		}
		first := c2.Now()
		info, err := a2.Inspect(ctx, "lease/housekeeping")
		if err != nil || !info.Held || info.Holder != "m1" || info.Version != l.Version || !info.Renewed.Equal(l.Renewed) || !info.UnchangedSince.Equal(first) {
			t.Fatalf("inspect %+v %v (lease %+v)", info, err, l)
		}
		c2.Advance(time.Minute)
		info, _ = a2.Inspect(ctx, "lease/housekeeping")
		if !info.UnchangedSince.Equal(first) {
			t.Fatalf("same version keeps its first sighting: %v, want %v", info.UnchangedSince, first)
		}
		if _, err := a1.Renew(ctx, l); err != nil {
			t.Fatal(err)
		}
		info, _ = a2.Inspect(ctx, "lease/housekeeping")
		if !info.UnchangedSince.Equal(c2.Now()) {
			t.Fatalf("a new version is a new sighting: %v, want %v", info.UnchangedSince, c2.Now())
		}
		free, err := a2.Inspect(ctx, "lease/none")
		if err != nil || free.Held {
			t.Fatalf("free %+v %v", free, err)
		}
	})

	t.Run("takeover only after ttl without change, by local observation", func(t *testing.T) {
		a1, a2, c1, c2 := two(t)
		const key, ttl = "lease/housekeeping", 2 * time.Minute
		c1.Advance(24 * time.Hour)
		l, err := a1.Claim(ctx, key, "m1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a2.TakeOver(ctx, key, "m2", ttl); !errors.Is(err, ports.ErrTaken) {
			t.Fatalf("takeover at first sight = %v", err)
		}
		c2.Advance(ttl)
		if _, err := a2.TakeOver(ctx, key, "m2", ttl); !errors.Is(err, ports.ErrTaken) {
			t.Fatalf("takeover exactly at ttl = %v", err)
		}
		if l, err = a1.Renew(ctx, l); err != nil {
			t.Fatal(err)
		}
		c2.Advance(90 * time.Second)
		if _, err := a2.TakeOver(ctx, key, "m2", ttl); !errors.Is(err, ports.ErrTaken) {
			t.Fatalf("takeover after a renewal = %v", err)
		}
		c2.Advance(ttl + time.Second)
		nl, err := a2.TakeOver(ctx, key, "m2", ttl)
		if err != nil {
			t.Fatalf("takeover after ttl: %v", err)
		}
		if nl.Holder != "m2" || nl.Key != key || nl.Version == "" || nl.Version == l.Version {
			t.Fatalf("taken %+v", nl)
		}
		if _, err := a1.Renew(ctx, l); !errors.Is(err, ports.ErrLost) {
			t.Fatalf("old holder renew = %v, want ErrLost", err)
		}
		if err := a1.Release(ctx, l); !errors.Is(err, ports.ErrNotHeld) {
			t.Fatalf("old holder release = %v, want ErrNotHeld", err)
		}
		info, err := a1.Inspect(ctx, key)
		if err != nil || info.Holder != "m2" || info.Version != nl.Version {
			t.Fatalf("inspect %+v %v", info, err)
		}
		if _, err := a2.TakeOver(ctx, "lease/other", "m2", ttl); err != nil {
			t.Fatalf("takeover of a free key is a claim: %v", err)
		}
		if _, err := a1.Claim(ctx, "lease/other", "m1"); !errors.Is(err, ports.ErrTaken) {
			t.Fatalf("claim after takeover of free key = %v", err)
		}
	})

	t.Run("counters start at 1 and never repeat", func(t *testing.T) {
		a1, a2, _, _ := two(t)
		for want := 1; want <= 3; want++ {
			n, err := a1.Next(ctx, "ids-I")
			if err != nil || n != want {
				t.Fatalf("next = %d %v, want %d", n, err, want)
			}
		}
		if n, err := a2.Next(ctx, "ids-P"); err != nil || n != 1 {
			t.Fatalf("other counter = %d %v", n, err)
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		got := map[int]bool{}
		for _, a := range []ports.Arbiter{a1, a2} {
			for range 2 {
				wg.Go(func() {
					for range 4 {
						n, err := a.Next(ctx, "ids-I")
						if err != nil {
							t.Errorf("next: %v", err)
							return
						}
						mu.Lock()
						if got[n] {
							t.Errorf("number %d given twice", n)
						}
						got[n] = true
						mu.Unlock()
					}
				})
			}
		}
		wg.Wait()
		for n := 4; n <= 19; n++ {
			if !got[n] {
				t.Fatalf("number %d missing: %v", n, got)
			}
		}
	})

	t.Run("list keys by prefix", func(t *testing.T) {
		a1, a2, _, _ := two(t)
		for _, k := range []string{"claims/I-2", "I-10", "lease/housekeeping"} {
			if _, err := a1.Claim(ctx, k, "m1"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := a1.Next(ctx, "ids-I"); err != nil {
			t.Fatal(err)
		}
		got, err := a2.List(ctx, "claims/")
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(got)
		if fmt.Sprint(got) != "[claims/I-10 claims/I-2]" {
			t.Fatalf("claims = %v", got)
		}
		all, err := a2.List(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(all)
		if fmt.Sprint(all) != "[claims/I-10 claims/I-2 counters/ids-I lease/housekeeping]" {
			t.Fatalf("all = %v", all)
		}
		none, err := a2.List(ctx, "nothing/")
		if err != nil || none == nil || len(none) != 0 {
			t.Fatalf("empty list must be empty, not nil: %#v %v", none, err)
		}
	})

	t.Run("invalid keys are refused", func(t *testing.T) {
		a1, _, _, _ := two(t)
		for _, k := range []string{"", "/x", "x/", "a..b", "a b", "a:b", "lease/.hidden", "x.lock", "a//b", "a*b"} {
			if _, err := a1.Claim(ctx, k, "m1"); err == nil {
				t.Errorf("claim %q accepted", k)
			}
			if _, err := a1.Inspect(ctx, k); err == nil {
				t.Errorf("inspect %q accepted", k)
			}
		}
		if _, err := a1.Next(ctx, "bad name"); err == nil {
			t.Error("counter with a space accepted")
		}
	})
}

// IntegrateFixture is an arbiter whose main is Main, with Child on top of
// Main, Grandchild on top of Child and Unrelated outside that history.
type IntegrateFixture struct {
	Arbiter    ports.Arbiter
	Main       string
	Child      string
	Grandchild string
	Unrelated  string
	// MainAt reads main at the arbiter.
	MainAt func() string
}

// ArbiterIntegrate checks that main moves only by fast-forward from the expected value.
func ArbiterIntegrate(t *testing.T, fixture func(t *testing.T) IntegrateFixture) {
	ctx := context.Background()
	t.Run("fast-forward and stale main", func(t *testing.T) {
		f := fixture(t)
		if err := f.Arbiter.Integrate(ctx, f.Main, f.Main); err != nil || f.MainAt() != f.Main {
			t.Fatalf("same commit: %v, main %s", err, f.MainAt())
		}
		if err := f.Arbiter.Integrate(ctx, f.Main, f.Unrelated); err == nil || errors.Is(err, ports.ErrStaleMain) || f.MainAt() != f.Main {
			t.Fatalf("unrelated commit: %v, main %s", err, f.MainAt())
		}
		if err := f.Arbiter.Integrate(ctx, f.Main, f.Child); err != nil || f.MainAt() != f.Child {
			t.Fatalf("fast-forward: %v, main %s", err, f.MainAt())
		}
		if err := f.Arbiter.Integrate(ctx, f.Main, f.Grandchild); !errors.Is(err, ports.ErrStaleMain) || f.MainAt() != f.Child {
			t.Fatalf("stale expected main = %v, main %s", err, f.MainAt())
		}
		if err := f.Arbiter.Integrate(ctx, f.Child, f.Grandchild); err != nil || f.MainAt() != f.Grandchild {
			t.Fatalf("second fast-forward: %v, main %s", err, f.MainAt())
		}
		if err := f.Arbiter.Integrate(ctx, f.Grandchild, f.Child); err == nil || f.MainAt() != f.Grandchild {
			t.Fatalf("going back must fail: %v, main %s", err, f.MainAt())
		}
	})
}
