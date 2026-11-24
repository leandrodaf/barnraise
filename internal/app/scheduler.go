package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Slot is an agent position on this machine: a role, ready to run.
type Slot struct {
	Agent    string         `json:"agent"`
	Role     string         `json:"role"`
	Machine  domain.Machine `json:"machine"`
	Reviewer bool           `json:"reviewer"`
}

// Assignment is the work handed to an agent slot.
type Assignment struct {
	Project string         `json:"project"`
	Item    *domain.Item   `json:"item"`
	Step    domain.StepDef `json:"step"`
	New     bool           `json:"new"` // freshly claimed
}

// laneWindow is how far back the scheduler counts claims per lane.
const laneWindow = 7 * 24 * time.Hour

// stepRole returns the role that works on a step (agent steps and
// approvals by a role), or "" for steps the driver runs.
func stepRole(st domain.StepDef) string {
	switch st.Type {
	case domain.StepAgent:
		return st.Role
	case domain.StepApproval:
		if st.Approver != "owner" {
			return st.Approver
		}
	}
	return ""
}

// NextWork picks the next work for a slot: first a step of an item this
// machine already holds, then a new item by capacity split, focus,
// dependencies and file overlap, reserved atomically in the arbiter.
func (s *Service) NextWork(ctx context.Context, project string, slot Slot) (*Assignment, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if on, _ := s.runtime.Standby(); on {
		return nil, ErrStandby
	}
	p.mu.Lock()
	if p.state.Paused {
		p.mu.Unlock()
		return nil, ErrPaused
	}
	if a := s.heldWorkLocked(p, slot); a != nil {
		p.running[a.Item.ID] = slot.Agent
		p.mu.Unlock()
		return a, nil
	}
	cands := s.candidatesLocked(p, slot)
	p.mu.Unlock()

	if len(cands) == 0 && slot.Role == "analyst" {
		if a := s.refineWork(p, slot); a != nil {
			return a, nil
		}
	}
	for _, it := range cands {
		if ok := s.validBeforeStart(ctx, p, it); !ok {
			continue
		}
		a, err := s.claim(ctx, p, it.ID, slot)
		if errors.Is(err, ports.ErrTaken) || errors.Is(err, ErrConflict) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return a, nil
	}
	return nil, ErrNoWork
}

// RefineStep is the analyst's work on draft items: EARS criteria tied to
// checks, anchors, and questions to the owner instead of guesses.
const RefineStep = "refine"

// refineWork hands a draft item to an idle analyst, at most once per version.
// Drafts have no claim: refining them is a duty of the cluster, done only
// by the machine that holds the housekeeping lease. Otherwise every
// machine's analyst refined the same draft at once.
func (s *Service) refineWork(p *Project, slot Slot) *Assignment {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Ports().Arbiter != nil {
		if _, held := p.leases[HousekeepingKey]; !held {
			return nil
		}
	}
	if p.refined == nil {
		p.refined = map[string]int{}
	}
	var best *domain.Item
	for _, it := range p.state.Items {
		if it.Status != domain.StatusDraft || it.WaitingQuestion != "" {
			continue
		}
		if _, busy := p.running[it.ID]; busy {
			continue
		}
		if v, ok := p.refined[it.ID]; ok && v == it.Version {
			continue // already refined at this version; it waits for a person
		}
		if best == nil || it.Priority < best.Priority || (it.Priority == best.Priority && it.CreatedAt.Before(best.CreatedAt)) {
			best = it
		}
	}
	if best == nil {
		return nil
	}
	p.refined[best.ID] = best.Version
	p.running[best.ID] = slot.Agent
	return &Assignment{Project: p.Name, Item: best.Clone(), Step: domain.StepDef{Name: RefineStep, Type: domain.StepAgent, Role: "analyst"}}
}

// forgetRefine lets a draft be refined again at its current version: the
// refine that was handed out never started (no account with capacity, a
// workspace error), and otherwise the draft would wait for a person to edit
// it before any analyst looked at it again.
func (s *Service) forgetRefine(p *Project, itemID string) {
	p.mu.Lock()
	delete(p.refined, itemID)
	p.mu.Unlock()
}

// heldWorkLocked finds a step of a held item that wants this role.
func (s *Service) heldWorkLocked(p *Project, slot Slot) *Assignment {
	var best *domain.Item
	var bestStep domain.StepDef
	for _, it := range p.state.Items {
		if it.Status != domain.StatusInProgress || it.Claim == nil || it.Claim.Machine != s.d.Machine || it.Parked {
			continue
		}
		if _, busy := p.running[it.ID]; busy {
			continue
		}
		if it.Alt != nil && it.Alt.A != nil {
			continue // attempt a delivered; the second attempt decides when it ends
		}
		f, err := p.Cfg().FlowOf(it.Kind, it.Area)
		if err != nil {
			continue
		}
		st, ok := f.Step(it.Step)
		if !ok || stepRole(st) != slot.Role {
			continue
		}
		if !s.machineFits(p, slot.Role, slot.Machine) {
			continue
		}
		if best == nil || it.Priority < best.Priority || (it.Priority == best.Priority && it.StepStartedAt.Before(best.StepStartedAt)) {
			best, bestStep = it, st
		}
	}
	if best == nil {
		return nil
	}
	return &Assignment{Project: p.Name, Item: best.Clone(), Step: bestStep}
}

func (s *Service) machineFits(p *Project, role string, m domain.Machine) bool {
	r, ok := p.Cfg().Roles[role]
	if !ok {
		return false
	}
	for _, need := range r.Needs {
		if !m.HasLabel(need) {
			return false
		}
	}
	return true
}

// candidatesLocked returns new items whose first step this role runs,
// ordered by lane choice and priority.
func (s *Service) candidatesLocked(p *Project, slot Slot) []*domain.Item {
	if !s.machineFits(p, slot.Role, slot.Machine) {
		return nil
	}
	now := s.d.Clock.Now()
	items := p.state.ItemList()
	focus := p.state.Focus
	focusActive := focus.Active(items, now)
	var inProgressFiles [][]string
	for _, it := range items {
		if it.Status == domain.StatusInProgress || it.Status == domain.StatusQueued {
			inProgressFiles = append(inProgressFiles, p.Cfg().Scheduling.Exclusive(domain.PredictedFiles(it)))
		}
	}
	byLane := map[domain.Lane][]*domain.Item{}
	hasP0 := false
	for _, it := range items {
		if it.Status != domain.StatusReady || it.Claim != nil {
			continue
		}
		if it.Parked && focusActive && !focus.Includes(it) {
			continue
		}
		if !it.DepsSatisfied(func(id string) *domain.Item { return p.state.Items[id] }) {
			continue
		}
		f, err := p.Cfg().FlowOf(it.Kind, it.Area)
		if err != nil {
			continue
		}
		start := it.Step
		if start == "" {
			start = f.First()
		}
		st, ok := f.Step(start)
		if !ok {
			continue
		}
		role := stepRole(st)
		if role != "" && role != slot.Role {
			continue
		}
		if role == "" && slot.Role != firstAgentRole(f) {
			continue // driver steps first: any implementer may claim it
		}
		pf := p.Cfg().Scheduling.Exclusive(domain.PredictedFiles(it))
		overlap := false
		for _, other := range inProgressFiles {
			if domain.FilesOverlap(pf, other) {
				overlap = true
				break
			}
		}
		if overlap {
			continue
		}
		// Self-improvement only runs with the owner's approval (it came from an approved proposal).
		byLane[it.Lane] = append(byLane[it.Lane], it)
		if it.Lane == domain.LaneBugs && it.Priority == domain.P0 {
			hasP0 = true
		}
	}
	usage := domain.LaneUsage{}
	selfWeek := 0
	for _, it := range items {
		// Count only items that actually started.
		started := firstStart(it)
		if started.IsZero() || now.Sub(started) > laneWindow {
			continue
		}
		usage[it.Lane]++
		if it.Lane == domain.LaneSelf && now.Sub(started) <= 7*24*time.Hour {
			selfWeek++
		}
	}
	capacity := p.Cfg().Capacity
	if focusActive && focus.Share > 0 && focus.Share != capacity.Goal {
		// The focus takes its share from the goal lane; the rest is split proportionally.
		rest := 100 - focus.Share
		other := capacity.Bugs + capacity.Improvements
		if other > 0 {
			capacity.Bugs = capacity.Bugs * rest / other
			capacity.Improvements = rest - capacity.Bugs
		}
		capacity.Goal = focus.Share
	}
	var out []*domain.Item
	remaining := map[domain.Lane][]*domain.Item{}
	maps.Copy(remaining, byLane)
	for len(remaining) > 0 {
		avail := map[domain.Lane]bool{}
		for l, list := range remaining {
			if len(list) > 0 {
				avail[l] = true
			}
		}
		lane, ok := domain.PickLane(capacity, usage, avail, selfWeek, hasP0)
		if !ok {
			break
		}
		list := remaining[lane]
		if lane == domain.LaneGoal && focusActive {
			list = focusFirst(list, focus)
		}
		out = append(out, list...)
		delete(remaining, lane)
		hasP0 = false
	}
	return out
}

func focusFirst(list []*domain.Item, f *domain.Focus) []*domain.Item {
	var in, rest []*domain.Item
	for _, it := range list {
		if f.Includes(it) {
			in = append(in, it)
		} else {
			rest = append(rest, it)
		}
	}
	return append(in, rest...)
}

func firstStart(it *domain.Item) time.Time {
	if it.Claim != nil && !it.Claim.Since.IsZero() {
		return it.Claim.Since
	}
	if !it.IntegratedAt.IsZero() {
		return it.IntegratedAt
	}
	return time.Time{}
}

func firstAgentRole(f *domain.Flow) string {
	for _, st := range f.Steps {
		if r := stepRole(st); r != "" {
			return r
		}
	}
	return ""
}

// claim reserves the item in the arbiter and starts its first step.
func (s *Service) claim(ctx context.Context, p *Project, id string, slot Slot) (*Assignment, error) {
	var lease ports.Lease
	if p.Ports().Arbiter != nil {
		l, err := p.Ports().Arbiter.Claim(ctx, claimKey(id), s.d.Machine)
		if err != nil {
			return nil, err
		}
		lease = l
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	cur, ok := p.state.Items[id]
	if !ok || cur.Status != domain.StatusReady || cur.Claim != nil {
		s.releaseLeaseValue(ctx, p, lease)
		return nil, ErrConflict
	}
	f, err := p.Cfg().FlowOf(cur.Kind, cur.Area)
	if err != nil {
		s.releaseLeaseValue(ctx, p, lease)
		return nil, err
	}
	now := s.d.Clock.Now().UTC()
	var attached []*domain.Tweak
	if p.Cfg().Tweaks.AttachOnClaim && cur.Area != "" {
		attached = domain.PendingTweaks(p.state.TweakList(), cur.Area)
	}
	it, err := s.mutateItemLocked(ctx, p, id, domain.EvItemClaimed, slot.Agent, func(it *domain.Item) error {
		it.Status = domain.StatusInProgress
		it.Claim = &domain.Claim{Machine: s.d.Machine, Agent: slot.Agent, Since: now}
		it.Parked = false
		if it.Step == "" {
			it.Step = f.First()
		}
		it.StepStartedAt = now
		it.Attempts = 0
		if it.Branch == "" {
			it.Branch = domain.BranchName(it.ID, it.Title)
		}
		for _, t := range attached {
			it.Tweaks = append(it.Tweaks, t.Text)
		}
		return nil
	}, domain.Payload{})
	if err != nil {
		s.releaseLeaseValue(ctx, p, lease)
		return nil, err
	}
	for _, t := range attached {
		tt := *t
		tt.ItemID = id
		_, _ = s.emitLocked(ctx, p, domain.EvTweakAttached, t.ID, "barnraise", domain.Payload{Tweak: &tt})
	}
	if p.Ports().Arbiter != nil {
		p.leases[id] = lease
	}
	st, _ := f.Step(it.Step)
	if stepRole(st) == slot.Role {
		p.running[id] = slot.Agent
	}
	return &Assignment{Project: p.Name, Item: it, Step: st, New: true}, nil
}

// validBeforeStart compares the item's validation base with main: if its
// anchors changed since, it goes through triage first.
func (s *Service) validBeforeStart(ctx context.Context, p *Project, it *domain.Item) bool {
	if p.Ports().Git == nil || it.Anchors.Base == "" || it.Anchors.Empty() {
		return true
	}
	main, err := p.Ports().Git.MainHead(ctx)
	if err != nil || main == it.Anchors.Base {
		return true
	}
	files, err := p.Ports().Git.ChangedFiles(ctx, it.Anchors.Base, main)
	if err != nil {
		return true
	}
	ch := domain.Change{Kind: "integration", Cause: "main " + short(main), Commit: main, Files: files}
	var graph domain.CallGraph
	if s.d.Knowledge != nil {
		graph = s.d.Knowledge.Graph()
	}
	if len(domain.FindImpacted(ch, []*domain.Item{it}, graph)) == 0 {
		_, _ = s.mutateItem(ctx, p, it.ID, domain.EvItemRevalidated, "barnraise", func(x *domain.Item) error {
			x.Anchors.Base = main
			x.ValidatedAt = s.d.Clock.Now().UTC()
			return nil
		}, domain.Payload{Note: "anchors untouched since the last validation"})
		return true
	}
	res, err := s.triageItems(ctx, p, ch, []*domain.Item{it})
	if err != nil {
		s.d.Log.Warn("triage before start failed; starting anyway", "item", it.ID, "err", err)
		return true
	}
	for _, r := range res {
		if r.ItemID == it.ID && r.Verdict != domain.TriageStillValid && r.Verdict != domain.TriageNeedsUpdate {
			return false
		}
	}
	return true
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// releaseLease frees this machine's claim of an item.
func (s *Service) releaseLease(ctx context.Context, p *Project, id string) {
	p.mu.Lock()
	l, ok := p.leases[id]
	delete(p.leases, id)
	delete(p.running, id)
	p.mu.Unlock()
	if ok {
		s.releaseLeaseValue(ctx, p, l)
	}
}

func (s *Service) releaseLeaseValue(ctx context.Context, p *Project, l ports.Lease) {
	if p.Ports().Arbiter == nil || l.Key == "" {
		return
	}
	if err := p.Ports().Arbiter.Release(ctx, l); err != nil && !errors.Is(err, ports.ErrNotHeld) {
		s.d.Log.Warn("release lease", "key", l.Key, "err", err)
	}
}

// doneRunning frees a held item for its next step, only if owner still holds the marker.
func (s *Service) doneRunning(p *Project, id, owner string) {
	p.mu.Lock()
	if cur, ok := p.running[id]; ok && (owner == "" || cur == owner) {
		delete(p.running, id)
	}
	p.mu.Unlock()
}

// ClaimForAgent is the MCP next_item: the agent's current item, or a new one.
func (s *Service) ClaimForAgent(ctx context.Context, project string, a Actor, m domain.Machine) (*Assignment, error) {
	if err := a.Require(ScopeClaim, project); err != nil {
		return nil, err
	}
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if a.ItemID != "" {
		p.mu.Lock()
		it, ok := p.state.Items[a.ItemID]
		p.mu.Unlock()
		if ok && !it.Status.Terminal() {
			f, err := p.Cfg().FlowOf(it.Kind, it.Area)
			if err != nil {
				return nil, err
			}
			st, _ := f.Step(it.Step)
			return &Assignment{Project: p.Name, Item: it.Clone(), Step: st}, nil
		}
	}
	if a.Role == "" {
		return nil, fmt.Errorf("%w: next_item needs a role", ErrInvalid)
	}
	m.Name = s.d.Machine
	return s.NextWork(ctx, p.Name, Slot{Agent: a.Name, Role: a.Role, Machine: m})
}
