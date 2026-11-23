package domain

import (
	"math"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// claimSim is one machine of a simulated cluster: a local projection plus
// the events other machines sent that it has not applied yet.
type claimSim struct {
	name    string
	state   *State
	pending []Event
}

// Property: machines claim and release one item concurrently, each acting on
// its own (possibly stale) projection and bumping the version. Events reach
// the others in any order. Once everything is delivered every machine agrees
// on the same snapshot, so there is never more than one owner of the item.
func TestPropertyNeverTwoOwners(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		base := time.Unix(1700000000, 0).UTC()
		nm := rapid.IntRange(2, 4).Draw(t, "machines")
		ms := make([]*claimSim, nm)
		seed := NewEvent("seed", EvItemCreated, "p", "I-1", "m0", "owner", base,
			Payload{Item: &Item{ID: "I-1", Version: 1, Status: StatusReady, UpdatedAt: base}})
		for i := range ms {
			ms[i] = &claimSim{name: FormatID(KindMemory, i+1), state: NewState("p")}
			if err := ms[i].state.Apply(seed); err != nil {
				t.Fatal(err)
			}
		}
		seq := 0
		steps := rapid.IntRange(1, 30).Draw(t, "steps")
		for i := 0; i < steps; i++ {
			m := ms[rapid.IntRange(0, nm-1).Draw(t, "actor")]
			switch rapid.IntRange(0, 2).Draw(t, "action") {
			case 0, 1: // act on the local view
				cur := m.state.Items["I-1"]
				next := cur.Clone()
				next.Version++
				// Clocks are coarse: equal timestamps happen.
				next.UpdatedAt = base.Add(time.Duration(rapid.IntRange(0, 2).Draw(t, "clock")) * time.Second)
				typ := ""
				switch {
				case cur.Status == StatusReady && cur.Claim == nil:
					next.Status = StatusInProgress
					next.Claim = &Claim{Machine: m.name, Since: next.UpdatedAt}
					typ = EvItemClaimed
				case cur.Claim != nil && cur.Claim.Machine == m.name:
					next.Status = StatusReady
					next.Claim = nil
					typ = EvItemReleased
				default:
					continue
				}
				seq++
				e := NewEvent(FormatID(KindDecision, seq), typ, "p", "I-1", m.name, "", next.UpdatedAt, Payload{Item: next})
				if err := m.state.Apply(e); err != nil {
					t.Fatal(err)
				}
				for _, o := range ms {
					if o != m {
						o.pending = append(o.pending, e)
					}
				}
			case 2: // deliver one pending event, in any order
				if len(m.pending) == 0 {
					continue
				}
				k := rapid.IntRange(0, len(m.pending)-1).Draw(t, "deliver")
				e := m.pending[k]
				m.pending = append(m.pending[:k], m.pending[k+1:]...)
				if err := m.state.Apply(e); err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, m := range ms {
			for _, e := range rapid.Permutation(m.pending).Draw(t, "flush") {
				if err := m.state.Apply(e); err != nil {
					t.Fatal(err)
				}
			}
			m.pending = nil
		}
		owner := func(s *State) string {
			if c := s.Items["I-1"].Claim; c != nil {
				return c.Machine
			}
			return ""
		}
		ref := ms[0].state.Items["I-1"]
		for _, m := range ms[1:] {
			got := m.state.Items["I-1"]
			if got.Version != ref.Version || owner(m.state) != owner(ms[0].state) || got.Status != ref.Status {
				t.Fatalf("machines disagree on the owner: %s sees v%d owner %q, %s sees v%d owner %q",
					ms[0].name, ref.Version, owner(ms[0].state), m.name, got.Version, owner(m.state))
			}
		}
		if (ref.Claim != nil) != (ref.Status == StatusInProgress) {
			t.Fatalf("claim and status out of step: %+v", ref)
		}
	})
}

// Property: with every lane always available, the greedy deficit choice
// converges to the configured shares.
func TestPropertyCapacityConverges(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		g := rapid.IntRange(1, 98).Draw(t, "goal")
		b := rapid.IntRange(1, 99-g).Draw(t, "bugs")
		c := Capacity{Goal: g, Bugs: b, Improvements: 100 - g - b}
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		all := map[Lane]bool{LaneGoal: true, LaneBugs: true, LaneImprovement: true, LaneSelf: true}
		usage := LaneUsage{}
		n := 1000
		for i := 0; i < n; i++ {
			l, ok := PickLane(c, usage, all, 0, false)
			if !ok || l == LaneSelf {
				t.Fatalf("pick %q %v", l, ok)
			}
			usage[l]++
		}
		for _, l := range []Lane{LaneGoal, LaneBugs, LaneImprovement} {
			got := float64(usage[l]) * 100 / float64(n)
			if math.Abs(got-float64(c.Share(l))) > 1 {
				t.Fatalf("lane %s got %.1f%%, share %d%% (usage %v)", l, got, c.Share(l), usage)
			}
		}
	})
}

// Property: FitBudget keeps the fixed order, never cuts order 1, cuts the
// least relevant parts first and fits the budget unless only order-1 parts
// are left.
func TestPropertyFitBudget(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 12).Draw(t, "n")
		parts := make([]ContextPart, n)
		for i := range parts {
			parts[i] = ContextPart{
				Order:     rapid.IntRange(1, 6).Draw(t, "order"),
				ID:        FormatID(KindItem, i+1),
				Body:      strings.Repeat("x", rapid.IntRange(0, 50).Draw(t, "body")),
				Tokens:    rapid.IntRange(0, 500).Draw(t, "tokens"),
				Relevance: float64(rapid.IntRange(0, 10).Draw(t, "rel")) / 10,
			}
		}
		budget := rapid.IntRange(-10, 3000).Draw(t, "budget")
		in := append([]ContextPart(nil), parts...)
		kept, cut := FitBudget(in, budget)
		if len(kept)+len(cut) != n {
			t.Fatalf("lost parts: %d + %d != %d", len(kept), len(cut), n)
		}
		total, order1 := 0, 0
		minKeptRel := math.Inf(1)
		for i, p := range kept {
			total += p.Tokens
			if p.Order == 1 {
				order1 += p.Tokens
			} else if p.Relevance < minKeptRel {
				minKeptRel = p.Relevance
			}
			if i > 0 && kept[i-1].Order > p.Order {
				t.Fatalf("order broken: %v", kept)
			}
		}
		for _, p := range cut {
			if p.Order == 1 {
				t.Fatalf("order 1 was cut: %+v", p)
			}
			if p.Relevance > minKeptRel {
				t.Fatalf("cut %s (relevance %.1f) while keeping a part with relevance %.1f", p.ID, p.Relevance, minKeptRel)
			}
		}
		if budget <= 0 && len(cut) > 0 {
			t.Fatal("no budget means no cut")
		}
		if budget > 0 && total > budget && total > order1 {
			t.Fatalf("kept %d tokens over budget %d (order-1 part %d)", total, budget, order1)
		}
	})
}

var globSegment = rapid.StringMatching(`[a-z0-9_.-]{1,6}`)

// pathSegment is a file name that is never read as a flag.
var pathSegment = rapid.StringMatching(`[a-z0-9_][a-z0-9_.-]{0,5}`)

// Property: MatchGlob on literal names is equality, ** spans directories and
// * never crosses a slash.
func TestPropertyMatchGlob(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		segs := rapid.SliceOfN(globSegment, 1, 5).Draw(t, "segs")
		name := strings.Join(segs, "/")
		if !MatchGlob(name, name) {
			t.Fatalf("literal %q does not match itself", name)
		}
		if !MatchGlob("**", name) {
			t.Fatalf("** does not match %q", name)
		}
		if !MatchGlob("**/"+segs[len(segs)-1], name) {
			t.Fatalf("**/last does not match %q", name)
		}
		if len(segs) > 1 {
			if !MatchGlob(segs[0]+"/**", name) {
				t.Fatalf("first/** does not match %q", name)
			}
			if MatchGlob("*", name) {
				t.Fatalf("* crosses a slash in %q", name)
			}
			if MatchGlob(segs[0], name) {
				t.Fatalf("prefix %q matches longer %q", segs[0], name)
			}
		}
		if !MatchGlob(strings.Repeat("*/", len(segs)-1)+"*", name) {
			t.Fatalf("one star per segment does not match %q", name)
		}
		other := rapid.SliceOfN(globSegment, 1, 5).Draw(t, "other")
		if o := strings.Join(other, "/"); o != name && MatchGlob(name, o) {
			t.Fatalf("literal %q matches different %q", name, o)
		}
		if MatchGlob("", name) || MatchGlob(name, "") {
			t.Fatal("empty pattern or name must not match")
		}
	})
}

// Property: Similarity is symmetric, bounded and 1 on itself.
func TestPropertySimilarity(t *testing.T) {
	words := rapid.SampledFrom([]string{"cards", "screen", "login", "slow", "cache", "quotes", "the", "não", "Fix", "crash", "42", "a"})
	text := rapid.Custom(func(t *rapid.T) string {
		return strings.Join(rapid.SliceOfN(words, 0, 8).Draw(t, "w"), rapid.SampledFrom([]string{" ", ", ", "-", "\n"}).Draw(t, "sep"))
	})
	rapid.Check(t, func(t *rapid.T) {
		a := rapid.OneOf(text, rapid.String()).Draw(t, "a")
		b := rapid.OneOf(text, rapid.String()).Draw(t, "b")
		ab, ba := Similarity(a, b), Similarity(b, a)
		if ab != ba {
			t.Fatalf("not symmetric: %v vs %v", ab, ba)
		}
		if ab < 0 || ab > 1 || math.IsNaN(ab) {
			t.Fatalf("out of range: %v", ab)
		}
		if len(Tokens(a)) > 0 && Similarity(a, a) != 1 {
			t.Fatalf("Similarity(a, a) = %v", Similarity(a, a))
		}
		if len(Tokens(a)) == 0 && ab != 0 {
			t.Fatal("no words means no similarity")
		}
	})
}

// Property: ParseID inverts FormatID, and FormatID normalizes a parsed id.
func TestPropertyIDRoundTrip(t *testing.T) {
	kinds := []IDKind{KindGoal, KindItem, KindProposal, KindDecision, KindMemory, KindQuestion}
	rapid.Check(t, func(t *rapid.T) {
		k := rapid.SampledFrom(kinds).Draw(t, "kind")
		n := rapid.IntRange(1, math.MaxInt32).Draw(t, "n")
		s := FormatID(k, n)
		gk, gn, err := ParseID(s)
		if err != nil || gk != k || gn != n {
			t.Fatalf("ParseID(%q) = %q %d %v", s, gk, gn, err)
		}
		if gk, gn, err := ParseID(" " + strings.ToLower(s) + " "); err != nil || FormatID(gk, gn) != s {
			t.Fatalf("lowercase %q does not normalize: %v", s, err)
		}
		if k == KindItem {
			for _, in := range []string{s, strings.TrimPrefix(s, "I-"), "#" + strings.TrimPrefix(s, "I-")} {
				if got, err := NormalizeItemID(in); err != nil || got != s {
					t.Fatalf("NormalizeItemID(%q) = %q %v", in, got, err)
				}
			}
		}
		raw := rapid.String().Draw(t, "raw")
		if k, n, err := ParseID(raw); err == nil {
			if n <= 0 {
				t.Fatalf("ParseID(%q) accepted %d", raw, n)
			}
			if k2, n2, err := ParseID(FormatID(k, n)); err != nil || k2 != k || n2 != n {
				t.Fatalf("re-parse of %q failed", raw)
			}
		}
	})
}

// Property: whatever the shape of the path, writes under denied globs are
// denied, and reads of credentials through any reader are denied.
func TestPropertyPolicyDeniesProtectedPaths(t *testing.T) {
	p := DefaultPolicy()
	p.Home, p.Worktree = "/home/u", "/w/I-1"
	rapid.Check(t, func(t *rapid.T) {
		tail := strings.Join(rapid.SliceOfN(pathSegment, 1, 3).Draw(t, "tail"), "/")
		prefix := rapid.SampledFrom([]string{"", "./", "/w/I-1/"}).Draw(t, "prefix")
		dir := rapid.SampledFrom([]string{".github/workflows/", ".barnraise/", ".git/"}).Draw(t, "dir")
		target := prefix + dir + tail
		if d := p.Decide(ToolCall{Tool: "Write", Paths: []string{target}, Write: true}, nil); d.Allow {
			t.Fatalf("write to %q allowed", target)
		}
		verb := rapid.SampledFrom([]string{"echo x >", "echo x >>", "echo x 2>", "echo x &>", "touch", "rm -f", "tee -a", "cp a.txt"}).Draw(t, "verb")
		cmd := verb + " " + target
		if d := p.Decide(ToolCall{Tool: "Bash", Command: cmd}, nil); d.Allow {
			t.Fatalf("command %q allowed", cmd)
		}
		secret := rapid.SampledFrom([]string{"~/.ssh/", "~/.aws/", "/home/u/.config/gh/", "~/.gnupg/"}).Draw(t, "secret") + tail
		reader := rapid.SampledFrom([]string{"cat", "head -n 5", "base64", "grep -r key", "tail -f", "sort <"}).Draw(t, "reader")
		if d := p.Decide(ToolCall{Tool: "Bash", Command: reader + " " + secret}, nil); d.Allow {
			t.Fatalf("reading %q with %q allowed", secret, reader)
		}
	})
}
