package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Change is something that may invalidate items: an integration, a new
// decision, a changed spec, a person editing the tracker.
type Change struct {
	Kind      string   `json:"kind"`  // integration, goal, focus, decision, spec, dependency, tracker, sweep
	Cause     string   `json:"cause"` // link: I-41 merge commit, D-12, tracker url
	Commit    string   `json:"commit,omitempty"`
	Files     []string `json:"files,omitempty"`
	Symbols   []string `json:"symbols,omitempty"`
	Areas     []string `json:"areas,omitempty"`
	Contracts []string `json:"contracts,omitempty"`
	Decisions []string `json:"decisions,omitempty"`
	Specs     []string `json:"specs,omitempty"`
	Items     []string `json:"items,omitempty"` // dependencies cancelled, split or changed
	Summary   string   `json:"summary,omitempty"`
	SourceID  string   `json:"source_id,omitempty"` // item that produced the change, excluded
}

// Impact is a candidate item and why it was picked.
type Impact struct {
	ItemID  string   `json:"item_id"`
	Reasons []string `json:"reasons"`
	Score   int      `json:"score"`
}

// CallGraph answers "who calls this symbol" (one level), from the code map.
type CallGraph interface {
	Callers(symbol string) []string
	SymbolsInFile(file string) []string
}

// FindImpacted crosses a change with the anchors of all live items without
// any model: milliseconds for a thousand items, zero tokens.
func FindImpacted(c Change, items []*Item, graph CallGraph) []Impact {
	changedFiles := setOf(c.Files)
	changedSymbols := setOf(c.Symbols)
	if graph != nil {
		for f := range changedFiles {
			for _, s := range graph.SymbolsInFile(f) {
				changedSymbols[s] = true
			}
		}
	}
	callers := map[string]bool{}
	if graph != nil {
		for s := range changedSymbols {
			for _, cl := range graph.Callers(s) {
				callers[cl] = true
			}
		}
	}
	areas := map[string]bool{}
	for _, a := range c.Areas {
		areas[AreaName(a)] = true
	}
	contracts, decisions, specs, deps := setOf(c.Contracts), setOf(c.Decisions), setOf(c.Specs), setOf(c.Items)
	var out []Impact
	for _, it := range items {
		if it.Status.Terminal() || it.ID == c.SourceID {
			continue
		}
		var reasons []string
		score := 0
		for _, f := range it.Anchors.Files {
			if changedFiles[f] {
				reasons = append(reasons, "file "+f)
				score += 3
				continue
			}
			for cf := range changedFiles {
				if MatchGlob(f, cf) || (strings.HasSuffix(f, "/") && strings.HasPrefix(cf, f)) {
					reasons = append(reasons, "file "+cf+" under "+f)
					score += 2
					break
				}
			}
		}
		for _, s := range it.Anchors.Symbols {
			if changedSymbols[s] {
				reasons = append(reasons, "symbol "+s)
				score += 3
			} else if callers[s] {
				reasons = append(reasons, "caller "+s+" of a changed symbol")
				score++
			}
		}
		for _, k := range it.Anchors.Contracts {
			if contracts[k] {
				reasons = append(reasons, "contract "+k)
				score += 3
			}
		}
		for _, d := range it.Anchors.Decisions {
			if decisions[d] {
				reasons = append(reasons, "decision "+d)
				score += 2
			}
		}
		for _, sp := range it.Anchors.Specs {
			if specs[sp] {
				reasons = append(reasons, "spec "+sp)
				score += 2
			}
		}
		for _, d := range it.Deps {
			if deps[d] {
				reasons = append(reasons, "dependency "+d)
				score += 3
			}
		}
		if len(areas) > 0 && (c.Kind == "decision" || c.Kind == "spec" || c.Kind == "goal" || c.Kind == "focus") {
			for _, a := range append([]string{it.Area}, it.Anchors.Areas...) {
				if a != "" && areas[AreaName(a)] {
					reasons = append(reasons, "area "+AreaName(a))
					score++
					break
				}
			}
		}
		if len(reasons) > 0 {
			out = append(out, Impact{ItemID: it.ID, Reasons: dedupe(reasons), Score: score})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return idNum(out[i].ItemID) < idNum(out[j].ItemID)
	})
	return out
}

// StaleItems returns live items not validated for longer than maxAge (nightly sweep).
func StaleItems(items []*Item, now time.Time, maxAge time.Duration) []*Item {
	var out []*Item
	for _, it := range items {
		if it.Status.Terminal() || it.Status == StatusInProgress {
			continue
		}
		ref := it.ValidatedAt
		if ref.IsZero() {
			ref = it.CreatedAt
		}
		if now.Sub(ref) > maxAge {
			out = append(out, it)
		}
	}
	SortItems(out)
	return out
}

// TriageVerdict is the fixed answer of the cheap triage model.
type TriageVerdict string

const (
	TriageStillValid    TriageVerdict = "still_valid"
	TriageNeedsUpdate   TriageVerdict = "needs_update"
	TriageMaybeDone     TriageVerdict = "maybe_done"
	TriageObsolete      TriageVerdict = "obsolete"
	TriageDuplicate     TriageVerdict = "duplicate"
	TriageNewDependency TriageVerdict = "new_dependency"
	TriageConflict      TriageVerdict = "conflict"
)

// TriageResult is one verdict for one candidate.
type TriageResult struct {
	ItemID      string        `json:"item_id"`
	Verdict     TriageVerdict `json:"verdict"`
	Reason      string        `json:"reason"`
	Criteria    []Criterion   `json:"criteria,omitempty"` // for needs_update
	Anchors     *Anchors      `json:"anchors,omitempty"`  // for needs_update
	Target      string        `json:"target,omitempty"`   // duplicate of / depends on
	ScopeChange bool          `json:"scope_change,omitempty"`
}

// Validate checks the verdict shape.
func (t TriageResult) Validate() error {
	switch t.Verdict {
	case TriageStillValid, TriageMaybeDone, TriageObsolete, TriageConflict:
	case TriageNeedsUpdate:
		if len(t.Criteria) == 0 && t.Anchors == nil {
			return fmt.Errorf("%s: needs_update must bring new criteria or anchors", t.ItemID)
		}
	case TriageDuplicate, TriageNewDependency:
		if t.Target == "" {
			return fmt.Errorf("%s: %s needs a target item", t.ItemID, t.Verdict)
		}
	default:
		return fmt.Errorf("%s: unknown verdict %q", t.ItemID, t.Verdict)
	}
	if strings.TrimSpace(t.Reason) == "" {
		return fmt.Errorf("%s: verdict needs a reason", t.ItemID)
	}
	return nil
}

// FilesOverlap reports whether two items are predicted to touch the same files.
func FilesOverlap(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y || MatchGlob(x, y) || MatchGlob(y, x) {
				return true
			}
		}
	}
	return false
}

// PredictedFiles is what the scheduler uses to keep two agents off the same files.
// Every item has its own plan.md and criteria.json, and Barnraise refreshes the
// repository map on every branch: none of them is shared work, and counting
// them made any item that had delivered once wait for every other one running
// (I-72 stood ready a day behind I-84 for its plan.md).
func PredictedFiles(it *Item) []string {
	files := it.Anchors.Files
	if len(it.FilesTouched) > 0 {
		files = append(append([]string(nil), it.FilesTouched...), it.Anchors.Files...)
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		switch f {
		case "plan.md", "criteria.json", RepoMapFile:
			continue
		}
		out = append(out, f)
	}
	return out
}

func setOf(s []string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		if v != "" {
			m[v] = true
		}
	}
	return m
}

func dedupe(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// Dedupe is exported for other layers.
func Dedupe(s []string) []string { return dedupe(s) }

// EstimateTokens approximates tokens of text (4 chars per token).
func EstimateTokens(s string) int { return (len(s) + 3) / 4 }

// ContextPart is one ordered part of an item's context pack.
type ContextPart struct {
	Order     int     `json:"order"` // fixed order, good for the cache
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	Title     string  `json:"title"`
	Body      string  `json:"body"`
	Relevance float64 `json:"relevance"`
	Tokens    int     `json:"tokens"`
}

// FitBudget keeps the fixed order and cuts the least relevant parts first
// until the pack fits the budget. Order 1 (the item itself) is never cut.
func FitBudget(parts []ContextPart, budget int) (kept []ContextPart, cut []ContextPart) {
	total := 0
	for i := range parts {
		if parts[i].Tokens == 0 {
			parts[i].Tokens = EstimateTokens(parts[i].Body) + EstimateTokens(parts[i].Title)
		}
		total += parts[i].Tokens
	}
	drop := map[int]bool{}
	if budget > 0 && total > budget {
		idx := make([]int, len(parts))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool {
			if parts[idx[a]].Relevance != parts[idx[b]].Relevance {
				return parts[idx[a]].Relevance < parts[idx[b]].Relevance
			}
			return parts[idx[a]].Order > parts[idx[b]].Order
		})
		for _, i := range idx {
			if total <= budget {
				break
			}
			if parts[i].Order == 1 {
				continue
			}
			drop[i] = true
			total -= parts[i].Tokens
		}
	}
	for i, p := range parts {
		if drop[i] {
			cut = append(cut, p)
		} else {
			kept = append(kept, p)
		}
	}
	sort.SliceStable(kept, func(a, b int) bool { return kept[a].Order < kept[b].Order })
	return kept, cut
}

// RRF fuses ranked lists by reciprocal rank (k=60): no single layer dominates.
func RRF(lists ...[]string) []string {
	score := map[string]float64{}
	first := map[string]int{}
	n := 0
	for _, l := range lists {
		for r, id := range l {
			score[id] += 1 / (60 + float64(r+1))
			if _, ok := first[id]; !ok {
				first[id] = n
				n++
			}
		}
	}
	out := make([]string, 0, len(score))
	for id := range score {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool {
		if score[out[i]] != score[out[j]] {
			return score[out[i]] > score[out[j]]
		}
		return first[out[i]] < first[out[j]]
	})
	return out
}

// RecallAtK is the share of relevant ids found in the first k results.
func RecallAtK(results, relevant []string, k int) float64 {
	if len(relevant) == 0 {
		return 1
	}
	if k > len(results) {
		k = len(results)
	}
	top := setOf(results[:k])
	hit := 0
	for _, r := range relevant {
		if top[r] {
			hit++
		}
	}
	return float64(hit) / float64(len(relevant))
}
