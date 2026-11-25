package app

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// What an item is doing right now, beyond its status.
const (
	ActWorking      = "working"       // an agent session runs on it
	ActRefining     = "refining"      // the analyst is refining the draft
	ActRunning      = "running"       // no agent, but processes run for it (a check, a build)
	ActIdle         = "idle"          // held by a machine, nothing runs (between steps, no capacity)
	ActStalled      = "stalled"       // the machine that holds it is out
	ActWaitingOwner = "waiting_owner" // a person has to decide
	ActBlocked      = "blocked"       // waits for another item
	ActPaused       = "paused"        // the item or the whole project is paused
	ActParked       = "parked"        // waits for a focus to end
	ActQueued       = "queued"        // in the merge queue
	ActReady        = "ready"         // can be claimed
	ActDraft        = "draft"         // not ready yet
	ActIntegrated   = "integrated"
	ActClosed       = "closed"
)

var activityRank = map[string]int{ActWorking: 0, ActRefining: 1, ActRunning: 2, ActIdle: 3, ActStalled: 4, ActWaitingOwner: 5, ActBlocked: 6,
	ActPaused: 7, ActQueued: 8, ActReady: 9, ActParked: 10, ActDraft: 11, ActIntegrated: 12, ActClosed: 13}

// Limits of one overview answer.
const (
	overviewLimit    = 500
	overviewLimitMax = 5000
	overviewFinished = 7 * 24 * time.Hour
	overviewDays     = 30
	sessionFresh     = 6 * time.Hour
)

// OverviewQuery chooses what an overview shows.
type OverviewQuery struct {
	Project  string        // "" = every project the caller may read
	Item     string        // only this item
	Activity []string      // only these activities or statuses
	Machine  string        // only what is on this machine
	Finished time.Duration // integrated and closed items of this long ago (default 7 days)
	All      bool          // every finished item, whatever its age
	Limit    int           // items per project (default 500, at most 5000)
	Offset   int
	Brief    bool // without the per-item breakdowns and process lists
}

// PRView is an item's pull request. State is derived from the item, not
// asked to the host: draft, ready, merged or closed.
type PRView struct {
	Number int    `json:"number"`
	URL    string `json:"url,omitempty"`
	State  string `json:"state"`
}

// ItemOverview is everything known about one item, on every machine.
type ItemOverview struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Kind     string `json:"kind"`
	Priority int    `json:"priority"`
	Lane     string `json:"lane,omitempty"`
	Area     string `json:"area,omitempty"`
	GoalID   string `json:"goal_id,omitempty"`
	Size     string `json:"size,omitempty"`
	Class    string `json:"class,omitempty"`
	Status   string `json:"status"`
	Step     string `json:"step,omitempty"`
	Activity string `json:"activity"`
	// Detail says why it is where it is, in a sentence, when there is a why.
	Detail string `json:"detail,omitempty"`
	// Machine is where the item is being worked on (its claim).
	Machine         string        `json:"machine,omitempty"`
	Claim           *domain.Claim `json:"claim,omitempty"`
	Branch          string        `json:"branch,omitempty"`
	PR              *PRView       `json:"pr,omitempty"`
	TrackerRef      string        `json:"tracker_ref,omitempty"`
	Adopted         bool          `json:"adopted,omitempty"`
	Paused          bool          `json:"paused,omitempty"`
	Parked          bool          `json:"parked,omitempty"`
	Risky           bool          `json:"risky,omitempty"`
	Tier            int           `json:"tier,omitempty"`
	Attempts        int           `json:"attempts,omitempty"`
	ReworkRounds    int           `json:"rework_rounds,omitempty"`
	Deps            []string      `json:"deps,omitempty"`
	BlockedBy       []string      `json:"blocked_by,omitempty"`
	WaitingQuestion string        `json:"waiting_question,omitempty"`
	CriteriaMet     int           `json:"criteria_met"`
	CriteriaTotal   int           `json:"criteria_total"`
	CreatedAt       time.Time     `json:"created_at,omitzero"`
	UpdatedAt       time.Time     `json:"updated_at,omitzero"`
	StepStartedAt   time.Time     `json:"step_started_at,omitzero"`
	IntegratedAt    time.Time     `json:"integrated_at,omitzero"`
	MergeCommit     string        `json:"merge_commit,omitempty"`
	ClosedReason    string        `json:"closed_reason,omitempty"`
	ClosedBy        string        `json:"closed_by,omitempty"`
	// StepSeconds: how long it is at the current step. QuietSeconds: since
	// anything changed on it. LeadSeconds: from creation to integration (or to now).
	StepSeconds  float64 `json:"step_seconds,omitempty"`
	QuietSeconds float64 `json:"quiet_seconds,omitempty"`
	LeadSeconds  float64 `json:"lead_seconds,omitempty"`

	Sessions  []*domain.SessionInfo `json:"sessions,omitempty"`
	Worktrees []domain.WorktreeInfo `json:"worktrees,omitempty"`
	// Procs adds up what runs for the item on every machine.
	Procs *domain.ProcSummary `json:"procs,omitempty"`

	Usage          domain.UsageTotals        `json:"usage"`
	UsageByStep    domain.UsageBy            `json:"usage_by_step,omitempty"`
	UsageByModel   domain.UsageBy            `json:"usage_by_model,omitempty"`
	UsageByMachine domain.UsageBy            `json:"usage_by_machine,omitempty"`
	UsageByRole    domain.UsageBy            `json:"usage_by_role,omitempty"`
	Runs           map[string]domain.StepRun `json:"runs,omitempty"`
	Checks         []*domain.CheckMark       `json:"checks,omitempty"`
	Reviews        int                       `json:"reviews,omitempty"`
	LastReview     *domain.ReviewMark        `json:"last_review,omitempty"`
	Deliveries     int                       `json:"deliveries,omitempty"`
	LastDelivery   *domain.DeliveryMark      `json:"last_delivery,omitempty"`
	LastActivity   *domain.Activity          `json:"last_activity,omitempty"`

	// What the owner's board shows, decided here and not by the panel:
	// the flow's steps, what the owner approves now, the column and the
	// one fact to read first.
	Flow       []FlowStep `json:"flow,omitempty"`
	Awaits     string     `json:"awaits,omitempty"` // plan or delivery
	NeedsOwner bool       `json:"needs_owner,omitempty"`
	Column     string     `json:"column"`
	Signal     Signal     `json:"signal"`
}

// ProjectOverview is one project across the cluster.
type ProjectOverview struct {
	Project       string               `json:"project"`
	Paused        bool                 `json:"paused,omitempty"`
	PausedWhy     string               `json:"paused_why,omitempty"`
	Goal          *domain.Goal         `json:"goal,omitempty"`
	Progress      *domain.GoalProgress `json:"progress,omitempty"`
	Focus         *domain.Focus        `json:"focus,omitempty"`
	Status        map[string]int       `json:"status"`   // every item of the project, by status
	Activity      map[string]int       `json:"activity"` // every item of the project, by activity
	OpenQuestions int                  `json:"open_questions,omitempty"`
	OpenProposals int                  `json:"open_proposals,omitempty"`
	// Items are the ones that match the query, after offset and limit;
	// ItemsMatched counts them before the cut.
	Items        []*ItemOverview `json:"items"`
	ItemsMatched int             `json:"items_matched"`
	ItemsTotal   int             `json:"items_total"`
	Truncated    bool            `json:"truncated,omitempty"`

	Usage          domain.UsageTotals `json:"usage"`
	UsageByModel   domain.UsageBy     `json:"usage_by_model,omitempty"`
	UsageByRole    domain.UsageBy     `json:"usage_by_role,omitempty"`
	UsageByAccount domain.UsageBy     `json:"usage_by_account,omitempty"`
	UsageByMachine domain.UsageBy     `json:"usage_by_machine,omitempty"`
	UsageByAgent   domain.UsageBy     `json:"usage_by_agent,omitempty"`
	// UsageByDay holds the last 30 days (UTC), by 2006-01-02.
	UsageByDay domain.UsageBy    `json:"usage_by_day,omitempty"`
	Accounts   []*domain.Account `json:"accounts,omitempty"`
}

// MachineOverview is one machine across the projects.
type MachineOverview struct {
	Machine  MachineView           `json:"machine"`
	Projects []string              `json:"projects,omitempty"`
	Sessions []*domain.SessionInfo `json:"sessions,omitempty"`
	// Items are the ones this machine holds, as project/id.
	Items     []string              `json:"items,omitempty"`
	Worktrees []domain.WorktreeInfo `json:"worktrees,omitempty"`
	// WorktreeBytes sums the measured worktrees; the disk is the one that holds them.
	WorktreeBytes  int64 `json:"worktree_bytes,omitempty"`
	DiskFreeBytes  int64 `json:"disk_free_bytes,omitempty"`
	DiskTotalBytes int64 `json:"disk_total_bytes,omitempty"`
	// What runs for items on this machine, added up.
	Procs          *domain.ProcSummary `json:"procs,omitempty"`
	ProcsSupported bool                `json:"procs_supported,omitempty"`
	// ReportedAt is when the machine last told what it keeps.
	ReportedAt time.Time          `json:"reported_at,omitzero"`
	Usage      domain.UsageTotals `json:"usage"`
}

// Overview is the cluster in one answer: what every project, item and
// machine is doing and what it cost. It is built from memory.
type Overview struct {
	GeneratedAt time.Time          `json:"generated_at"`
	Machine     string             `json:"machine"` // the one that answered
	Projects    []*ProjectOverview `json:"projects"`
	Machines    []*MachineOverview `json:"machines"`
	Activity    map[string]int     `json:"activity"`
	Usage       domain.UsageTotals `json:"usage"`
	// The projects' usage added up, so a reader never sums it.
	UsageByModel   domain.UsageBy `json:"usage_by_model,omitempty"`
	UsageByRole    domain.UsageBy `json:"usage_by_role,omitempty"`
	UsageByAccount domain.UsageBy `json:"usage_by_account,omitempty"`
	UsageByMachine domain.UsageBy `json:"usage_by_machine,omitempty"`
	UsageByDay     domain.UsageBy `json:"usage_by_day,omitempty"`
}

// Overview answers what is being done, where and at what cost, on every
// machine. It reads only this daemon's memory: the log's totals are kept as
// events are applied and each machine tells its own worktrees and processes,
// so the answer costs the same with ten items or ten thousand finished ones.
func (s *Service) Overview(ctx context.Context, a Actor, q OverviewQuery) (*Overview, error) {
	var projects []*Project
	if q.Project != "" {
		p, err := s.Project(q.Project)
		if err != nil {
			return nil, err
		}
		if err := a.Require(ScopeItemsRead, p.Name); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	} else {
		for _, name := range s.Projects() {
			p, err := s.Project(name)
			if err != nil || a.Require(ScopeItemsRead, p.Name) != nil {
				continue
			}
			projects = append(projects, p)
		}
		if len(projects) == 0 && len(s.Projects()) > 0 {
			return nil, fmt.Errorf("%w: no project this token may read", ErrForbidden)
		}
	}
	if q.Item != "" {
		id, err := domain.NormalizeItemID(q.Item)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		q.Item = id
	}
	switch {
	case q.Limit <= 0:
		q.Limit = overviewLimit
	case q.Limit > overviewLimitMax:
		q.Limit = overviewLimitMax
	}
	q.Offset = max(q.Offset, 0)
	if q.Finished <= 0 {
		q.Finished = overviewFinished
	}
	now := s.d.Clock.Now()
	out := &Overview{GeneratedAt: now.UTC(), Machine: s.d.Machine, Activity: map[string]int{},
		UsageByModel: domain.UsageBy{}, UsageByRole: domain.UsageBy{}, UsageByAccount: domain.UsageBy{}, UsageByMachine: domain.UsageBy{}, UsageByDay: domain.UsageBy{}}
	machines := map[string]*MachineOverview{}
	for _, p := range projects {
		po := s.projectOverview(p, q, now, machines)
		out.Projects = append(out.Projects, po)
		out.Usage.Merge(po.Usage)
		out.UsageByModel.Add(po.UsageByModel)
		out.UsageByRole.Add(po.UsageByRole)
		out.UsageByAccount.Add(po.UsageByAccount)
		out.UsageByMachine.Add(po.UsageByMachine)
		out.UsageByDay.Add(po.UsageByDay)
		for k, n := range po.Activity {
			out.Activity[k] += n
		}
	}
	for _, name := range sortedKeys(machines) {
		m := machines[name]
		sort.Strings(m.Projects)
		sort.Strings(m.Items)
		sort.Slice(m.Sessions, func(i, j int) bool { return m.Sessions[i].Agent < m.Sessions[j].Agent })
		if q.Machine == "" || q.Machine == name {
			out.Machines = append(out.Machines, m)
		}
	}
	return out, nil
}

// projectOverview builds one project and adds what it knows to the machines.
func (s *Service) projectOverview(p *Project, q OverviewQuery, now time.Time, machines map[string]*MachineOverview) *ProjectOverview {
	ttl := p.Cfg().Lease.TTL
	// The state is read in place, under the project's lock, and only what
	// goes into the answer is copied: a full copy would cost as much as the
	// whole history on every question.
	p.mu.Lock()
	defer p.mu.Unlock()
	snap := p.state
	po := &ProjectOverview{Project: p.Name, Paused: snap.Paused, PausedWhy: snap.PausedWhy, Status: map[string]int{}, Activity: map[string]int{}, Items: []*ItemOverview{}}
	items := make([]*domain.Item, 0, len(snap.Items))
	for _, it := range snap.Items {
		items = append(items, it)
	}
	po.ItemsTotal = len(items)
	if g := snap.ActiveGoal(); g != nil {
		goal := *g
		po.Goal = &goal
		pr := domain.ComputeProgress(items, g.ID, now, 7*24*time.Hour)
		po.Progress = &pr
	}
	if snap.Focus != nil && snap.Focus.Active(items, now) {
		focus := *snap.Focus
		po.Focus = &focus
	}
	po.OpenQuestions = len(snap.OpenQuestions())
	for _, pr := range snap.Proposals {
		if pr.Status == domain.ProposalOpen {
			po.OpenProposals++
		}
	}
	for _, acc := range snap.AccountList() {
		po.Accounts = append(po.Accounts, acc.WithOutlook(now))
	}

	// Machines: health as this project sees it; the freshest word wins.
	health := map[string]string{}
	for name, m := range snap.Machines {
		health[name] = m.Health(now, ttl)
		mo := machineOverview(machines, name)
		if mo.Machine.Name == "" || m.LastSeen.After(mo.Machine.LastSeen) {
			mo.Machine = MachineView{Machine: *m, Health: health[name]}
		}
		mo.Projects = append(mo.Projects, p.Name)
	}
	up := func(machine string) bool { h := health[machine]; return h == "green" || h == "yellow" }

	// Live sessions, by item.
	sessions := map[string][]*domain.SessionInfo{}
	for _, si := range snap.Sessions {
		if !si.Live() || now.Sub(si.At) > sessionFresh || !up(si.Machine) {
			continue
		}
		c := *si
		sessions[si.ItemID] = append(sessions[si.ItemID], &c)
		mo := machineOverview(machines, si.Machine)
		mo.Sessions = append(mo.Sessions, &c)
	}
	// Worktrees, by item, and each machine's own totals.
	worktrees := map[string][]domain.WorktreeInfo{}
	for _, name := range sortedKeys(snap.Worktrees) {
		rep := snap.Worktrees[name]
		mo := machineOverview(machines, name)
		mo.WorktreeBytes += rep.SizeBytes
		mo.ProcsSupported = mo.ProcsSupported || rep.ProcsSupported
		if rep.At.After(mo.ReportedAt) {
			mo.ReportedAt = rep.At
			mo.DiskFreeBytes, mo.DiskTotalBytes = rep.DiskFreeBytes, rep.DiskTotalBytes
		}
		for _, w := range rep.Worktrees {
			if q.Brief && w.Procs != nil {
				c := *w.Procs
				c.Top = nil
				w.Procs = &c
			}
			mo.Worktrees = append(mo.Worktrees, w)
			mo.Procs = addProcs(mo.Procs, w.Procs, !q.Brief)
			if w.ItemID != "" {
				worktrees[w.ItemID] = append(worktrees[w.ItemID], w)
			}
		}
	}

	// Every item is counted; only the ones that match are detailed.
	var matched []*ItemOverview
	for _, it := range items {
		if it.Status.Terminal() && !wantsFinished(q, it, now) {
			// History: counted, never built. The status of a finished item is its activity.
			po.Status[string(it.Status)]++
			po.Activity[string(it.Status)]++
			continue
		}
		io := itemOverview(p, snap, it.Clone(), now, sessions[it.ID], worktrees[it.ID], up)
		po.Status[io.Status]++
		po.Activity[io.Activity]++
		if it.Claim != nil && !it.Status.Terminal() {
			mo := machineOverview(machines, it.Claim.Machine)
			mo.Items = append(mo.Items, p.Name+"/"+it.ID)
		}
		if matchesOverview(q, it, io, now) {
			matched = append(matched, io)
		}
	}
	sort.SliceStable(matched, func(i, j int) bool {
		a, b := matched[i], matched[j]
		if ra, rb := activityRank[a.Activity], activityRank[b.Activity]; ra != rb {
			return ra < rb
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		return a.UpdatedAt.After(b.UpdatedAt)
	})
	po.ItemsMatched = len(matched)
	if q.Offset < len(matched) {
		matched = matched[q.Offset:]
	} else {
		matched = nil
	}
	if len(matched) > q.Limit {
		matched, po.Truncated = matched[:q.Limit], true
	}
	if matched != nil {
		po.Items = matched
	}

	// The ledger is read last and only for the items shown.
	led := snap.Ledger
	if led != nil {
		po.Usage = led.Total
		po.UsageByModel, po.UsageByRole, po.UsageByAccount = led.ByModel.Clone(), led.ByRole.Clone(), led.ByAccount.Clone()
		po.UsageByMachine, po.UsageByAgent = led.ByMachine.Clone(), led.ByAgent.Clone()
		po.UsageByDay = domain.UsageBy{}
		since := now.UTC().AddDate(0, 0, -overviewDays).Format(time.DateOnly)
		for day, t := range led.ByDay {
			if day >= since {
				c := *t
				po.UsageByDay[day] = &c
			}
		}
		for _, io := range po.Items {
			il := led.ByItem[io.ID]
			io.Signal = itemSignal(snap.Items[io.ID], io, il)
			if il == nil {
				continue
			}
			io.Usage, io.Reviews, io.Deliveries = il.Usage, il.Reviews, il.Deliveries
			if q.Brief {
				continue
			}
			c := il.Clone()
			io.UsageByStep, io.UsageByModel, io.UsageByMachine, io.UsageByRole = c.ByStep, c.ByModel, c.ByMachine, c.ByRole
			io.LastReview, io.LastDelivery, io.LastActivity = c.LastReview, c.LastDelivery, c.Last
			for _, name := range sortedKeys(c.Checks) {
				io.Checks = append(io.Checks, c.Checks[name])
			}
		}
	}
	if led == nil {
		for _, io := range po.Items {
			io.Signal = itemSignal(snap.Items[io.ID], io, nil)
		}
	}
	for name, t := range po.UsageByMachine {
		machineOverview(machines, name).Usage.Merge(*t)
	}
	return po
}

func machineOverview(m map[string]*MachineOverview, name string) *MachineOverview {
	mo := m[name]
	if mo == nil {
		mo = &MachineOverview{}
		m[name] = mo
	}
	return mo
}

// addProcs adds one worktree's processes to a total. The heaviest stay listed.
func addProcs(total, w *domain.ProcSummary, top bool) *domain.ProcSummary {
	if w == nil {
		return total
	}
	if total == nil {
		total = &domain.ProcSummary{ByKind: map[string]int{}}
	}
	total.Count += w.Count
	total.CPUPct += w.CPUPct
	total.RSSBytes += w.RSSBytes
	total.CPUSeconds += w.CPUSeconds
	total.PeakRSSBytes = max(total.PeakRSSBytes, w.PeakRSSBytes)
	for k, n := range w.ByKind {
		total.ByKind[k] += n
	}
	if top {
		total.Top = append(total.Top, w.Top...)
		sort.SliceStable(total.Top, func(i, j int) bool {
			if total.Top[i].CPUPct != total.Top[j].CPUPct {
				return total.Top[i].CPUPct > total.Top[j].CPUPct
			}
			return total.Top[i].RSSBytes > total.Top[j].RSSBytes
		})
		if len(total.Top) > procTop {
			total.Top = total.Top[:procTop]
		}
	}
	return total
}

// wantsFinished reports whether a finished item can be in the answer at all.
func wantsFinished(q OverviewQuery, it *domain.Item, now time.Time) bool {
	switch {
	case q.Item != "":
		return it.ID == q.Item
	case len(q.Activity) > 0:
		return slices.Contains(q.Activity, string(it.Status))
	}
	return q.All || now.Sub(it.UpdatedAt) <= q.Finished
}

func matchesOverview(q OverviewQuery, it *domain.Item, io *ItemOverview, now time.Time) bool {
	if q.Item != "" {
		return it.ID == q.Item
	}
	if len(q.Activity) > 0 {
		if !slices.Contains(q.Activity, io.Activity) && !slices.Contains(q.Activity, io.Status) {
			return false
		}
	} else if it.Status.Terminal() && !wantsFinished(q, it, now) {
		return false
	}
	if q.Machine != "" {
		on := io.Machine == q.Machine
		for _, si := range io.Sessions {
			on = on || si.Machine == q.Machine
		}
		for _, w := range io.Worktrees {
			on = on || w.Machine == q.Machine
		}
		if !on {
			return false
		}
	}
	return true
}

// itemOverview joins what the item says with what the machines say about it.
func itemOverview(p *Project, snap *domain.State, it *domain.Item, now time.Time, sessions []*domain.SessionInfo, worktrees []domain.WorktreeInfo, up func(string) bool) *ItemOverview {
	io := &ItemOverview{ID: it.ID, Title: it.Title, Kind: it.Kind, Priority: int(it.Priority), Lane: string(it.Lane), Area: it.Area, GoalID: it.GoalID,
		Size: it.Size, Class: it.Class, Status: string(it.Status), Step: it.Step, Claim: it.Claim, Branch: it.Branch, TrackerRef: it.TrackerRef,
		Adopted: it.Adopted, Paused: it.Status == domain.StatusPaused, Parked: it.Parked, Risky: it.Risky, Tier: it.Tier, Attempts: it.Attempts,
		ReworkRounds: len(it.Rework), Deps: it.Deps, WaitingQuestion: it.WaitingQuestion, CriteriaTotal: len(it.Criteria),
		CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt, StepStartedAt: it.StepStartedAt, IntegratedAt: it.IntegratedAt, MergeCommit: it.MergeCommit,
		ClosedReason: it.ClosedReason, ClosedBy: it.ClosedBy, Sessions: sessions, Worktrees: worktrees, Runs: it.Runs}
	sort.Slice(io.Sessions, func(i, j int) bool { return io.Sessions[i].Agent < io.Sessions[j].Agent })
	for _, c := range it.Criteria {
		if c.Met {
			io.CriteriaMet++
		}
	}
	for _, dep := range it.Deps {
		if d := snap.Items[dep]; d == nil || d.Status != domain.StatusIntegrated {
			io.BlockedBy = append(io.BlockedBy, dep)
		}
	}
	if it.Claim != nil {
		io.Machine = it.Claim.Machine
	}
	for _, w := range worktrees {
		io.Procs = addProcs(io.Procs, w.Procs, true)
	}
	terminal := it.Status.Terminal()
	if !terminal {
		if !it.StepStartedAt.IsZero() {
			io.StepSeconds = now.Sub(it.StepStartedAt).Seconds()
		}
		io.QuietSeconds = now.Sub(it.UpdatedAt).Seconds()
	}
	switch {
	case !it.IntegratedAt.IsZero():
		io.LeadSeconds = it.IntegratedAt.Sub(it.CreatedAt).Seconds()
	case !terminal:
		io.LeadSeconds = now.Sub(it.CreatedAt).Seconds()
	}
	if it.PR != "" {
		pr := prFromRef(it.PR)
		io.PR = &PRView{Number: pr.Number, URL: pr.URL, State: prState(p, it)}
	}
	io.Activity, io.Detail = itemActivity(snap, it, io, up)
	if io.Activity == ActIdle && it.Claim != nil {
		if why := idleWhy(p, snap, it, now); why != "" {
			io.Detail = why
		}
	}
	boardFacts(p, it, io)
	return io
}

// prState derives the pull request's state from the item: Barnraise opens it
// as a draft, marks it ready when the implementation is delivered, and
// merges or closes it with the item.
func prState(p *Project, it *domain.Item) string {
	switch it.Status {
	case domain.StatusIntegrated:
		return "merged"
	case domain.StatusClosed:
		return "closed"
	case domain.StatusQueued:
		return "ready"
	}
	for step := range it.Runs {
		if step != it.Step && isImplementStep(p, it, step) {
			return "ready"
		}
	}
	return "draft"
}

func itemActivity(snap *domain.State, it *domain.Item, io *ItemOverview, up func(string) bool) (activity, detail string) {
	switch it.Status {
	case domain.StatusIntegrated:
		return ActIntegrated, ""
	case domain.StatusClosed:
		return ActClosed, it.ClosedReason
	case domain.StatusPaused:
		return ActPaused, "taken over by a person"
	}
	if snap.Paused {
		return ActPaused, "the project is paused: " + snap.PausedWhy
	}
	switch it.Status {
	case domain.StatusWaitingOwner:
		if q := snap.Questions[it.WaitingQuestion]; q != nil {
			return ActWaitingOwner, truncate(q.Decision, 200)
		}
		return ActWaitingOwner, ""
	case domain.StatusBlocked:
		return ActBlocked, strings.Join(io.BlockedBy, ", ")
	case domain.StatusQueued:
		return ActQueued, ""
	}
	if it.Parked {
		return ActParked, "waits for the focus to end"
	}
	running := io.Procs != nil && io.Procs.Count > 0
	switch it.Status {
	case domain.StatusDraft:
		if len(io.Sessions) > 0 {
			return ActRefining, io.Sessions[0].Agent
		}
		if len(it.Clarifications) > 0 {
			return ActDraft, strconv.Itoa(len(it.Clarifications)) + " open clarifications"
		}
		return ActDraft, ""
	case domain.StatusReady:
		if len(io.BlockedBy) > 0 {
			return ActReady, "after " + strings.Join(io.BlockedBy, ", ")
		}
		return ActReady, ""
	}
	// In progress.
	switch {
	case len(io.Sessions) > 0:
		return ActWorking, io.Sessions[0].Agent + " on " + io.Sessions[0].Machine
	case len(io.BlockedBy) > 0:
		// Its step waits for other items to land, and starts again by itself.
		return ActBlocked, "waits for " + strings.Join(io.BlockedBy, ", ")
	case it.Claim != nil && !up(it.Claim.Machine):
		return ActStalled, "machine " + it.Claim.Machine + " is out"
	case running:
		return ActRunning, strconv.Itoa(io.Procs.Count) + " processes, no agent"
	case it.Claim == nil:
		return ActIdle, "no machine holds it"
	}
	return ActIdle, "held by " + it.Claim.Machine + ", nothing runs"
}

// idleWhy says why an item a machine holds has no session: the agents of
// its step's role on that machine are all busy, or the step starts at the
// next pass. "" when the step has no agent role.
func idleWhy(p *Project, snap *domain.State, it *domain.Item, now time.Time) string {
	f, err := p.Cfg().FlowOf(it.Kind, it.Area)
	if err != nil {
		return ""
	}
	st, ok := f.Step(it.Step)
	if !ok {
		return ""
	}
	role := stepRole(st)
	if role == "" {
		return ""
	}
	busy := 0
	for _, si := range snap.Sessions {
		if si.Live() && now.Sub(si.At) <= sessionFresh && si.Role == role && si.Machine == it.Claim.Machine {
			busy++
		}
	}
	if n := p.Cfg().Roles[role].Count; n > 0 && busy >= n {
		return fmt.Sprintf("next for the %s on %s: %d of %d busy", role, it.Claim.Machine, busy, n)
	}
	return "the " + role + " on " + it.Claim.Machine + " takes it at the next pass"
}
