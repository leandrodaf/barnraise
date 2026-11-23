package app

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Status is what `barnraise status`, the MCP status tool and the panel's home show.
type Status struct {
	Project   string                `json:"project"`
	Machine   string                `json:"machine"`
	Paused    bool                  `json:"paused"`
	PausedWhy string                `json:"paused_why,omitempty"`
	Goal      *domain.Goal          `json:"goal,omitempty"`
	Progress  *domain.GoalProgress  `json:"progress,omitempty"`
	Focus     *domain.Focus         `json:"focus,omitempty"`
	FocusDone int                   `json:"focus_done"`
	FocusAll  int                   `json:"focus_all"`
	Counts    map[string]int        `json:"counts"`
	Queue     []*domain.Item        `json:"queue"`
	Waiting   []*domain.Question    `json:"waiting_owner"`
	Agents    []*domain.SessionInfo `json:"agents"`
	Accounts  []*domain.Account     `json:"accounts"`
	Machines  []MachineView         `json:"machines"`
	Proposals int                   `json:"open_proposals"`
	Alerts    []string              `json:"alerts,omitempty"`
}

// MachineView adds health to a machine.
type MachineView struct {
	domain.Machine
	Health string `json:"health"`
}

// GetStatus builds the status.
func (s *Service) GetStatus(ctx context.Context, project string, a Actor) (Status, error) {
	p, err := s.Project(project)
	if err != nil {
		return Status{}, err
	}
	if err := a.Require(ScopeItemsRead, p.Name); err != nil {
		return Status{}, err
	}
	snap := p.Snapshot()
	now := s.d.Clock.Now()
	st := Status{Project: p.Name, Machine: s.d.Machine, Paused: snap.Paused, PausedWhy: snap.PausedWhy, Counts: map[string]int{}}
	items := snap.ItemList()
	if g := snap.ActiveGoal(); g != nil {
		st.Goal = g
		pr := domain.ComputeProgress(items, g.ID, now, 7*24*time.Hour)
		st.Progress = &pr
	}
	if snap.Focus != nil && snap.Focus.Active(items, now) {
		st.Focus = snap.Focus
		for _, it := range items {
			if snap.Focus.Includes(it) {
				st.FocusAll++
				if it.Status == domain.StatusIntegrated {
					st.FocusDone++
				}
			}
		}
	}
	for _, it := range items {
		st.Counts[string(it.Status)]++
		if !it.Status.Terminal() && it.Status != domain.StatusDraft {
			st.Queue = append(st.Queue, it)
		}
	}
	st.Waiting = snap.OpenQuestions()
	for _, si := range snap.Sessions {
		if now.Sub(si.At) < 6*time.Hour {
			st.Agents = append(st.Agents, si)
		}
	}
	sort.Slice(st.Agents, func(i, j int) bool { return st.Agents[i].Agent < st.Agents[j].Agent })
	st.Accounts = snap.AccountList()
	ttl := p.Cfg().Lease.TTL
	for _, name := range sortedKeys(snap.Machines) {
		m := snap.Machines[name]
		st.Machines = append(st.Machines, MachineView{Machine: *m, Health: m.Health(now, ttl)})
	}
	for _, pr := range snap.Proposals {
		if pr.Status == domain.ProposalOpen {
			st.Proposals++
		}
	}
	if st.Paused {
		st.Alerts = append(st.Alerts, "paused: "+st.PausedWhy)
	}
	for _, m := range st.Machines {
		if m.Health == "red" {
			st.Alerts = append(st.Alerts, fmt.Sprintf("machine %s is out since %s", m.Name, m.LastSeen.Format("15:04")))
		}
	}
	return st, nil
}

// Metrics are computed from the event log.
type Metrics struct {
	Window           string             `json:"window"`
	IntegratedPerDay float64            `json:"integrated_per_day"`
	IntegratedByKind map[string]int     `json:"integrated_by_kind"`
	ReworkAvg        float64            `json:"rework_avg"`
	ReworkCauses     map[string]int     `json:"rework_causes"`
	TokensByModel    map[string]int64   `json:"tokens_by_model"`
	TokensByRole     map[string]int64   `json:"tokens_by_role"`
	TokensByItem     map[string]int64   `json:"tokens_by_item"`
	StepSeconds      map[string]float64 `json:"step_seconds"`
	CacheHitRate     float64            `json:"cache_hit_rate"`
	ProposalsMade    int                `json:"proposals_made"`
	ProposalsOK      int                `json:"proposals_approved"`
	WaitingOwnerMax  string             `json:"waiting_owner_max"`
	Violations       int                `json:"violations"`
	FlakyQuarantined int                `json:"flaky_quarantined"`
	ExploreReads     map[string]int     `json:"explore_reads_by_area"`
	FixedCostRatio   map[string]float64 `json:"fixed_cost_ratio_by_area"`
	Alerts           []string           `json:"alerts,omitempty"`
}

// Metrics measures time, tokens and rework by step, item and role.
func (s *Service) Metrics(ctx context.Context, project string, a Actor, window time.Duration) (Metrics, error) {
	p, err := s.Project(project)
	if err != nil {
		return Metrics{}, err
	}
	if err := a.Require(ScopeItemsRead, p.Name); err != nil {
		return Metrics{}, err
	}
	now := s.d.Clock.Now()
	since := now.Add(-window)
	evs, err := s.d.Store.Query(ctx, ports.EventQuery{Project: p.Name, Since: since})
	if err != nil {
		return Metrics{}, err
	}
	m := Metrics{Window: window.String(), IntegratedByKind: map[string]int{}, ReworkCauses: map[string]int{}, TokensByModel: map[string]int64{},
		TokensByRole: map[string]int64{}, TokensByItem: map[string]int64{}, StepSeconds: map[string]float64{}, ExploreReads: map[string]int{}, FixedCostRatio: map[string]float64{}}
	snap := p.Snapshot()
	integrated := 0
	var cacheRead, totalIn int64
	stepTime := map[string]time.Duration{}
	stepCount := map[string]int{}
	implTimeByArea := map[string]time.Duration{}
	fixedTimeByArea := map[string]time.Duration{}
	flaky := map[string]bool{}
	for _, e := range evs {
		pl, err := e.DecodePayload()
		if err != nil {
			continue
		}
		switch e.Type {
		case domain.EvItemIntegrated:
			integrated++
			if pl.Item != nil {
				m.IntegratedByKind[pl.Item.Kind]++
			}
		case domain.EvReworkStarted:
			if pl.Rework != nil {
				m.ReworkCauses[pl.Rework.From]++
			}
		case domain.EvAgentUsage:
			if u := pl.Usage; u != nil {
				m.TokensByModel[u.Provider+"/"+u.Model] += u.Total()
				m.TokensByRole[u.Role] += u.Total()
				if u.ItemID != "" {
					m.TokensByItem[u.ItemID] += u.Total()
				}
				cacheRead += u.CacheRead
				totalIn += u.Input + u.CacheRead + u.CacheWrite
				if u.Step != "" {
					stepTime[u.Step] += u.Duration
					stepCount[u.Step]++
				}
				area := ""
				if it := snap.Items[u.ItemID]; it != nil {
					area = it.Area
				}
				m.ExploreReads[area] += u.ExploreReads
				if u.Role == "implementer" && (u.Step == "implement" || u.Step == "draft") {
					implTimeByArea[area] += u.Duration
				} else {
					fixedTimeByArea[area] += u.Duration
				}
			}
		case domain.EvProposalCreated:
			m.ProposalsMade++
		case domain.EvProposalUpdated:
			if pl.Proposal != nil && pl.Proposal.Status == domain.ProposalApproved {
				m.ProposalsOK++
			}
		case domain.EvPolicyViolation:
			m.Violations++
		case domain.EvCheckCompleted:
			if pl.Check != nil {
				flaky[pl.Check.Check] = pl.Check.Flaky
			}
		}
	}
	for _, f := range flaky {
		if f {
			m.FlakyQuarantined++
		}
	}
	if d := window.Hours() / 24; d > 0 {
		m.IntegratedPerDay = float64(integrated) / d
	}
	rounds, n := 0, 0
	for _, it := range snap.Items {
		if it.Status == domain.StatusIntegrated && it.IntegratedAt.After(since) {
			rounds += len(it.Rework)
			n++
		}
	}
	if n > 0 {
		m.ReworkAvg = float64(rounds) / float64(n)
	}
	if totalIn > 0 {
		m.CacheHitRate = float64(cacheRead) / float64(totalIn)
	}
	for st, d := range stepTime {
		m.StepSeconds[st] = d.Seconds() / float64(stepCount[st])
	}
	var maxWait time.Duration
	for _, q := range snap.OpenQuestions() {
		if w := now.Sub(q.AskedAt); w > maxWait {
			maxWait = w
		}
	}
	m.WaitingOwnerMax = maxWait.Round(time.Minute).String()
	for area, impl := range implTimeByArea {
		if impl > 0 {
			m.FixedCostRatio[area] = fixedTimeByArea[area].Seconds() / impl.Seconds()
		}
	}
	// Alerts from the design table.
	if m.ReworkAvg > 2 {
		m.Alerts = append(m.Alerts, fmt.Sprintf("rework average is %.1f rounds per item (above 2)", m.ReworkAvg))
	}
	totalRework := 0
	for _, c := range m.ReworkCauses {
		totalRework += c
	}
	for cause, c := range m.ReworkCauses {
		if totalRework >= 5 && float64(c)/float64(totalRework) > 0.3 {
			m.Alerts = append(m.Alerts, fmt.Sprintf("%.0f%% of rework comes from step %s", 100*float64(c)/float64(totalRework), cause))
		}
	}
	if m.ProposalsMade >= 5 && float64(m.ProposalsOK)/float64(m.ProposalsMade) < 0.2 {
		m.Alerts = append(m.Alerts, fmt.Sprintf("only %d of %d proposals approved: agents may be inventing work", m.ProposalsOK, m.ProposalsMade))
	}
	if maxWait > 24*time.Hour {
		m.Alerts = append(m.Alerts, "an item waits for the owner for more than 24 hours")
	}
	if m.FlakyQuarantined > 5 {
		m.Alerts = append(m.Alerts, fmt.Sprintf("%d flaky checks in quarantine", m.FlakyQuarantined))
	}
	if m.Violations > 0 {
		m.Alerts = append(m.Alerts, fmt.Sprintf("%d policy violations blocked by hooks", m.Violations))
	}
	for area, r := range m.FixedCostRatio {
		if r > 1 {
			m.Alerts = append(m.Alerts, fmt.Sprintf("items of area %q are too small: fixed cost is %.1fx the implementation time", def(area, "-"), r))
		}
	}
	sort.Strings(m.Alerts)
	return m, nil
}

// TimelineEntry is one event of an item, in order.
type TimelineEntry struct {
	Seq     int64          `json:"seq"`
	Time    time.Time      `json:"time"`
	Type    string         `json:"type"`
	Actor   string         `json:"actor"`
	Machine string         `json:"machine"`
	Step    string         `json:"step,omitempty"`
	Summary string         `json:"summary"`
	Payload domain.Payload `json:"payload"`
}

// Timeline returns every event of an item with "redo from here" points (steps).
func (s *Service) Timeline(ctx context.Context, project string, a Actor, id string) ([]TimelineEntry, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeItemsRead, p.Name); err != nil {
		return nil, err
	}
	id, err = domain.NormalizeItemID(id)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	evs, err := s.d.Store.Query(ctx, ports.EventQuery{Project: p.Name, Subject: id})
	if err != nil {
		return nil, err
	}
	var out []TimelineEntry
	for _, e := range evs {
		pl, _ := e.DecodePayload()
		te := TimelineEntry{Seq: e.Seq, Time: e.Time, Type: e.Type, Actor: e.Actor, Machine: e.Machine, Payload: pl}
		if pl.Item != nil {
			te.Step = pl.Item.Step
			te.Summary = fmt.Sprintf("%s at step %s", pl.Item.Status, pl.Item.Step)
		}
		switch {
		case pl.Delivery != nil:
			te.Summary = fmt.Sprintf("%s delivered %s: %s", pl.Delivery.Agent, pl.Delivery.Status, truncate(pl.Delivery.Summary, 200))
		case pl.Review != nil:
			te.Summary = fmt.Sprintf("%s reviewed %s (criteria %s, code %s)", pl.Review.Reviewer, pl.Review.Decision, pl.Review.Criteria, pl.Review.Code)
		case pl.Check != nil:
			te.Summary = fmt.Sprintf("check %s passed=%v: %s", pl.Check.Check, pl.Check.Passed, truncate(pl.Check.Summary, 200))
		case pl.Rework != nil:
			te.Summary = fmt.Sprintf("rework %s → %s: %s", pl.Rework.From, pl.Rework.To, truncate(pl.Rework.Reason, 200))
		case pl.Journal != nil:
			te.Summary = truncate(pl.Journal.Text, 200)
		case pl.Usage != nil:
			te.Summary = fmt.Sprintf("%s used %d tokens on %s/%s in %s", pl.Usage.Agent, pl.Usage.Total(), pl.Usage.Provider, pl.Usage.Model, pl.Usage.Duration.Round(time.Second))
		case pl.Note != "" && te.Summary == "":
			te.Summary = pl.Note
		case pl.Note != "":
			te.Summary += " — " + pl.Note
		}
		out = append(out, te)
	}
	return out, nil
}

// Estimate is the forecast before starting: time and share of the account week.
type Estimate struct {
	ItemID       string  `json:"item_id"`
	Hours        float64 `json:"hours"`
	Tokens       int64   `json:"tokens"`
	WeekPct      float64 `json:"week_pct"`
	Account      string  `json:"account,omitempty"`
	BasedOnItems int     `json:"based_on_items"`
}

// EstimateItem forecasts an item from integrated items of the same kind and area.
func (s *Service) EstimateItem(ctx context.Context, project string, a Actor, id string) (Estimate, error) {
	p, err := s.Project(project)
	if err != nil {
		return Estimate{}, err
	}
	if err := a.Require(ScopeItemsRead, p.Name); err != nil {
		return Estimate{}, err
	}
	id, err = domain.NormalizeItemID(id)
	if err != nil {
		return Estimate{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	snap := p.Snapshot()
	it := snap.Items[id]
	if it == nil {
		return Estimate{}, fmt.Errorf("%w: item %s", ErrNotFound, id)
	}
	m, err := s.Metrics(ctx, p.Name, a, 30*24*time.Hour)
	if err != nil {
		return Estimate{}, err
	}
	var tokens []int64
	var hours []float64
	for _, o := range snap.Items {
		if o.Status != domain.StatusIntegrated || o.Kind != it.Kind {
			continue
		}
		if it.Area != "" && o.Area != it.Area && len(tokens) > 3 {
			continue
		}
		if t := m.TokensByItem[o.ID]; t > 0 {
			tokens = append(tokens, t)
			if c := firstStart(o); !c.IsZero() && !o.IntegratedAt.IsZero() {
				hours = append(hours, o.IntegratedAt.Sub(c).Hours())
			}
		}
	}
	e := Estimate{ItemID: id, BasedOnItems: len(tokens)}
	if len(tokens) == 0 {
		lo, hi, _ := domain.ParseSizeRange(p.Cfg().TargetSize)
		e.Hours = (lo + hi) / 2
		return e, nil
	}
	slices.Sort(tokens)
	e.Tokens = tokens[len(tokens)/2]
	if len(hours) > 0 {
		sort.Float64s(hours)
		e.Hours = hours[len(hours)/2]
	}
	// Share of a week: derived from accounts that reported both tokens and usage.
	if acc := domain.PickAccount(snap.AccountList(), "claude", s.d.Machine, p.Name, s.d.Clock.Now()); acc != nil {
		e.Account = acc.ID
		var used int64
		for _, t := range m.TokensByModel {
			used += t
		}
		if acc.Usage.WeeklyPct > 0 && used > 0 {
			e.WeekPct = float64(e.Tokens) / float64(used) * acc.Usage.WeeklyPct
		}
	}
	return e, nil
}
