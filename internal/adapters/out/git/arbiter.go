package git

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// Ref layout of the arbiter.
//
// A key names a ref under refs/barnraise/: the key "claims/I-42" lives in
// refs/barnraise/claims/I-42 and "lease/housekeeping" in
// refs/barnraise/lease/housekeeping. A key without a slash is an item claim,
// so "I-42" is the same as "claims/I-42" (Lease.Key keeps the key as given).
// Counters live in refs/barnraise/counters/<counter>; the message starts
// with the last number given out.
//
// Each lease ref points to a parentless commit with the empty tree whose
// message is JSON {"holder", "renewed", "nonce"}; the commit sha is the
// lease version used for compare-and-swap. With a remote the swap is an
// atomic push with --force-with-lease; without one it is git update-ref
// with the expected old value, so the local disk is the arbiter.
const (
	refRoot       = "refs/barnraise/"
	counterPrefix = "counters/"
	claimPrefix   = "claims/"
)

// ErrNotDescendant is returned by Integrate when next does not descend from
// the expected main.
var ErrNotDescendant = errors.New("next does not descend from main")

// errMismatch is a compare-and-swap whose expected value no longer holds.
var errMismatch = errors.New("ref changed")

const (
	casContentionRetries = 10
	nextRetries          = 64
)

var _ ports.Arbiter = (*Arbiter)(nil)

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type observation struct {
	version string
	since   time.Time
}

// Arbiter implements ports.Arbiter on git refs.
type Arbiter struct {
	repo  *Repo
	clock ports.Clock

	treeMu    sync.Mutex
	emptyTree string
	zeroOID   string

	mu   sync.Mutex
	seen map[string]observation // key -> version first seen locally and when
}

// NewArbiter returns an arbiter on repo's remote, or on repo itself when it
// has no remote. clock (nil means the system clock) measures how long a
// lease stays unchanged, by local observation only.
func NewArbiter(repo *Repo, clock ports.Clock) *Arbiter {
	if clock == nil {
		clock = realClock{}
	}
	return &Arbiter{repo: repo, clock: clock, seen: map[string]observation{}}
}

type leaseBody struct {
	Holder  string `json:"holder"`
	Renewed string `json:"renewed"`
	Nonce   string `json:"nonce,omitempty"`
}

// KeyRef returns the ref that holds key.
func KeyRef(key string) (string, error) {
	if err := validKey(key); err != nil {
		return "", err
	}
	if !strings.Contains(key, "/") {
		key = claimPrefix + key
	}
	return refRoot + key, nil
}

func validKey(key string) error {
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
		return fmt.Errorf("invalid arbiter key %q", key)
	}
	return nil
}

func (a *Arbiter) init(ctx context.Context) error {
	a.treeMu.Lock()
	defer a.treeMu.Unlock()
	if a.emptyTree != "" {
		return nil
	}
	out, err := run(ctx, a.repo.root, strings.NewReader(""), "mktree")
	if err != nil {
		return err
	}
	a.emptyTree = strings.TrimSpace(out)
	a.zeroOID = strings.Repeat("0", len(a.emptyTree))
	return nil
}

// writeObject stores a parentless empty-tree commit with message msg.
func (a *Arbiter) writeObject(ctx context.Context, msg string) (string, error) {
	if err := a.init(ctx); err != nil {
		return "", err
	}
	ts := strconv.FormatInt(a.clock.Now().Unix(), 10) + " +0000"
	ident := fallbackName + " <" + fallbackEmail + "> " + ts
	body := "tree " + a.emptyTree + "\nauthor " + ident + "\ncommitter " + ident + "\n\n" + msg + "\n"
	out, err := run(ctx, a.repo.root, strings.NewReader(body), "hash-object", "-t", "commit", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func nonce() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (a *Arbiter) writeLease(ctx context.Context, holder string) (string, time.Time, error) {
	now := a.clock.Now().UTC()
	msg, err := json.Marshal(leaseBody{Holder: holder, Renewed: now.Format(time.RFC3339Nano), Nonce: nonce()})
	if err != nil {
		return "", time.Time{}, err
	}
	sha, err := a.writeObject(ctx, string(msg))
	return sha, now, err
}

// readMessage returns the commit message of object sha.
func (a *Arbiter) readMessage(ctx context.Context, sha string) (string, error) {
	out, err := run(ctx, a.repo.root, nil, "cat-file", "commit", sha)
	if err != nil {
		return "", err
	}
	_, msg, _ := strings.Cut(out, "\n\n")
	return strings.TrimSpace(msg), nil
}

// readRef returns the value of ref at the arbiter.
func (a *Arbiter) readRef(ctx context.Context, ref string) (string, bool, error) {
	if !a.repo.HasRemote() {
		return a.repo.refValue(ctx, ref)
	}
	out, err := run(ctx, a.repo.root, nil, "ls-remote", a.repo.remote, ref)
	if err != nil {
		return "", false, fmt.Errorf("%w: %v", ports.ErrOffline, err)
	}
	sha, ok := parseLsRemote(out, ref)
	return sha, ok, nil
}

// hasObject reports whether sha is in the local object store.
func (a *Arbiter) hasObject(ctx context.Context, sha string) bool {
	_, err := run(ctx, a.repo.root, nil, "cat-file", "-e", sha+"^{commit}")
	return err == nil
}

// readValue reads ref and its message, fetching the object when needed.
func (a *Arbiter) readValue(ctx context.Context, ref string) (sha, msg string, ok bool, err error) {
	for range 5 {
		sha, ok, err = a.readRef(ctx, ref)
		if err != nil || !ok {
			return "", "", ok, err
		}
		if a.repo.HasRemote() && !a.hasObject(ctx, sha) {
			// Fetch the objects without updating any local ref, so concurrent
			// readers in the same clone never fight over ref locks.
			_, err = run(ctx, a.repo.root, nil, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", a.repo.remote, ref)
			if err != nil {
				return "", "", false, fmt.Errorf("%w: %v", ports.ErrOffline, err)
			}
			if !a.hasObject(ctx, sha) {
				continue // the ref moved between ls-remote and fetch
			}
		}
		msg, err = a.readMessage(ctx, sha)
		return sha, msg, true, err
	}
	return "", "", false, fmt.Errorf("%s keeps changing", ref)
}

// cas moves ref from old to new at the arbiter. old "" means the ref must not
// exist; new "" deletes it. It returns errMismatch when ref is not at old.
func (a *Arbiter) cas(ctx context.Context, ref, old, new string, noVerify bool) error {
	if err := a.init(ctx); err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		err := a.casOnce(ctx, ref, old, new, noVerify)
		if err == nil || !errors.Is(err, errContention) {
			return err
		}
		cur, ok, rerr := a.readRef(ctx, ref)
		if rerr != nil {
			return rerr
		}
		if (old == "" && ok) || (old != "" && (!ok || cur != old)) {
			return errMismatch
		}
		if attempt >= casContentionRetries {
			return fmt.Errorf("update %s: %w", ref, err)
		}
		sleep(ctx, time.Duration(1+mrand.IntN(5*(attempt+1)))*time.Millisecond)
	}
}

// errContention marks a refused swap whose cause is still unknown.
var errContention = errors.New("swap refused")

func (a *Arbiter) casOnce(ctx context.Context, ref, old, new string, noVerify bool) error {
	if !a.repo.HasRemote() {
		var err error
		switch {
		case new == "":
			_, err = a.repo.git(ctx, "", "update-ref", "-d", ref, old)
		case old == "":
			_, err = a.repo.git(ctx, "", "update-ref", ref, new, a.zeroOID)
		default:
			_, err = a.repo.git(ctx, "", "update-ref", ref, new, old)
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%w: %v", errContention, err)
		}
		return nil
	}
	args := []string{"push", "--porcelain", "--atomic"}
	if noVerify {
		args = append(args, "--no-verify")
	}
	args = append(args, "--force-with-lease="+ref+":"+old, a.repo.remote, new+":"+ref)
	out, err := a.repo.git(ctx, "", args...)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if rejected(out) {
			return fmt.Errorf("%w: %v", errContention, err)
		}
		return fmt.Errorf("%w: %v", ports.ErrOffline, err)
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// observe records the version of key and returns when this process first saw it.
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

// Claim creates the lease of key for holder; ports.ErrTaken if it exists.
func (a *Arbiter) Claim(ctx context.Context, key, holder string) (ports.Lease, error) {
	ref, err := KeyRef(key)
	if err != nil {
		return ports.Lease{}, err
	}
	sha, now, err := a.writeLease(ctx, holder)
	if err != nil {
		return ports.Lease{}, err
	}
	if err := a.cas(ctx, ref, "", sha, true); err != nil {
		if errors.Is(err, errMismatch) {
			return ports.Lease{}, fmt.Errorf("%w: %s", ports.ErrTaken, key)
		}
		return ports.Lease{}, err
	}
	a.observe(key, sha)
	return ports.Lease{Key: key, Holder: holder, Version: sha, Renewed: now}, nil
}

// Renew writes a new version of l; ports.ErrLost if l is no longer current.
func (a *Arbiter) Renew(ctx context.Context, l ports.Lease) (ports.Lease, error) {
	ref, err := KeyRef(l.Key)
	if err != nil {
		return ports.Lease{}, err
	}
	if l.Version == "" {
		return ports.Lease{}, fmt.Errorf("%w: %s has no version", ports.ErrLost, l.Key)
	}
	sha, now, err := a.writeLease(ctx, l.Holder)
	if err != nil {
		return ports.Lease{}, err
	}
	if err := a.cas(ctx, ref, l.Version, sha, true); err != nil {
		if errors.Is(err, errMismatch) {
			return ports.Lease{}, fmt.Errorf("%w: %s", ports.ErrLost, l.Key)
		}
		return ports.Lease{}, err
	}
	a.observe(l.Key, sha)
	return ports.Lease{Key: l.Key, Holder: l.Holder, Version: sha, Renewed: now}, nil
}

// Release deletes the lease if it is still at l's version; ports.ErrNotHeld
// otherwise.
func (a *Arbiter) Release(ctx context.Context, l ports.Lease) error {
	ref, err := KeyRef(l.Key)
	if err != nil {
		return err
	}
	if l.Version == "" {
		return fmt.Errorf("%w: %s", ports.ErrNotHeld, l.Key)
	}
	if err := a.cas(ctx, ref, l.Version, "", true); err != nil {
		if errors.Is(err, errMismatch) {
			return fmt.Errorf("%w: %s", ports.ErrNotHeld, l.Key)
		}
		return err
	}
	a.forget(l.Key)
	return nil
}

// Inspect reads the lease of key. UnchangedSince is when this process first
// saw the current version, by the injected clock.
func (a *Arbiter) Inspect(ctx context.Context, key string) (ports.LeaseInfo, error) {
	ref, err := KeyRef(key)
	if err != nil {
		return ports.LeaseInfo{}, err
	}
	sha, msg, ok, err := a.readValue(ctx, ref)
	if err != nil {
		return ports.LeaseInfo{}, err
	}
	if !ok {
		a.forget(key)
		return ports.LeaseInfo{}, nil
	}
	info := ports.LeaseInfo{Held: true, Version: sha, UnchangedSince: a.observe(key, sha)}
	var b leaseBody
	if json.Unmarshal([]byte(msg), &b) == nil {
		info.Holder = b.Holder
		info.Renewed, _ = time.Parse(time.RFC3339Nano, b.Renewed)
	}
	return info, nil
}

// TakeOver replaces the lease of key when this process has seen the same
// version for longer than ttl (or there is no lease). The holder's own
// timestamp is ignored, so clock skew between machines does not matter.
// It returns ports.ErrTaken while the lease is alive or when another
// machine took it first.
func (a *Arbiter) TakeOver(ctx context.Context, key, holder string, ttl time.Duration) (ports.Lease, error) {
	ref, err := KeyRef(key)
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
	sha, now, err := a.writeLease(ctx, holder)
	if err != nil {
		return ports.Lease{}, err
	}
	if err := a.cas(ctx, ref, info.Version, sha, true); err != nil {
		if errors.Is(err, errMismatch) {
			return ports.Lease{}, fmt.Errorf("%w: %s", ports.ErrTaken, key)
		}
		return ports.Lease{}, err
	}
	a.observe(key, sha)
	return ports.Lease{Key: key, Holder: holder, Version: sha, Renewed: now}, nil
}

// Next allocates the next number of counter (1 first). Concurrent callers on
// any clone get unique numbers.
func (a *Arbiter) Next(ctx context.Context, counter string) (int, error) {
	if err := validKey(counter); err != nil {
		return 0, err
	}
	ref := refRoot + counterPrefix + counter
	for attempt := range nextRetries {
		old, msg, ok, err := a.readValue(ctx, ref)
		if err != nil {
			return 0, err
		}
		n := 0
		if ok {
			first, _, _ := strings.Cut(msg, "\n")
			if n, err = strconv.Atoi(strings.TrimSpace(first)); err != nil {
				return 0, fmt.Errorf("counter %s: bad value %q", counter, msg)
			}
		}
		// The nonce keeps objects unique: two clones writing the same number
		// must not produce the same sha, or the second push would be a no-op
		// reported as success.
		sha, err := a.writeObject(ctx, strconv.Itoa(n+1)+"\n\n"+nonce())
		if err != nil {
			return 0, err
		}
		err = a.cas(ctx, ref, old, sha, true)
		if err == nil {
			return n + 1, nil
		}
		if !errors.Is(err, errMismatch) {
			return 0, err
		}
		sleep(ctx, time.Duration(mrand.IntN(2+3*min(attempt, 10)))*time.Millisecond)
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
	}
	return 0, fmt.Errorf("counter %s: too much contention", counter)
}

// Integrate moves main from expectedMain to next, only by fast-forward.
// ports.ErrStaleMain means main is no longer at expectedMain; ErrNotDescendant
// means next does not contain expectedMain. With a remote the push carries
// --force-with-lease on expectedMain, which together with the ancestry check
// makes it an exact compare-and-swap that is always a fast-forward for the
// server (branch protection still applies). The local main ref and the
// remote-tracking ref follow on success.
func (a *Arbiter) Integrate(ctx context.Context, expectedMain, next string) error {
	r := a.repo
	exp, ok, err := r.resolve(ctx, "", expectedMain)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("integrate: expected main %q not found", expectedMain)
	}
	nxt, ok, err := r.resolve(ctx, "", next)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("integrate: %q not found", next)
	}
	anc, err := r.isAncestor(ctx, exp, nxt)
	if err != nil {
		return err
	}
	if !anc {
		return fmt.Errorf("%w: %s is not based on %s", ErrNotDescendant, short(nxt), short(exp))
	}
	mainRef := "refs/heads/" + r.main
	if exp == nxt {
		return nil
	}
	if err := a.cas(ctx, mainRef, exp, nxt, false); err != nil {
		if errors.Is(err, errMismatch) {
			return fmt.Errorf("%w: expected %s", ports.ErrStaleMain, short(exp))
		}
		return err
	}
	if r.HasRemote() {
		_, _ = r.git(ctx, "", "update-ref", r.TrackingRef(r.main), nxt)
		cur, ok, err := r.resolve(ctx, "", mainRef)
		if err == nil && ok && cur != nxt {
			if anc, _ := r.isAncestor(ctx, cur, nxt); anc {
				if _, err := r.git(ctx, "", "update-ref", mainRef, nxt, cur); err == nil {
					r.syncCheckedOut(ctx, r.main, cur, nxt)
				}
			}
		}
		return nil
	}
	r.syncCheckedOut(ctx, r.main, exp, nxt)
	return nil
}

// syncCheckedOut fast-forwards the index and files of worktrees that have
// branch checked out after its ref moved from old to new. Best effort: a
// worktree with conflicting local changes is left alone.
func (r *Repo) syncCheckedOut(ctx context.Context, branch, old, new string) {
	wts, err := r.worktreesOn(ctx, branch)
	if err != nil {
		return
	}
	for _, wt := range wts {
		_, _ = r.git(ctx, wt, "read-tree", "-m", "-u", old, new)
	}
}

// List returns the keys under refs/barnraise/ that start with prefix, for
// example List(ctx, "claims/") gives "claims/I-42", "claims/I-43".
func (a *Arbiter) List(ctx context.Context, prefix string) ([]string, error) {
	var refs []string
	if a.repo.HasRemote() {
		out, err := run(ctx, a.repo.root, nil, "ls-remote", a.repo.remote, refRoot+"*")
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ports.ErrOffline, err)
		}
		for line := range strings.SplitSeq(out, "\n") {
			if _, name, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok {
				refs = append(refs, name)
			}
		}
	} else {
		out, err := a.repo.git(ctx, "", "for-each-ref", "--format=%(refname)", refRoot)
		if err != nil {
			return nil, err
		}
		refs = strings.Fields(out)
	}
	keys := []string{}
	for _, ref := range refs {
		key, ok := strings.CutPrefix(ref, refRoot)
		if ok && strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
