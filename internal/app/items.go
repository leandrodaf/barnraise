package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// NewItem is the input to create an item.
type NewItem struct {
	Title      string             `json:"title"`
	Body       string             `json:"body,omitempty"`
	Kind       string             `json:"kind,omitempty"`
	Priority   string             `json:"priority,omitempty"`
	Area       string             `json:"area,omitempty"`
	GoalID     string             `json:"goal_id,omitempty"`
	Size       string             `json:"size,omitempty"`
	Class      string             `json:"class,omitempty"`
	Criteria   []domain.Criterion `json:"criteria,omitempty"`
	Preserve   []string           `json:"preserve,omitempty"`
	Deps       []string           `json:"deps,omitempty"`
	Anchors    domain.Anchors     `json:"anchors"`
	Checklist  []string           `json:"checklist,omitempty"`
	Risky      bool               `json:"risky,omitempty"`
	External   bool               `json:"external,omitempty"` // came from a tracker or webhook: sanitize and fence
	TrackerRef string             `json:"tracker_ref,omitempty"`
	Lane       string             `json:"lane,omitempty"`
}

// CreateResult is an item, or a proposal when the rules say it must wait.
type CreateResult struct {
	Item     *domain.Item     `json:"item,omitempty"`
	Proposal *domain.Proposal `json:"proposal,omitempty"`
	Note     string           `json:"note,omitempty"`
}

// CreateItem adds an item to the queue.
func flowName(f *domain.Flow) string {
	if f == nil {
		return "?"
	}
	return f.Name
}

func (s *Service) CreateItem(ctx context.Context, project string, a Actor, in NewItem) (CreateResult, error) {
	p, err := s.Project(project)
	if err != nil {
		return CreateResult{}, err
	}
	if err := a.Require(ScopeItemsWrite, p.Name); err != nil {
		return CreateResult{}, err
	}
	if in.External {
		in.Title = domain.SanitizeExternal(in.Title)
		in.Body = domain.SanitizeExternal(in.Body)
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return CreateResult{}, fmt.Errorf("%w: an item needs a title", ErrInvalid)
	}
	if in.Kind == "" {
		in.Kind = "feature"
	}
	prio, err := domain.ParsePriority(in.Priority)
	if err != nil {
		return CreateResult{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if _, err := p.Cfg().FlowOf(in.Kind, in.Area); err != nil {
		return CreateResult{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	// Bugs found by agents at P2/P3 wait as proposals; P0/P1 enter directly.
	if in.Kind == "bug" && domain.BugNeedsProposal(prio) && a.Kind == ActorAgent {
		pr, err := s.Propose(ctx, p.Name, a, domain.Proposal{Title: in.Title, Problem: in.Body, Evidence: "reported while working", Impact: "bug " + prio.String(),
			Size: def(in.Size, "unknown"), Area: def(in.Area, "general"), GoalLink: "quality", Kind: "bug", Priority: prio})
		if err != nil {
			return CreateResult{}, err
		}
		return CreateResult{Proposal: pr.Proposal, Note: "P2 and P3 bugs wait as proposals"}, nil
	}
	for i := range in.Criteria {
		c := &in.Criteria[i]
		if c.ID == "" {
			c.ID = fmt.Sprintf("C%d", i+1)
		}
		c.Met, c.Evidence = false, ""
		if !in.External {
			if err := domain.ValidateEARS(c.Text); err != nil {
				return CreateResult{}, fmt.Errorf("%w: %v", ErrInvalid, err)
			}
		}
	}
	for i, d := range in.Deps {
		id, err := domain.NormalizeItemID(d)
		if err != nil {
			return CreateResult{}, fmt.Errorf("%w: dependency %v", ErrInvalid, err)
		}
		in.Deps[i] = id
	}
	num, err := s.allocate(ctx, p, domain.KindItem)
	if err != nil {
		return CreateResult{}, err
	}
	base := ""
	if p.Ports().Git != nil {
		base, _ = p.Ports().Git.MainHead(ctx)
	}
	now := s.d.Clock.Now().UTC()
	p.mu.Lock()
	goalID := in.GoalID
	lane := domain.Lane(in.Lane)
	if lane == "" {
		g := p.state.ActiveGoal()
		inGoal := goalID != "" || (g != nil && in.Kind != "bug" && in.Kind != "improvement" && in.Kind != "self_improvement")
		if inGoal && goalID == "" && g != nil {
			goalID = g.ID
		}
		lane = domain.LaneFor(in.Kind, prio, inGoal, in.Kind == "self_improvement")
		if in.Kind == "improvement" {
			lane = domain.LaneImprovement
		}
	}
	area := domain.AreaName(in.Area)
	if area == "" && len(in.Anchors.Files) > 0 {
		area = p.Cfg().AreaOfFile(in.Anchors.Files[0])
	}
	in.Anchors.Base = base
	it := &domain.Item{
		ID: domain.FormatID(domain.KindItem, num), Version: 1, Title: in.Title, Body: in.Body, Kind: in.Kind, Priority: prio, Lane: lane,
		Area: area, GoalID: goalID, Size: in.Size, Class: in.Class, Status: domain.StatusDraft, Criteria: in.Criteria, Preserve: in.Preserve,
		Deps: in.Deps, Anchors: in.Anchors, Checklist: in.Checklist, Risky: in.Risky, TrackerRef: in.TrackerRef, Adopted: in.External && in.TrackerRef != "",
		CreatedAt: now, UpdatedAt: now, ValidatedAt: now,
	}
	it.Clarifications = domain.FindClarifications(in.Body)
	it.RefreshReadiness(true)
	if !it.DepsSatisfied(func(id string) *domain.Item { return p.state.Items[id] }) && it.Status == domain.StatusReady {
		it.Status = domain.StatusBlocked
	}
	_, err = s.emitLocked(ctx, p, domain.EvItemCreated, it.ID, actorName(a), domain.Payload{Item: it})
	p.mu.Unlock()
	if err != nil {
		return CreateResult{}, err
	}
	s.syncTracker(p, it.ID, in.TrackerRef == "")
	s.runtime.Poke()
	return CreateResult{Item: it.Clone()}, nil
}

// allocate returns the next number of a kind, through the arbiter when present.
func (s *Service) allocate(ctx context.Context, p *Project, k domain.IDKind) (int, error) {
	p.mu.Lock()
	local := p.state.Counters[k] + 1
	p.mu.Unlock()
	if p.Ports().Arbiter == nil {
		return local, nil
	}
	for {
		n, err := p.Ports().Arbiter.Next(ctx, "ids-"+string(k))
		if err != nil {
			return 0, fmt.Errorf("allocate %s id: %w", k, err)
		}
		if n >= local {
			return n, nil
		}
		// The counter lags behind the log (imported history): skip ahead.
		if n < local {
			continue
		}
	}
}

// ItemPatch changes an item; nil fields stay.
type ItemPatch struct {
	Title    *string `json:"title,omitempty"`
	Body     *string `json:"body,omitempty"`
	Priority *string `json:"priority,omitempty"`
	Area     *string `json:"area,omitempty"`
	Size     *string `json:"size,omitempty"`
	Class    *string `json:"class,omitempty"`
	// Branch makes the item continue on a branch that already exists (work
	// started before Barnraise drove the project). Only before work starts.
	Branch         *string             `json:"branch,omitempty"`
	Criteria       *[]domain.Criterion `json:"criteria,omitempty"`
	Preserve       *[]string           `json:"preserve,omitempty"`
	Deps           *[]string           `json:"deps,omitempty"`
	Anchors        *domain.Anchors     `json:"anchors,omitempty"`
	Clarifications *[]string           `json:"clarifications,omitempty"`
	Checklist      *[]string           `json:"checklist,omitempty"`
	Risky          *bool               `json:"risky,omitempty"`
	Kind           *string             `json:"kind,omitempty"`
	Reason         string              `json:"reason,omitempty"`
}

// UpdateItem edits an item. Only analysts and the owner rewrite criteria.
func (s *Service) UpdateItem(ctx context.Context, project string, a Actor, id string, patch ItemPatch) (*domain.Item, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeItemsWrite, p.Name); err != nil {
		return nil, err
	}
	id, err = domain.NormalizeItemID(id)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	it, err := s.mutateItem(ctx, p, id, domain.EvItemUpdated, actorName(a), func(it *domain.Item) error {
		if it.Status.Terminal() {
			return fmt.Errorf("%w: %s is %s", ErrConflict, it.ID, it.Status)
		}
		if patch.Title != nil {
			it.Title = strings.TrimSpace(*patch.Title)
		}
		if patch.Body != nil {
			it.Body = *patch.Body
			it.Clarifications = domain.FindClarifications(it.Body)
		}
		if patch.Priority != nil {
			pr, err := domain.ParsePriority(*patch.Priority)
			if err != nil {
				return fmt.Errorf("%w: %v", ErrInvalid, err)
			}
			it.Priority = pr
		}
		if patch.Area != nil {
			to := domain.AreaName(*patch.Area)
			// An item already moving keeps its flow: its current step may not exist in another one.
			if it.Step != "" && to != it.Area {
				from, _ := p.Cfg().FlowOf(it.Kind, it.Area)
				next, _ := p.Cfg().FlowOf(it.Kind, to)
				if from != next {
					return fmt.Errorf("%w: %s is at step %s of flow %s; area %s uses another flow. Move it before work starts, or `barnraise item redo` it", ErrInvalid, it.ID, it.Step, flowName(from), to)
				}
			}
			it.Area = to
		}
		if patch.Size != nil {
			it.Size = *patch.Size
		}
		if patch.Class != nil {
			it.Class = *patch.Class
		}
		if patch.Branch != nil {
			b := strings.TrimSpace(*patch.Branch)
			switch {
			case a.Kind == ActorAgent:
				return fmt.Errorf("%w: an agent does not choose the branch of an item", ErrForbidden)
			case it.Step != "":
				return fmt.Errorf("%w: %s already started on branch %s", ErrInvalid, it.ID, it.Branch)
			case b == p.Cfg().MainBranch || strings.ContainsAny(b, " ~^:?*[\\") || strings.HasPrefix(b, "-") || strings.Contains(b, ".."):
				return fmt.Errorf("%w: %q is not a branch an item can use", ErrInvalid, b)
			}
			it.Branch = b
		}
		if patch.Kind != nil {
			if _, err := p.Cfg().FlowOf(*patch.Kind, it.Area); err != nil {
				return fmt.Errorf("%w: %v", ErrInvalid, err)
			}
			if it.Step != "" {
				return fmt.Errorf("%w: cannot change the kind of an item that already started", ErrConflict)
			}
			it.Kind = *patch.Kind
		}
		if patch.Criteria != nil {
			old := map[string]domain.Criterion{}
			for _, c := range it.Criteria {
				old[c.ID] = c
			}
			crit := append([]domain.Criterion(nil), (*patch.Criteria)...)
			for i := range crit {
				if crit[i].ID == "" {
					crit[i].ID = fmt.Sprintf("C%d", i+1)
				}
				if err := domain.ValidateEARS(crit[i].Text); err != nil {
					return fmt.Errorf("%w: %v", ErrInvalid, err)
				}
				// Rewriting a criterion clears its mark unless the text is the same.
				if o, ok := old[crit[i].ID]; ok && o.Text == crit[i].Text {
					crit[i].Met, crit[i].Evidence = o.Met, o.Evidence
				} else {
					crit[i].Met, crit[i].Evidence = false, ""
				}
			}
			it.Criteria = crit
		}
		if patch.Preserve != nil {
			it.Preserve = *patch.Preserve
		}
		if patch.Deps != nil {
			var deps []string
			for _, d := range *patch.Deps {
				n, err := domain.NormalizeItemID(d)
				if err != nil {
					return fmt.Errorf("%w: %v", ErrInvalid, err)
				}
				if n == it.ID {
					return fmt.Errorf("%w: an item cannot depend on itself", ErrInvalid)
				}
				deps = append(deps, n)
			}
			it.Deps = deps
		}
		if patch.Anchors != nil {
			base := it.Anchors.Base
			it.Anchors = *patch.Anchors
			if it.Anchors.Base == "" {
				it.Anchors.Base = base
			}
		}
		if patch.Clarifications != nil {
			it.Clarifications = *patch.Clarifications
		}
		if patch.Checklist != nil {
			it.Checklist = *patch.Checklist
		}
		if patch.Risky != nil {
			it.Risky = *patch.Risky
		}
		s.refreshStatusLocked(p, it)
		return nil
	}, domain.Payload{Note: patch.Reason})
	if err != nil {
		return nil, err
	}
	s.syncTracker(p, id, false)
	s.runtime.Poke()
	return it, nil
}

// refreshStatusLocked recomputes draft/ready/blocked for items not started.
func (s *Service) refreshStatusLocked(p *Project, it *domain.Item) {
	switch it.Status {
	case domain.StatusDraft, domain.StatusReady, domain.StatusBlocked:
	default:
		return
	}
	if it.Status == domain.StatusBlocked {
		it.Status = domain.StatusReady
	}
	it.RefreshReadiness(true)
	if it.Status == domain.StatusReady && !it.DepsSatisfied(func(id string) *domain.Item { return p.state.Items[id] }) {
		it.Status = domain.StatusBlocked
	}
}

// GetItem returns one item.
func (s *Service) GetItem(project string, a Actor, id string) (*domain.Item, error) {
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
	p.mu.Lock()
	defer p.mu.Unlock()
	it, ok := p.state.Items[id]
	if !ok {
		return nil, fmt.Errorf("%w: item %s", ErrNotFound, id)
	}
	return it.Clone(), nil
}

// ItemFilter narrows item lists.
type ItemFilter struct {
	Status string `json:"status,omitempty"`
	Area   string `json:"area,omitempty"`
	Lane   string `json:"lane,omitempty"`
	Goal   string `json:"goal,omitempty"`
	Kind   string `json:"kind,omitempty"`
	All    bool   `json:"all,omitempty"` // include integrated and closed
}

// ListItems returns items by priority.
func (s *Service) ListItems(project string, a Actor, f ItemFilter) ([]*domain.Item, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeItemsRead, p.Name); err != nil {
		return nil, err
	}
	snap := p.Snapshot()
	var out []*domain.Item
	for _, it := range snap.ItemList() {
		if !f.All && f.Status == "" && it.Status.Terminal() {
			continue
		}
		if f.Status != "" && string(it.Status) != f.Status {
			continue
		}
		if f.Area != "" && domain.AreaName(f.Area) != it.Area {
			continue
		}
		if f.Lane != "" && string(it.Lane) != f.Lane {
			continue
		}
		if f.Goal != "" && it.GoalID != f.Goal {
			continue
		}
		if f.Kind != "" && it.Kind != f.Kind {
			continue
		}
		out = append(out, it)
	}
	return out, nil
}

// CloseItem closes an item as obsolete, duplicate, done by another, or abandoned.
func (s *Service) CloseItem(ctx context.Context, project string, a Actor, id, reason, by, note string) (*domain.Item, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeItemsWrite, p.Name); err != nil {
		return nil, err
	}
	switch reason {
	case "obsolete", "duplicate", "done_by", "abandoned", "split":
	default:
		return nil, fmt.Errorf("%w: reason must be obsolete, duplicate, done_by, split or abandoned", ErrInvalid)
	}
	id, err = domain.NormalizeItemID(id)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	typ := domain.EvItemClosed
	switch reason {
	case "obsolete":
		typ = domain.EvItemObsoleted
	case "duplicate":
		typ = domain.EvItemMergedInto
	case "done_by":
		typ = domain.EvItemSuperseded
	}
	it, err := s.mutateItem(ctx, p, id, typ, actorName(a), func(it *domain.Item) error {
		if it.Status.Terminal() {
			return fmt.Errorf("%w: %s is already %s", ErrConflict, it.ID, it.Status)
		}
		it.Status = domain.StatusClosed
		it.ClosedReason = reason
		it.ClosedBy = by
		it.Claim = nil
		return nil
	}, domain.Payload{Note: note, Cause: by})
	if err != nil {
		return nil, err
	}
	s.releaseLease(ctx, p, id)
	s.stopItemSessions(id)
	if p.Ports().CodeHost != nil && it.PR != "" {
		go func() {
			msg := "Closed by Barnraise: " + reason
			if by != "" {
				msg += " (" + by + ")"
			}
			if note != "" {
				msg += ". " + note
			}
			_ = p.Ports().CodeHost.Close(context.Background(), prFromRef(it.PR), msg)
		}()
	}
	s.syncTracker(p, id, false)
	s.unblockDependents(ctx, p)
	return it, nil
}

// unblockDependents moves blocked items whose dependencies are done back to ready.
func (s *Service) unblockDependents(ctx context.Context, p *Project) {
	p.mu.Lock()
	var ids []string
	for _, it := range p.state.Items {
		if it.Status == domain.StatusBlocked && it.Step == "" && it.DepsSatisfied(func(id string) *domain.Item { return p.state.Items[id] }) {
			ids = append(ids, it.ID)
		}
	}
	for _, id := range ids {
		_, _ = s.mutateItemLocked(ctx, p, id, domain.EvItemUpdated, "barnraise", func(it *domain.Item) error {
			it.Status = domain.StatusReady
			it.RefreshReadiness(true)
			return nil
		}, domain.Payload{Note: "dependencies done"})
	}
	p.mu.Unlock()
	if len(ids) > 0 {
		s.runtime.Poke()
	}
}

func def(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}
