package domain

import (
	"maps"
	"time"
)

// EvMachineWorktrees carries a machine's inventory of worktrees. It is live
// state: only the latest report of each machine matters.
const EvMachineWorktrees = "machine.worktrees"

// Kinds of directory a machine keeps for a project.
const (
	WorktreeRoot    = "root"    // the project's own checkout
	WorktreeItem    = "item"    // an item's worktree
	WorktreeAlt     = "alt"     // the second attempt of a risky item
	WorktreeRefine  = "refine"  // the analyst's read-only checkout of a draft
	WorktreeScratch = "scratch" // a check for another machine, a look at main
	WorktreeRepro   = "repro"   // a reproduction of a change
	WorktreeTrash   = "trash"   // renamed for removal, not deleted yet
)

// WorktreeInfo is one directory a machine keeps for a project. Worktrees are
// local to a machine: this is how the others learn about them.
type WorktreeInfo struct {
	Machine string `json:"machine"`
	Project string `json:"project"`
	Name    string `json:"name"` // directory name
	Kind    string `json:"kind"`
	ItemID  string `json:"item_id,omitempty"`
	Path    string `json:"path"`
	Branch  string `json:"branch,omitempty"` // empty when detached
	Head    string `json:"head,omitempty"`   // commit checked out
	// InUse: a session or a check of this machine runs there right now.
	InUse bool `json:"in_use,omitempty"`
	// SetupDone: the project's worktree setup finished there (dependencies
	// are installed). False after the heavy directories were stripped.
	SetupDone  bool      `json:"setup_done,omitempty"`
	ModifiedAt time.Time `json:"modified_at,omitzero"`
	// SizeBytes and Files are measured in the background, at most every
	// half hour; SizedAt is zero while the first measure has not ended.
	SizeBytes int64     `json:"size_bytes,omitempty"`
	Files     int64     `json:"files,omitempty"`
	SizedAt   time.Time `json:"sized_at,omitzero"`
	// Procs is what runs there now; nil when nothing does.
	Procs *ProcSummary `json:"procs,omitempty"`
}

// WorktreeReport is what one machine keeps for one project, as it last said.
type WorktreeReport struct {
	Machine   string         `json:"machine"`
	Project   string         `json:"project"`
	At        time.Time      `json:"at,omitzero"`
	Worktrees []WorktreeInfo `json:"worktrees,omitempty"`
	// SizeBytes sums the measured worktrees (the project's root is not measured).
	SizeBytes int64 `json:"size_bytes,omitempty"`
	// The disk that holds the worktrees; 0 = the machine does not report it.
	DiskFreeBytes  int64 `json:"disk_free_bytes,omitempty"`
	DiskTotalBytes int64 `json:"disk_total_bytes,omitempty"`
	// ProcsSupported: this machine attributes processes to worktrees (Linux).
	ProcsSupported bool `json:"procs_supported,omitempty"`
	// SampledAt is when processes were last read; SampleMS what the read cost.
	SampledAt time.Time `json:"sampled_at,omitzero"`
	SampleMS  float64   `json:"sample_ms,omitempty"`
}

// UsageTotals adds up agent sessions: tokens, cost and time.
type UsageTotals struct {
	Input      int64   `json:"input"`
	Output     int64   `json:"output"`
	CacheRead  int64   `json:"cache_read"`
	CacheWrite int64   `json:"cache_write"`
	Tokens     int64   `json:"tokens"` // the four above
	CostUSD    float64 `json:"cost_usd,omitempty"`
	// Seconds of agent sessions (not of the clock: two agents count twice).
	Seconds float64 `json:"seconds"`
	// CPUSeconds is the processor time of everything the sessions ran (the
	// CLI, tests, browsers, builds); PeakRSSBytes the most memory at once.
	// Zero where the machine does not measure processes.
	CPUSeconds   float64   `json:"cpu_seconds,omitempty"`
	PeakRSSBytes int64     `json:"peak_rss_bytes,omitempty"`
	Sessions     int       `json:"sessions"`
	LastAt       time.Time `json:"last_at,omitzero"`
}

// Add counts one session.
func (t *UsageTotals) Add(u TokenUsage, at time.Time) {
	t.Input += u.Input
	t.Output += u.Output
	t.CacheRead += u.CacheRead
	t.CacheWrite += u.CacheWrite
	t.Tokens += u.Total()
	t.CostUSD += u.CostUSD
	t.Seconds += u.Duration.Seconds()
	t.CPUSeconds += u.CPUSeconds
	t.PeakRSSBytes = max(t.PeakRSSBytes, u.PeakRSSBytes)
	t.Sessions++
	if at.After(t.LastAt) {
		t.LastAt = at
	}
}

// Merge adds other totals to these.
func (t *UsageTotals) Merge(o UsageTotals) {
	t.Input += o.Input
	t.Output += o.Output
	t.CacheRead += o.CacheRead
	t.CacheWrite += o.CacheWrite
	t.Tokens += o.Tokens
	t.CostUSD += o.CostUSD
	t.Seconds += o.Seconds
	t.CPUSeconds += o.CPUSeconds
	t.PeakRSSBytes = max(t.PeakRSSBytes, o.PeakRSSBytes)
	t.Sessions += o.Sessions
	if o.LastAt.After(t.LastAt) {
		t.LastAt = o.LastAt
	}
}

// UsageBy is usage split by a name (a model, a machine, a step…).
type UsageBy map[string]*UsageTotals

func (b UsageBy) add(key string, u TokenUsage, at time.Time) {
	if key == "" {
		key = "-"
	}
	t := b[key]
	if t == nil {
		t = &UsageTotals{}
		b[key] = t
	}
	t.Add(u, at)
}

// Clone copies the split.
func (b UsageBy) Clone() UsageBy {
	if b == nil {
		return nil
	}
	c := make(UsageBy, len(b))
	for k, v := range b {
		t := *v
		c[k] = &t
	}
	return c
}

// MergeInto adds this split to dst.
func (b UsageBy) MergeInto(dst UsageBy) {
	for k, v := range b {
		t := dst[k]
		if t == nil {
			t = &UsageTotals{}
			dst[k] = t
		}
		t.Merge(*v)
	}
}

// CheckMark is the last result of one check of an item.
type CheckMark struct {
	Check   string    `json:"check"`
	Passed  bool      `json:"passed"`
	Flaky   bool      `json:"flaky,omitempty"`
	MainRed bool      `json:"main_red,omitempty"`
	Commit  string    `json:"commit,omitempty"`
	RunsOn  string    `json:"runs_on,omitempty"`
	Seconds float64   `json:"seconds,omitempty"`
	Runs    int       `json:"runs"`
	At      time.Time `json:"at,omitzero"`
}

// ReviewMark is the last review of an item.
type ReviewMark struct {
	Step     string    `json:"step"`
	Reviewer string    `json:"reviewer"`
	Decision string    `json:"decision"`
	Criteria string    `json:"criteria_verdict"`
	Code     string    `json:"code_verdict"`
	Findings int       `json:"findings"`
	At       time.Time `json:"at,omitzero"`
}

// DeliveryMark is the last delivery of an item.
type DeliveryMark struct {
	Step   string    `json:"step"`
	Agent  string    `json:"agent"`
	Status string    `json:"status"`
	Commit string    `json:"commit,omitempty"`
	At     time.Time `json:"at,omitzero"`
}

// Activity is the last thing recorded about an item.
type Activity struct {
	Type    string    `json:"type"`
	Machine string    `json:"machine,omitempty"`
	Actor   string    `json:"actor,omitempty"`
	At      time.Time `json:"at,omitzero"`
}

// ItemLedger is what the log says an item cost and went through.
type ItemLedger struct {
	Usage        UsageTotals           `json:"usage"`
	ByStep       UsageBy               `json:"by_step,omitempty"`
	ByModel      UsageBy               `json:"by_model,omitempty"`
	ByMachine    UsageBy               `json:"by_machine,omitempty"`
	ByRole       UsageBy               `json:"by_role,omitempty"`
	Checks       map[string]*CheckMark `json:"checks,omitempty"`
	Reviews      int                   `json:"reviews"`
	LastReview   *ReviewMark           `json:"last_review,omitempty"`
	Deliveries   int                   `json:"deliveries"`
	LastDelivery *DeliveryMark         `json:"last_delivery,omitempty"`
	Last         *Activity             `json:"last_activity,omitempty"`
}

// Clone copies the item's ledger.
func (l *ItemLedger) Clone() *ItemLedger {
	c := *l
	c.ByStep, c.ByModel, c.ByMachine, c.ByRole = l.ByStep.Clone(), l.ByModel.Clone(), l.ByMachine.Clone(), l.ByRole.Clone()
	if l.Checks != nil {
		c.Checks = make(map[string]*CheckMark, len(l.Checks))
		for k, v := range l.Checks {
			m := *v
			c.Checks[k] = &m
		}
	}
	if l.LastReview != nil {
		r := *l.LastReview
		c.LastReview = &r
	}
	if l.LastDelivery != nil {
		d := *l.LastDelivery
		c.LastDelivery = &d
	}
	if l.Last != nil {
		a := *l.Last
		c.Last = &a
	}
	return &c
}

// Ledger adds up the log of a project as it is applied: what each item,
// machine, account, model and day cost. It is derived state, the same on
// every machine, so a question about cost never reads the log again.
type Ledger struct {
	Total     UsageTotals            `json:"total"`
	ByItem    map[string]*ItemLedger `json:"by_item,omitempty"`
	ByMachine UsageBy                `json:"by_machine,omitempty"`
	ByAccount UsageBy                `json:"by_account,omitempty"`
	ByModel   UsageBy                `json:"by_model,omitempty"`
	ByRole    UsageBy                `json:"by_role,omitempty"`
	ByAgent   UsageBy                `json:"by_agent,omitempty"`
	ByDay     UsageBy                `json:"by_day,omitempty"` // UTC day, 2006-01-02
}

// NewLedger creates an empty ledger.
func NewLedger() *Ledger {
	return &Ledger{ByItem: map[string]*ItemLedger{}, ByMachine: UsageBy{}, ByAccount: UsageBy{}, ByModel: UsageBy{}, ByRole: UsageBy{}, ByAgent: UsageBy{}, ByDay: UsageBy{}}
}

// Clone copies the ledger.
func (l *Ledger) Clone() *Ledger {
	c := &Ledger{Total: l.Total, ByItem: make(map[string]*ItemLedger, len(l.ByItem)),
		ByMachine: l.ByMachine.Clone(), ByAccount: l.ByAccount.Clone(), ByModel: l.ByModel.Clone(), ByRole: l.ByRole.Clone(), ByAgent: l.ByAgent.Clone(), ByDay: l.ByDay.Clone()}
	for k, v := range l.ByItem {
		c.ByItem[k] = v.Clone()
	}
	return c
}

func (l *Ledger) item(id string) *ItemLedger {
	il := l.ByItem[id]
	if il == nil {
		il = &ItemLedger{ByStep: UsageBy{}, ByModel: UsageBy{}, ByMachine: UsageBy{}, ByRole: UsageBy{}}
		l.ByItem[id] = il
	}
	return il
}

// apply folds one stored event. Live-only events never reach it.
func (l *Ledger) apply(e Event, p Payload) {
	itemID := ""
	switch {
	case p.Usage != nil:
		itemID = p.Usage.ItemID
	case p.Delivery != nil:
		itemID = p.Delivery.ItemID
	case p.Review != nil:
		itemID = p.Review.ItemID
	case p.Check != nil:
		itemID = p.Check.ItemID
	case p.Journal != nil:
		itemID = p.Journal.ItemID
	}
	if itemID == "" && p.Item != nil {
		itemID = p.Item.ID
	}
	var il *ItemLedger
	if itemID != "" {
		il = l.item(itemID)
		if il.Last == nil || !e.Time.Before(il.Last.At) {
			il.Last = &Activity{Type: e.Type, Machine: e.Machine, Actor: e.Actor, At: e.Time}
		}
	}
	if u := p.Usage; u != nil && e.Type == EvAgentUsage {
		model := u.Model
		if u.Provider != "" {
			model = u.Provider + "/" + u.Model
		}
		l.Total.Add(*u, e.Time)
		l.ByMachine.add(e.Machine, *u, e.Time)
		l.ByAccount.add(u.Account, *u, e.Time)
		l.ByModel.add(model, *u, e.Time)
		l.ByRole.add(u.Role, *u, e.Time)
		l.ByAgent.add(u.Agent, *u, e.Time)
		l.ByDay.add(e.Time.UTC().Format(time.DateOnly), *u, e.Time)
		if il != nil {
			il.Usage.Add(*u, e.Time)
			il.ByStep.add(u.Step, *u, e.Time)
			il.ByModel.add(model, *u, e.Time)
			il.ByMachine.add(e.Machine, *u, e.Time)
			il.ByRole.add(u.Role, *u, e.Time)
		}
	}
	if il == nil {
		return
	}
	if c := p.Check; c != nil && e.Type == EvCheckCompleted {
		if il.Checks == nil {
			il.Checks = map[string]*CheckMark{}
		}
		runs := 0
		if prev := il.Checks[c.Check]; prev != nil {
			runs = prev.Runs
		}
		il.Checks[c.Check] = &CheckMark{Check: c.Check, Passed: c.Passed, Flaky: c.Flaky, MainRed: c.MainRed, Commit: c.Commit, RunsOn: c.RunsOn,
			Seconds: c.Duration.Seconds(), Runs: runs + 1, At: firstTime(c.At, e.Time)}
	}
	if r := p.Review; r != nil && e.Type == EvReviewSubmitted {
		il.Reviews++
		il.LastReview = &ReviewMark{Step: r.Step, Reviewer: r.Reviewer, Decision: string(r.Decision), Criteria: string(r.Criteria), Code: string(r.Code),
			Findings: len(r.Findings), At: firstTime(r.At, e.Time)}
	}
	if d := p.Delivery; d != nil && e.Type == EvDeliverySubmitted {
		il.Deliveries++
		il.LastDelivery = &DeliveryMark{Step: d.Step, Agent: d.Agent, Status: string(d.Status), Commit: d.Commit, At: firstTime(d.At, e.Time)}
	}
}

func firstTime(a, b time.Time) time.Time {
	if !a.IsZero() {
		return a
	}
	return b
}

// CloneWorktrees copies the map of reports. A report is replaced whole,
// never changed, so the reports themselves are shared.
func CloneWorktrees(m map[string]*WorktreeReport) map[string]*WorktreeReport {
	return maps.Clone(m)
}

// Live reports whether a session is running, as far as its last word says.
func (s *SessionInfo) Live() bool {
	switch s.State {
	case "starting", "working", "waiting":
		return true
	}
	return false
}

// ProcInfo is one process that runs for a worktree: the agent's CLI, a
// test, a browser, a build, an emulator.
type ProcInfo struct {
	PID  int    `json:"pid"`
	Name string `json:"name"`
	// Command is the program and its first plain arguments. Options are left
	// out: they are where keys and tokens travel.
	Command string `json:"command,omitempty"`
	// Kind: agent, test, browser, build, emulator, runtime, container, git, shell or other.
	Kind string `json:"kind"`
	// CPUPct is the share of one processor used since the previous sample
	// (200 = two processors busy).
	CPUPct     float64   `json:"cpu_pct"`
	RSSBytes   int64     `json:"rss_bytes"`
	CPUSeconds float64   `json:"cpu_seconds"` // since it started
	StartedAt  time.Time `json:"started_at,omitzero"`
}

// ProcSummary measures everything that runs for one worktree.
type ProcSummary struct {
	Count    int            `json:"count"`
	CPUPct   float64        `json:"cpu_pct"`
	RSSBytes int64          `json:"rss_bytes"`
	ByKind   map[string]int `json:"by_kind,omitempty"`
	// Top are the heaviest processes, a few: the rest is in the totals.
	Top []ProcInfo `json:"top,omitempty"`
	// CPUSeconds and PeakRSSBytes are measured for the worktree since the
	// machine's daemon started; they stay after the processes end.
	CPUSeconds   float64 `json:"cpu_seconds,omitempty"`
	PeakRSSBytes int64   `json:"peak_rss_bytes,omitempty"`
}

// Add adds every total of o into u.
func (u UsageBy) Add(o UsageBy) {
	for k, t := range o {
		if t == nil {
			continue
		}
		if c := u[k]; c != nil {
			c.Merge(*t)
		} else {
			cp := *t
			u[k] = &cp
		}
	}
}
