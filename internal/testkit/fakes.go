package testkit

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// CheckOutcome is one scripted check run.
type CheckOutcome struct {
	Passed  bool
	Summary string
	Flaky   bool
	Pending bool
	Err     error
}

// Pass and Fail are common outcomes.
var (
	Pass    = CheckOutcome{Passed: true, Summary: "ok"}
	Fail    = CheckOutcome{Passed: false, Summary: "failed"}
	Pending = CheckOutcome{Pending: true}
)

// Checks is a scripted ports.CheckRunner. Each check name has a queue of
// outcomes for branch runs and another for runs on main (spec.Main); the
// last outcome of a queue repeats. Unscripted checks pass.
type Checks struct {
	mu     sync.Mutex
	branch map[string][]CheckOutcome
	main   map[string][]CheckOutcome
	specs  []ports.CheckSpec
	// Func, when set, decides every run.
	Func func(spec ports.CheckSpec) (domain.CheckResult, bool, error)
}

var _ ports.CheckRunner = (*Checks)(nil)

// NewChecks returns a runner where every check passes.
func NewChecks() *Checks {
	return &Checks{branch: map[string][]CheckOutcome{}, main: map[string][]CheckOutcome{}}
}

// Script sets the outcomes of a check's branch runs, in order.
func (c *Checks) Script(name string, outs ...CheckOutcome) *Checks {
	c.mu.Lock()
	c.branch[name] = outs
	c.mu.Unlock()
	return c
}

// ScriptMain sets the outcomes of a check's runs on main.
func (c *Checks) ScriptMain(name string, outs ...CheckOutcome) *Checks {
	c.mu.Lock()
	c.main[name] = outs
	c.mu.Unlock()
	return c
}

// Specs returns every spec received.
func (c *Checks) Specs() []ports.CheckSpec {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ports.CheckSpec(nil), c.specs...)
}

// Run implements ports.CheckRunner.
func (c *Checks) Run(_ context.Context, spec ports.CheckSpec) (domain.CheckResult, bool, error) {
	c.mu.Lock()
	c.specs = append(c.specs, spec)
	fn := c.Func
	q := c.branch
	if spec.Main {
		q = c.main
	}
	name := spec.Def.Name
	var out CheckOutcome
	scripted := false
	if list := q[name]; len(list) > 0 {
		out, scripted = list[0], true
		if len(list) > 1 {
			q[name] = list[1:]
		}
	}
	c.mu.Unlock()
	if fn != nil {
		return fn(spec)
	}
	if !scripted {
		out = Pass
	}
	if out.Err != nil {
		return domain.CheckResult{}, false, out.Err
	}
	if out.Pending {
		return domain.CheckResult{}, true, nil
	}
	return domain.CheckResult{Check: name, ItemID: spec.ItemID, Commit: spec.Commit, Passed: out.Passed, Flaky: out.Flaky, Summary: out.Summary, RunsOn: spec.Def.RunsOn}, false, nil
}

// Notifier records notifications (ports.Notifier).
type Notifier struct {
	mu   sync.Mutex
	sent []ports.Notification
	Err  error
}

var _ ports.Notifier = (*Notifier)(nil)

// Notify implements ports.Notifier.
func (n *Notifier) Notify(_ context.Context, x ports.Notification) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sent = append(n.sent, x)
	return n.Err
}

// Sent returns the notifications so far.
func (n *Notifier) Sent() []ports.Notification {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]ports.Notification(nil), n.sent...)
}

// Kinds returns the kinds of the notifications so far.
func (n *Notifier) Kinds() []string {
	var out []string
	for _, x := range n.Sent() {
		out = append(out, x.Kind)
	}
	return out
}

// Tracker is a recording ports.Tracker whose List returns Items.
type Tracker struct {
	mu        sync.Mutex
	Items     []ports.TrackerItem
	created   []string
	updated   []string
	comments  []string
	n         int
	CreateErr error
	UpdateErr error
	ListErr   error
}

var _ ports.Tracker = (*Tracker)(nil)

// Name implements ports.Tracker.
func (t *Tracker) Name() string { return "memory" }

// List implements ports.Tracker.
func (t *Tracker) List(_ context.Context, since time.Time) ([]ports.TrackerItem, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ListErr != nil {
		return nil, t.ListErr
	}
	var out []ports.TrackerItem
	for _, it := range t.Items {
		if it.Updated.IsZero() || !it.Updated.Before(since) {
			out = append(out, it)
		}
	}
	return out, nil
}

// Create implements ports.Tracker.
func (t *Tracker) Create(_ context.Context, it *domain.Item, _ string) (ports.TrackerItem, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.CreateErr != nil {
		return ports.TrackerItem{}, t.CreateErr
	}
	t.n++
	t.created = append(t.created, it.ID)
	return ports.TrackerItem{Ref: fmt.Sprintf("mem#%d", t.n), Number: t.n, Title: "[" + it.ID + "] " + it.Title}, nil
}

// Update implements ports.Tracker.
func (t *Tracker) Update(_ context.Context, it *domain.Item, _ string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.updated = append(t.updated, it.ID+":"+string(it.Status))
	return t.UpdateErr
}

// Comment implements ports.Tracker.
func (t *Tracker) Comment(_ context.Context, ref, text string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.comments = append(t.comments, ref+": "+text)
	return nil
}

// Columns implements ports.Tracker.
func (t *Tracker) Columns(context.Context) ([]string, error) {
	return []string{"draft", "ready", "in_progress", "integrated"}, nil
}

// Created returns the item ids created in the tracker.
func (t *Tracker) Created() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.created...)
}

// Updated returns "id:status" of every update.
func (t *Tracker) Updated() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.updated...)
}

// CodeHost is a recording ports.CodeHost.
type CodeHost struct {
	mu      sync.Mutex
	calls   []string
	n       int
	OpenErr error
}

var _ ports.CodeHost = (*CodeHost)(nil)

func (h *CodeHost) record(format string, args ...any) {
	h.mu.Lock()
	h.calls = append(h.calls, fmt.Sprintf(format, args...))
	h.mu.Unlock()
}

// Calls returns the recorded calls, like "MarkReady 1".
func (h *CodeHost) Calls() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...)
}

// Count returns how many calls start with prefix.
func (h *CodeHost) Count(prefix string) int {
	n := 0
	for _, c := range h.Calls() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// Name implements ports.CodeHost.
func (h *CodeHost) Name() string { return "memory" }

// OpenDraftPR implements ports.CodeHost.
func (h *CodeHost) OpenDraftPR(_ context.Context, branch, base, title, _ string) (ports.PR, error) {
	h.record("OpenDraftPR %s %s %s", branch, base, title)
	if h.OpenErr != nil {
		return ports.PR{}, h.OpenErr
	}
	h.mu.Lock()
	h.n++
	n := h.n
	h.mu.Unlock()
	return ports.PR{Number: n, URL: fmt.Sprintf("https://host.test/pr/%d", n), State: "open", Draft: true, Head: branch}, nil
}

// UpdatePR implements ports.CodeHost.
func (h *CodeHost) UpdatePR(_ context.Context, pr ports.PR, title, _ string) error {
	h.record("UpdatePR %d %s", pr.Number, title)
	return nil
}

// MarkReady implements ports.CodeHost.
func (h *CodeHost) MarkReady(_ context.Context, pr ports.PR) error {
	h.record("MarkReady %d", pr.Number)
	return nil
}

// Comment implements ports.CodeHost.
func (h *CodeHost) Comment(_ context.Context, pr ports.PR, body string) error {
	h.record("Comment %d %s", pr.Number, firstLine(body))
	return nil
}

// Review implements ports.CodeHost.
func (h *CodeHost) Review(_ context.Context, pr ports.PR, approve bool, _ string) error {
	h.record("Review %d %v", pr.Number, approve)
	return nil
}

// ReportCheck implements ports.CodeHost.
func (h *CodeHost) ReportCheck(_ context.Context, sha, name string, passed bool, _ string) error {
	h.record("ReportCheck %s %s %v", sha, name, passed)
	return nil
}

// Merge implements ports.CodeHost.
func (h *CodeHost) Merge(_ context.Context, pr ports.PR, method string) (string, error) {
	h.record("Merge %d %s", pr.Number, method)
	return "merged", nil
}

// Close implements ports.CodeHost.
func (h *CodeHost) Close(_ context.Context, pr ports.PR, reason string) error {
	h.record("Close %d %s", pr.Number, reason)
	return nil
}

// DispatchWorkflow implements ports.CodeHost.
func (h *CodeHost) DispatchWorkflow(_ context.Context, workflow, ref string, _ map[string]string) error {
	h.record("DispatchWorkflow %s %s", workflow, ref)
	return nil
}

// Triage is a scripted ports.Triage: Verdicts maps item ids to verdicts;
// unscripted candidates are still valid.
type Triage struct {
	mu       sync.Mutex
	Verdicts map[string]domain.TriageResult
	Usage    domain.TokenUsage
	Err      error
	// Func, when set, decides every call.
	Func  func(ch domain.Change, items []*domain.Item) []domain.TriageResult
	calls [][]string
}

var _ ports.Triage = (*Triage)(nil)

// Set scripts one verdict.
func (t *Triage) Set(r domain.TriageResult) *Triage {
	t.mu.Lock()
	if t.Verdicts == nil {
		t.Verdicts = map[string]domain.TriageResult{}
	}
	t.Verdicts[r.ItemID] = r
	t.mu.Unlock()
	return t
}

// Calls returns the candidate ids of every call.
func (t *Triage) Calls() [][]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([][]string(nil), t.calls...)
}

// Triage implements ports.Triage.
func (t *Triage) Triage(_ context.Context, ch domain.Change, items []*domain.Item) ([]domain.TriageResult, domain.TokenUsage, error) {
	t.mu.Lock()
	var ids []string
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	t.calls = append(t.calls, ids)
	fn, err, usage := t.Func, t.Err, t.Usage
	verdicts := map[string]domain.TriageResult{}
	maps.Copy(verdicts, t.Verdicts)
	t.mu.Unlock()
	if err != nil {
		return nil, domain.TokenUsage{}, err
	}
	if fn != nil {
		return fn(ch, items), usage, nil
	}
	var out []domain.TriageResult
	for _, it := range items {
		if v, ok := verdicts[it.ID]; ok {
			v.ItemID = it.ID
			out = append(out, v)
			continue
		}
		out = append(out, domain.TriageResult{ItemID: it.ID, Verdict: domain.TriageStillValid, Reason: "unscripted"})
	}
	return out, usage, nil
}

// Graph is a static domain.CallGraph.
type Graph struct {
	CallersOf map[string][]string
	InFile    map[string][]string
}

// Callers implements domain.CallGraph.
func (g *Graph) Callers(symbol string) []string { return g.CallersOf[symbol] }

// SymbolsInFile implements domain.CallGraph.
func (g *Graph) SymbolsInFile(file string) []string { return g.InFile[file] }

// Knowledge is a minimal ports.Knowledge: documents by id, searched by
// substring of title, summary and body with the kind and area filters, plus
// rules by path prefix and a static graph.
type Knowledge struct {
	mu       sync.Mutex
	Docs     []ports.Doc
	Syms     map[string][]ports.Symbol
	LinkList []ports.Link
	G        *Graph
	RulesBy  map[string][]ports.Doc // path prefix -> docs
	indexed  int
	reindex  []string
}

var _ ports.Knowledge = (*Knowledge)(nil)

// Reindex implements ports.Knowledge.
func (k *Knowledge) Reindex(_ context.Context, project, _ string, commit string, changed []string) error {
	k.mu.Lock()
	k.reindex = append(k.reindex, project+"@"+commit+":"+strings.Join(changed, ","))
	k.mu.Unlock()
	return nil
}

// IndexItems implements ports.Knowledge.
func (k *Knowledge) IndexItems(_ context.Context, _ string, items []*domain.Item, proposals []*domain.Proposal) error {
	k.mu.Lock()
	k.indexed += len(items) + len(proposals)
	k.mu.Unlock()
	return nil
}

// Indexed returns how many items and proposals were indexed.
func (k *Knowledge) Indexed() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.indexed
}

// Reindexed returns "project@commit:files" of every Reindex call.
func (k *Knowledge) Reindexed() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.reindex...)
}

// Search implements ports.Knowledge.
func (k *Knowledge) Search(_ context.Context, _ string, query string, f ports.SearchFilters, limit int) ([]ports.SearchHit, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	q := strings.ToLower(query)
	var out []ports.SearchHit
	for _, d := range k.Docs {
		if f.Kind != "" && d.Kind != f.Kind {
			continue
		}
		if f.Area != "" && !domain.Contains(d.Areas, f.Area) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(d.ID+" "+d.Title+" "+d.Body+" "+strings.Join(d.Areas, " ")), q) {
			continue
		}
		out = append(out, ports.SearchHit{ID: d.ID, Kind: d.Kind, Title: d.Title, Summary: firstLine(d.Body), Path: d.Path, Tokens: domain.EstimateTokens(d.Body), Layer: "text"})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Get implements ports.Knowledge.
func (k *Knowledge) Get(_ context.Context, _ string, id string) (*ports.Doc, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, d := range k.Docs {
		if d.ID == id {
			dd := d
			return &dd, nil
		}
	}
	return nil, fmt.Errorf("document %s not found", id)
}

// Graph implements ports.Knowledge.
func (k *Knowledge) Graph() domain.CallGraph {
	if k.G == nil {
		return nil
	}
	return k.G
}

// Links implements ports.Knowledge.
func (k *Knowledge) Links(_ context.Context, _ string, id string) ([]ports.Link, error) {
	var out []ports.Link
	for _, l := range k.LinkList {
		if l.From == id || l.To == id {
			out = append(out, l)
		}
	}
	return out, nil
}

// Symbols implements ports.Knowledge.
func (k *Knowledge) Symbols(_ context.Context, _ string, file string) ([]ports.Symbol, error) {
	return k.Syms[file], nil
}

// Rules returns the standards of a path (the optional extension the app looks for).
func (k *Knowledge) Rules(_ string, path string) ([]ports.Doc, error) {
	var out []ports.Doc
	for prefix, docs := range k.RulesBy {
		if strings.HasPrefix(path, prefix) {
			out = append(out, docs...)
		}
	}
	return out, nil
}

// Exec is a scripted ports.Exec: results by command text, default exit 0.
type Exec struct {
	mu      sync.Mutex
	Results map[string]ports.ExecResult
	Err     error
	cmds    []ports.Cmd
}

var _ ports.Exec = (*Exec)(nil)

// Run implements ports.Exec.
func (e *Exec) Run(_ context.Context, c ports.Cmd) (ports.ExecResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cmds = append(e.cmds, c)
	key := strings.Join(append([]string{c.Name}, c.Args...), " ")
	if r, ok := e.Results[key]; ok {
		return r, e.Err
	}
	return ports.ExecResult{}, e.Err
}

// Cmds returns every command run.
func (e *Exec) Cmds() []ports.Cmd {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]ports.Cmd(nil), e.cmds...)
}
