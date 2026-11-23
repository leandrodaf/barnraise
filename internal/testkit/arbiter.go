package testkit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// ErrNotDescendant mirrors the git arbiter: next does not contain the expected main.
var ErrNotDescendant = errors.New("next does not descend from main")

// ArbiterBackend is the shared state of in-memory arbiters: what the git
// server holds. Each Handle is one machine with its own local observations
// (when it first saw each lease version), like one clone of the repository.
type ArbiterBackend struct {
	mu       sync.Mutex
	leases   map[string]memLease // by normalized key
	counters map[string]int
	main     string
	parents  map[string]string // commit -> parent, for ancestry
	version  int
	offline  bool
}

type memLease struct {
	holder  string
	version string
	renewed time.Time
}

// NewArbiterBackend returns an empty backend.
func NewArbiterBackend() *ArbiterBackend {
	return &ArbiterBackend{leases: map[string]memLease{}, counters: map[string]int{}, parents: map[string]string{}}
}

// Handle returns an arbiter for one machine; clock nil means a fresh manual clock.
func (b *ArbiterBackend) Handle(clock ports.Clock) *Arbiter {
	if clock == nil {
		clock = NewClock()
	}
	return &Arbiter{b: b, clock: clock, seen: map[string]observation{}}
}

// SetOffline makes every operation fail with ports.ErrOffline.
func (b *ArbiterBackend) SetOffline(off bool) {
	b.mu.Lock()
	b.offline = off
	b.mu.Unlock()
}

// SetMain sets the main commit the arbiter guards.
func (b *ArbiterBackend) SetMain(sha string) {
	b.mu.Lock()
	b.main = sha
	b.mu.Unlock()
}

// Main returns the current main commit.
func (b *ArbiterBackend) Main() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.main
}

// AddCommit records a commit and its parent ("" for a root). Once any commit
// is recorded, Integrate checks ancestry and unknown revisions; before that,
// every revision exists and descends from main.
func (b *ArbiterBackend) AddCommit(sha, parent string) {
	b.mu.Lock()
	b.parents[sha] = parent
	b.mu.Unlock()
}

// Arbiter is an in-memory ports.Arbiter with the semantics of the git
// adapter: compare-and-swap on opaque versions, TakeOver by local
// observation only (the holder's timestamp is ignored), keys without a
// slash are item claims ("I-42" is "claims/I-42"), counters start at 1.
type Arbiter struct {
	b     *ArbiterBackend
	clock ports.Clock

	mu   sync.Mutex
	seen map[string]observation
}

type observation struct {
	version string
	since   time.Time
}

var _ ports.Arbiter = (*Arbiter)(nil)

// NewArbiter returns an arbiter on its own fresh backend.
func NewArbiter(clock ports.Clock) *Arbiter { return NewArbiterBackend().Handle(clock) }

// Backend returns the shared state, to add more machines or set main.
func (a *Arbiter) Backend() *ArbiterBackend { return a.b }

// normKey validates a key like the git adapter and returns its ref name.
func normKey(key string) (string, error) {
	bad := key == "" || strings.HasPrefix(key, "/") || strings.HasSuffix(key, "/") ||
		strings.HasSuffix(key, ".lock") || strings.HasSuffix(key, ".") ||
		strings.Contains(key, "..") || strings.Contains(key, "//") || strings.Contains(key, "@{") ||
		strings.ContainsAny(key, " ~^:?*[\\\t\n\r")
	for part := range strings.SplitSeq(key, "/") {
		if strings.HasPrefix(part, ".") {
			bad = true
		}
	}
	for _, c := range key {
		if c < 0x20 || c == 0x7f {
			bad = true
		}
	}
	if bad {
		return "", fmt.Errorf("invalid arbiter key %q", key)
	}
	if !strings.Contains(key, "/") {
		key = "claims/" + key
	}
	return key, nil
}

// lockB locks the backend or reports it offline.
func (a *Arbiter) lockB() error {
	a.b.mu.Lock()
	if a.b.offline {
		a.b.mu.Unlock()
		return fmt.Errorf("%w: memory arbiter is offline", ports.ErrOffline)
	}
	return nil
}

func (a *Arbiter) nextVersion() string {
	a.b.version++
	return fmt.Sprintf("v%06d", a.b.version)
}

func (a *Arbiter) observe(key, version string) time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	o, ok := a.seen[key]
	if !ok || o.version != version {
		o = observation{version: version, since: a.clock.Now()}
		a.seen[key] = o
	}
	return o.since
}

func (a *Arbiter) forget(key string) {
	a.mu.Lock()
	delete(a.seen, key)
	a.mu.Unlock()
}

// Claim implements ports.Arbiter.
func (a *Arbiter) Claim(_ context.Context, key, holder string) (ports.Lease, error) {
	ref, err := normKey(key)
	if err != nil {
		return ports.Lease{}, err
	}
	if err := a.lockB(); err != nil {
		return ports.Lease{}, err
	}
	if _, ok := a.b.leases[ref]; ok {
		a.b.mu.Unlock()
		return ports.Lease{}, fmt.Errorf("%w: %s", ports.ErrTaken, key)
	}
	now := a.clock.Now().UTC()
	v := a.nextVersion()
	a.b.leases[ref] = memLease{holder: holder, version: v, renewed: now}
	a.b.mu.Unlock()
	a.observe(key, v)
	return ports.Lease{Key: key, Holder: holder, Version: v, Renewed: now}, nil
}

// Renew implements ports.Arbiter.
func (a *Arbiter) Renew(_ context.Context, l ports.Lease) (ports.Lease, error) {
	ref, err := normKey(l.Key)
	if err != nil {
		return ports.Lease{}, err
	}
	if l.Version == "" {
		return ports.Lease{}, fmt.Errorf("%w: %s has no version", ports.ErrLost, l.Key)
	}
	if err := a.lockB(); err != nil {
		return ports.Lease{}, err
	}
	cur, ok := a.b.leases[ref]
	if !ok || cur.version != l.Version {
		a.b.mu.Unlock()
		return ports.Lease{}, fmt.Errorf("%w: %s", ports.ErrLost, l.Key)
	}
	now := a.clock.Now().UTC()
	v := a.nextVersion()
	a.b.leases[ref] = memLease{holder: l.Holder, version: v, renewed: now}
	a.b.mu.Unlock()
	a.observe(l.Key, v)
	return ports.Lease{Key: l.Key, Holder: l.Holder, Version: v, Renewed: now}, nil
}

// Release implements ports.Arbiter.
func (a *Arbiter) Release(_ context.Context, l ports.Lease) error {
	ref, err := normKey(l.Key)
	if err != nil {
		return err
	}
	if l.Version == "" {
		return fmt.Errorf("%w: %s", ports.ErrNotHeld, l.Key)
	}
	if err := a.lockB(); err != nil {
		return err
	}
	cur, ok := a.b.leases[ref]
	if !ok || cur.version != l.Version {
		a.b.mu.Unlock()
		return fmt.Errorf("%w: %s", ports.ErrNotHeld, l.Key)
	}
	delete(a.b.leases, ref)
	a.b.mu.Unlock()
	a.forget(l.Key)
	return nil
}

// Inspect implements ports.Arbiter.
func (a *Arbiter) Inspect(_ context.Context, key string) (ports.LeaseInfo, error) {
	ref, err := normKey(key)
	if err != nil {
		return ports.LeaseInfo{}, err
	}
	if err := a.lockB(); err != nil {
		return ports.LeaseInfo{}, err
	}
	cur, ok := a.b.leases[ref]
	a.b.mu.Unlock()
	if !ok {
		a.forget(key)
		return ports.LeaseInfo{}, nil
	}
	return ports.LeaseInfo{Held: true, Holder: cur.holder, Version: cur.version, Renewed: cur.renewed, UnchangedSince: a.observe(key, cur.version)}, nil
}

// TakeOver implements ports.Arbiter.
func (a *Arbiter) TakeOver(ctx context.Context, key, holder string, ttl time.Duration) (ports.Lease, error) {
	ref, err := normKey(key)
	if err != nil {
		return ports.Lease{}, err
	}
	info, err := a.Inspect(ctx, key)
	if err != nil {
		return ports.Lease{}, err
	}
	if info.Held {
		if idle := a.clock.Now().Sub(info.UnchangedSince); idle <= ttl {
			return ports.Lease{}, fmt.Errorf("%w: %s renewed %s ago by local observation", ports.ErrTaken, key, idle.Round(time.Second))
		}
	}
	if err := a.lockB(); err != nil {
		return ports.Lease{}, err
	}
	cur, ok := a.b.leases[ref]
	if ok != info.Held || (ok && cur.version != info.Version) {
		a.b.mu.Unlock()
		return ports.Lease{}, fmt.Errorf("%w: %s", ports.ErrTaken, key)
	}
	now := a.clock.Now().UTC()
	v := a.nextVersion()
	a.b.leases[ref] = memLease{holder: holder, version: v, renewed: now}
	a.b.mu.Unlock()
	a.observe(key, v)
	return ports.Lease{Key: key, Holder: holder, Version: v, Renewed: now}, nil
}

// Next implements ports.Arbiter.
func (a *Arbiter) Next(_ context.Context, counter string) (int, error) {
	if _, err := normKey(counter); err != nil {
		return 0, err
	}
	if err := a.lockB(); err != nil {
		return 0, err
	}
	defer a.b.mu.Unlock()
	a.b.counters[counter]++
	return a.b.counters[counter], nil
}

// Integrate implements ports.Arbiter: fast-forward only, compare-and-swap on main.
func (a *Arbiter) Integrate(_ context.Context, expectedMain, next string) error {
	if err := a.lockB(); err != nil {
		return err
	}
	defer a.b.mu.Unlock()
	b := a.b
	if len(b.parents) > 0 {
		if _, ok := b.parents[expectedMain]; !ok {
			return fmt.Errorf("integrate: expected main %q not found", expectedMain)
		}
		if _, ok := b.parents[next]; !ok {
			return fmt.Errorf("integrate: %q not found", next)
		}
		if !b.ancestor(expectedMain, next) {
			return fmt.Errorf("%w: %s is not based on %s", ErrNotDescendant, next, expectedMain)
		}
	}
	if expectedMain == next {
		return nil
	}
	if b.main != expectedMain {
		return fmt.Errorf("%w: expected %s", ports.ErrStaleMain, expectedMain)
	}
	b.main = next
	return nil
}

// ancestor reports whether a is b or an ancestor of b. Callers hold b.mu.
func (b *ArbiterBackend) ancestor(a, c string) bool {
	for i := 0; c != "" && i <= len(b.parents); i++ {
		if c == a {
			return true
		}
		c = b.parents[c]
	}
	return false
}

// List implements ports.Arbiter: lease and counter keys under prefix, sorted.
func (a *Arbiter) List(_ context.Context, prefix string) ([]string, error) {
	if err := a.lockB(); err != nil {
		return nil, err
	}
	defer a.b.mu.Unlock()
	keys := []string{}
	for k := range a.b.leases {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	for k := range a.b.counters {
		if ck := "counters/" + k; strings.HasPrefix(ck, prefix) {
			keys = append(keys, ck)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// Holder returns who holds key ("" when free), without touching observations.
func (b *ArbiterBackend) Holder(key string) string {
	ref, err := normKey(key)
	if err != nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.leases[ref].holder
}

// Counter returns the last number a counter gave out.
func (b *ArbiterBackend) Counter(name string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.counters[name]
}

// SetCounter sets the last number a counter gave out.
func (b *ArbiterBackend) SetCounter(name string, n int) {
	b.mu.Lock()
	b.counters[name] = n
	b.mu.Unlock()
}
