package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// askLocked records an owner question. Callers hold p.mu.
func (s *Service) askLocked(ctx context.Context, p *Project, q *domain.Question, by string) (string, error) {
	if err := q.Validate(); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	q.ID = p.state.NextID(domain.KindQuestion)
	q.Version = 1
	q.AskedBy = by
	q.AskedAt = s.d.Clock.Now().UTC()
	if _, err := s.emitLocked(ctx, p, domain.EvOwnerQuestionAsked, q.ID, by, domain.Payload{Question: q}); err != nil {
		return "", err
	}
	return q.ID, nil
}

// AskOwner is the ask_owner tool: context, the decision, 2-4 options with one
// recommended, and what happens without an answer. The item waits; the
// agent takes other work.
func (s *Service) AskOwner(ctx context.Context, project string, a Actor, q domain.Question) (*domain.Question, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeAsk, p.Name); err != nil {
		return nil, err
	}
	if q.ItemID == "" {
		q.ItemID = a.ItemID
	}
	if q.ItemID != "" {
		if q.ItemID, err = domain.NormalizeItemID(q.ItemID); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	p.mu.Lock()
	// Implementers ask only by exception: one open question per item.
	if a.Kind == ActorAgent && a.Role != "analyst" {
		for _, oq := range p.state.Questions {
			if oq.Open() && oq.ItemID == q.ItemID && q.ItemID != "" {
				p.mu.Unlock()
				return nil, fmt.Errorf("%w: %s already has an open question (%s)", ErrConflict, q.ItemID, oq.ID)
			}
		}
	}
	id, err := s.askLocked(ctx, p, &q, actorName(a))
	if err != nil {
		p.mu.Unlock()
		return nil, err
	}
	if q.ItemID != "" {
		if it, ok := p.state.Items[q.ItemID]; ok && !it.Status.Terminal() {
			_, _ = s.mutateItemLocked(ctx, p, q.ItemID, domain.EvItemWaitingOwner, actorName(a), func(x *domain.Item) error {
				x.Status = domain.StatusWaitingOwner
				x.WaitingQuestion = id
				return nil
			}, domain.Payload{Note: q.Decision})
			delete(p.running, q.ItemID)
		}
	}
	out := *p.state.Questions[id]
	p.mu.Unlock()
	s.notifyOwnerQuestion(ctx, p, &out)
	return &out, nil
}

// Answer resolves an owner question. Answers to item questions move the item.
func (s *Service) Answer(ctx context.Context, project string, a Actor, qid, answer, note string) (*domain.Question, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeAnswer, p.Name); err != nil {
		return nil, err
	}
	qid = strings.ToUpper(strings.TrimSpace(qid))
	p.mu.Lock()
	cur, ok := p.state.Questions[qid]
	if !ok {
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: question %s", ErrNotFound, qid)
	}
	if !cur.Open() {
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: %s was already answered: %s", ErrConflict, qid, cur.Answer)
	}
	choice, err := cur.MatchAnswer(answer)
	if err != nil {
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	q := *cur
	q.Version++
	q.Answer = choice
	if note != "" {
		q.Answer = choice + " — " + note
	}
	q.AnsweredBy = actorName(a)
	q.AnsweredAt = s.d.Clock.Now().UTC()
	if _, err := s.emitLocked(ctx, p, domain.EvOwnerQuestionAnswer, qid, actorName(a), domain.Payload{Question: &q}); err != nil {
		p.mu.Unlock()
		return nil, err
	}
	var it *domain.Item
	if q.ItemID != "" {
		it = p.state.Items[q.ItemID]
	}
	p.mu.Unlock()
	if it != nil && it.WaitingQuestion == qid {
		if err := s.applyAnswer(ctx, p, a, it, choice, cur.ActionOf(choice), note); err != nil {
			return &q, err
		}
	}
	return &q, nil
}

// applyAnswer turns an owner's answer into the item's next move.
func (s *Service) applyAnswer(ctx context.Context, p *Project, a Actor, it *domain.Item, choice, action, note string) error {
	lc := strings.ToLower(choice)
	// Options Barnraise offers carry their action, whatever the language of
	// the label; older questions and the ones agents write are read by label.
	if action != "" {
		lc = map[string]string{"approve": "approve", "rework": "rework", "abandon": "abandon", "split": "split",
			"accept": "accept", "stronger": "stronger retry", "retry": "retry", "answer": "answer", "update": "update", "keep": "keep"}[action]
	}
	f, err := p.Cfg().FlowOf(it.Kind, it.Area)
	if err != nil {
		return err
	}
	st, _ := f.Step(it.Step)
	switch {
	case st.Type == domain.StepApproval && strings.HasPrefix(lc, "approve"):
		_, _, err := s.finishStep(ctx, p, stepOutcome{Item: it.ID, Step: it.Step, Outcome: domain.OutcomeApprove, Reason: note, Actor: actorName(a)})
		return err
	case st.Type == domain.StepApproval && strings.Contains(lc, "rework"):
		_, _, err := s.finishStep(ctx, p, stepOutcome{Item: it.ID, Step: it.Step, Outcome: domain.OutcomeReject, Reason: def(note, "the owner asked for rework"), Actor: actorName(a)})
		return err
	case strings.Contains(lc, "abandon"):
		_, err := s.CloseItem(ctx, p.Name, Owner(a.Name), it.ID, "abandoned", "", note)
		return err
	case strings.Contains(lc, "split"):
		_, err := s.CloseItem(ctx, p.Name, Owner(a.Name), it.ID, "split", "", def(note, "split by the owner"))
		return err
	case strings.Contains(lc, "accept") && it.Step != "":
		// Accept with a caveat: skip forward to the merge queue. A draft
		// never started has nothing to accept: it goes back to refining.
		next := ""
		for _, x := range f.Steps {
			if x.Type == domain.StepMergeQueue {
				next = x.Name
			}
		}
		if next == "" {
			next = f.Steps[len(f.Steps)-1].Name
		}
		_, err := s.mutateItem(ctx, p, it.ID, domain.EvItemResumed, actorName(a), func(x *domain.Item) error {
			x.Step = next
			x.Status = domain.StatusQueued
			x.WaitingQuestion = ""
			x.StepStartedAt = s.d.Clock.Now().UTC()
			return nil
		}, domain.Payload{Note: "accepted with a caveat: " + note})
		s.runtime.Poke()
		return err
	default:
		// Retry with a stronger model, or continue with the given context.
		_, err := s.mutateItem(ctx, p, it.ID, domain.EvItemResumed, actorName(a), func(x *domain.Item) error {
			x.Status = domain.StatusInProgress
			x.WaitingQuestion = ""
			x.StepStartedAt = s.d.Clock.Now().UTC()
			if strings.Contains(lc, "stronger") {
				x.Tier++
				// The rounds of the old budget keep their lessons to learn.
				pastRework(x)
				x.Rework = nil // a fresh budget of rounds on the stronger model
			}
			if x.Claim == nil {
				// Back to the queue only when it can start: a draft answered
				// while it was being refined stays a draft (no criteria, an
				// open [NEEDS CLARIFICATION]) and the analyst refines it again.
				x.Status = domain.StatusReady
				x.RefreshReadiness(true)
			}
			return nil
		}, domain.Payload{Note: note})
		if err == nil {
			// The answer itself goes to the item, not only a note: the next
			// session is often another agent, which did not ask.
			text := ot(p, "answered.message", choice)
			if note != "" {
				text += " — " + note
			}
			_, _ = s.SendMessage(ctx, p.Name, System(), domain.Message{To: "item:" + it.ID, ItemID: it.ID, Text: text})
		}
		s.runtime.Poke()
		return err
	}
}

// Approve approves or rejects, naming what: the plan, the delivery or a proposal.
func (s *Service) Approve(ctx context.Context, project string, a Actor, target, what string, approve bool, note string) error {
	p, err := s.Project(project)
	if err != nil {
		return err
	}
	if err := a.Require(ScopeApprove, p.Name); err != nil {
		return err
	}
	if strings.HasPrefix(strings.ToUpper(target), "P-") {
		if what != "" && what != "proposal" {
			return fmt.Errorf("%w: %s is a proposal: approve the proposal", ErrInvalid, target)
		}
		decision := "reject"
		if approve {
			decision = "approve"
		}
		_, err := s.DecideProposal(ctx, p.Name, a, target, decision, note)
		return err
	}
	id, err := domain.NormalizeItemID(target)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	p.mu.Lock()
	it, ok := p.state.Items[id]
	if !ok {
		p.mu.Unlock()
		return fmt.Errorf("%w: item %s", ErrNotFound, id)
	}
	it = it.Clone()
	f, _ := p.Cfg().FlowOf(it.Kind, it.Area)
	st, _ := f.Step(it.Step)
	p.mu.Unlock()
	if st.Type != domain.StepApproval && !(st.Type == domain.StepAgent && st.Review) {
		return fmt.Errorf("%w: %s is at step %s, which waits for no approval", ErrConflict, id, it.Step)
	}
	expected := approvalWhat(p, it, st)
	if st.Type == domain.StepAgent {
		expected = "delivery"
	}
	if what == "" {
		return fmt.Errorf("%w: say what you approve: the %s of %s", ErrInvalid, expected, id)
	}
	if what != expected {
		return fmt.Errorf("%w: %s waits for approval of the %s, not the %s", ErrConflict, id, expected, what)
	}
	if st.Type == domain.StepApproval && st.Approver != "owner" && a.Kind == ActorAgent && a.Role != st.Approver {
		return fmt.Errorf("%w: this approval belongs to %s", ErrForbidden, st.Approver)
	}
	outcome := domain.OutcomeApprove
	if !approve {
		outcome = domain.OutcomeReject
	}
	// Close the pending question, if any.
	if it.WaitingQuestion != "" {
		p.mu.Lock()
		if q, ok := p.state.Questions[it.WaitingQuestion]; ok && q.Open() {
			qq := *q
			qq.Version++
			qq.Answer = fmt.Sprintf("%v (%s)", map[bool]string{true: "approved", false: "rework"}[approve], note)
			qq.AnsweredBy, qq.AnsweredAt = actorName(a), s.d.Clock.Now().UTC()
			_, _ = s.emitLocked(ctx, p, domain.EvOwnerQuestionAnswer, qq.ID, actorName(a), domain.Payload{Question: &qq})
		}
		p.mu.Unlock()
	}
	_, _, err = s.finishStep(ctx, p, stepOutcome{Item: id, Step: it.Step, Outcome: outcome, Reason: def(note, "rejected by "+actorName(a)), Actor: actorName(a)})
	return err
}

// SetGoal sets the long-term goal with a measurable end, and generates the inventory.
func (s *Service) SetGoal(ctx context.Context, project string, a Actor, g domain.Goal) (*domain.Goal, []*domain.Item, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, nil, err
	}
	if err := a.Require(ScopeFocus, p.Name); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(g.Name) == "" {
		return nil, nil, fmt.Errorf("%w: a goal needs a name", ErrInvalid)
	}
	if strings.TrimSpace(g.DoneWhen) == "" {
		return nil, nil, fmt.Errorf("%w: a goal needs a measurable end (done_when)", ErrInvalid)
	}
	p.mu.Lock()
	g.ID = p.state.NextID(domain.KindGoal)
	g.Active = true
	g.CreatedAt = s.d.Clock.Now().UTC()
	_, err = s.emitLocked(ctx, p, domain.EvGoalSet, g.ID, actorName(a), domain.Payload{Goal: &g})
	// A new goal answers the open "what is the next goal?" question.
	for _, q := range p.state.OpenQuestions() {
		if q.ItemID == "" && strings.Contains(q.Decision, "next goal") {
			qq := *q
			qq.Version++
			qq.Answer, qq.AnsweredBy, qq.AnsweredAt = "Set a new goal — "+g.ID, actorName(a), s.d.Clock.Now().UTC()
			_, _ = s.emitLocked(ctx, p, domain.EvOwnerQuestionAnswer, qq.ID, actorName(a), domain.Payload{Question: &qq})
		}
	}
	p.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	var created []*domain.Item
	if g.Inventory != "" {
		entries, err := s.inventory(p, g.Inventory)
		if err != nil {
			return &g, nil, err
		}
		for _, e := range entries {
			res, err := s.CreateItem(ctx, p.Name, a, NewItem{Title: e.Title, Kind: "feature", GoalID: g.ID, Area: e.Area, Body: e.Body,
				Anchors: domain.Anchors{Specs: []string{e.Spec}, Areas: []string{e.Area}}})
			if err != nil {
				return &g, created, err
			}
			if res.Item != nil {
				created = append(created, res.Item)
			}
		}
	}
	s.runtime.Poke()
	return &g, created, nil
}

// Focus sets a temporary priority; with preview it only returns the effect.
func (s *Service) Focus(ctx context.Context, project string, a Actor, f domain.Focus, preview bool) (domain.FocusEffect, error) {
	p, err := s.Project(project)
	if err != nil {
		return domain.FocusEffect{}, err
	}
	if err := a.Require(ScopeFocus, p.Name); err != nil {
		return domain.FocusEffect{}, err
	}
	if f.Share == 0 {
		f.Share = 80
	}
	if f.Share < 0 || f.Share > 100 {
		return domain.FocusEffect{}, fmt.Errorf("%w: focus share must be 1-100%%", ErrInvalid)
	}
	for i, id := range f.Items {
		n, err := domain.NormalizeItemID(id)
		if err != nil {
			return domain.FocusEffect{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		f.Items[i] = n
	}
	if f.Area != "" {
		f.Area = domain.AreaName(f.Area)
	}
	if f.Name == "" {
		f.Name = f.Area
		if f.Name == "" {
			f.Name = strings.Join(f.Items, ", ")
		}
	}
	snap := p.Snapshot()
	effect := domain.PreviewFocus(&f, snap.ItemList())
	if f.Area == "" && len(f.Items) == 0 {
		return effect, fmt.Errorf("%w: a focus names an area or items", ErrInvalid)
	}
	if effect.FocusItems == 0 {
		areas := map[string]bool{}
		for _, it := range snap.ItemList() {
			if it.Area != "" && !it.Status.Terminal() {
				areas[it.Area] = true
			}
		}
		return effect, fmt.Errorf("%w: no live items in %s; areas with live items: %s", ErrInvalid, f.Name, strings.Join(sortedKeys(areas), ", "))
	}
	if preview {
		return effect, nil
	}
	f.SetBy = actorName(a)
	f.SetAt = s.d.Clock.Now().UTC()
	if _, err := s.emit(ctx, p, domain.EvFocusChanged, "", actorName(a), domain.Payload{Focus: &f}); err != nil {
		return effect, err
	}
	s.revalidateAsync(p, domain.Change{Kind: "focus", Cause: "focus " + f.Name, Areas: []string{f.Area}, Items: f.Items})
	s.runtime.Poke()
	return effect, nil
}

// ClearFocus ends the focus; capacity returns to the goal.
func (s *Service) ClearFocus(ctx context.Context, project string, a Actor) error {
	p, err := s.Project(project)
	if err != nil {
		return err
	}
	if err := a.Require(ScopeFocus, p.Name); err != nil {
		return err
	}
	_, err = s.emit(ctx, p, domain.EvFocusChanged, "", actorName(a), domain.Payload{Focus: nil, Note: "focus cleared"})
	// Parked items go back to the queue.
	p.mu.Lock()
	for _, it := range p.state.Items {
		if it.Parked {
			_, _ = s.mutateItemLocked(ctx, p, it.ID, domain.EvItemUpdated, "barnraise", func(x *domain.Item) error { x.Parked = false; return nil }, domain.Payload{})
		}
	}
	p.mu.Unlock()
	s.runtime.Poke()
	return err
}

// Pause is the emergency button: no new steps start, running sessions stop.
func (s *Service) Pause(ctx context.Context, project string, a Actor, why string) error {
	p, err := s.Project(project)
	if err != nil {
		return err
	}
	if err := a.Require(ScopePause, p.Name); err != nil {
		return err
	}
	if _, err := s.emit(ctx, p, domain.EvSystemPaused, "", actorName(a), domain.Payload{Note: why}); err != nil {
		return err
	}
	s.runtime.StopProject(p.Name)
	return nil
}

// Resume lifts the pause.
func (s *Service) Resume(ctx context.Context, project string, a Actor) error {
	p, err := s.Project(project)
	if err != nil {
		return err
	}
	if err := a.Require(ScopePause, p.Name); err != nil {
		return err
	}
	_, err = s.emit(ctx, p, domain.EvSystemResumed, "", actorName(a), domain.Payload{})
	s.runtime.Poke()
	return err
}

// Takeover pauses the agent on an item and hands its worktree to a person.
func (s *Service) Takeover(ctx context.Context, project string, a Actor, id string) (string, *domain.Item, error) {
	p, err := s.Project(project)
	if err != nil {
		return "", nil, err
	}
	if err := a.Require(ScopeApprove, p.Name); err != nil {
		return "", nil, err
	}
	id, err = domain.NormalizeItemID(id)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	s.stopItemSessions(id)
	it, err := s.mutateItem(ctx, p, id, domain.EvItemPaused, actorName(a), func(x *domain.Item) error {
		if x.Status.Terminal() {
			return fmt.Errorf("%w: %s is %s", ErrConflict, x.ID, x.Status)
		}
		if x.Claim != nil && x.Claim.Machine != s.d.Machine {
			return fmt.Errorf("%w: %s is held by machine %s; take it over there", ErrConflict, x.ID, x.Claim.Machine)
		}
		if x.Branch == "" {
			// Never claimed: name the branch now so the person's work is pushed.
			x.Branch = domain.BranchName(x.ID, x.Title)
		}
		x.Status = domain.StatusPaused
		return nil
	}, domain.Payload{Note: "taken over by " + actorName(a)})
	if err != nil {
		return "", nil, err
	}
	if it.Claim == nil && p.Ports().Arbiter != nil {
		// Reserve it so no machine starts it meanwhile.
		if l, err := p.Ports().Arbiter.Claim(ctx, claimKey(id), s.d.Machine); err == nil {
			p.mu.Lock()
			p.leases[id] = l
			p.mu.Unlock()
		}
	}
	dir, err := s.ensureWorkspace(ctx, p, id)
	return dir, it, err
}

// Handback returns a taken-over item to the flow, at its current step.
func (s *Service) Handback(ctx context.Context, project string, a Actor, id, note string) (*domain.Item, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeApprove, p.Name); err != nil {
		return nil, err
	}
	id, err = domain.NormalizeItemID(id)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if _, err := s.commitStep(ctx, p, id, "chore("+id+"): changes by "+actorName(a)); err != nil && strings.Contains(err.Error(), "secret") {
		return nil, err
	}
	it, err := s.mutateItem(ctx, p, id, domain.EvItemResumed, actorName(a), func(x *domain.Item) error {
		if x.Status != domain.StatusPaused {
			return fmt.Errorf("%w: %s was not taken over", ErrConflict, x.ID)
		}
		x.Status = domain.StatusInProgress
		if x.Claim == nil {
			x.Claim = &domain.Claim{Machine: s.d.Machine, Agent: actorName(a), Since: s.d.Clock.Now().UTC()}
		}
		if x.Step == "" {
			f, err := p.Cfg().FlowOf(x.Kind, x.Area)
			if err != nil {
				return err
			}
			x.Step = f.First()
		}
		x.StepStartedAt = s.d.Clock.Now().UTC()
		return nil
	}, domain.Payload{Note: note})
	if err == nil && note != "" {
		_, _ = s.SendMessage(ctx, p.Name, System(), domain.Message{To: "item:" + id, ItemID: id, Text: "Owner worked on this item: " + note})
	}
	s.runtime.Poke()
	return it, err
}

// Redo restarts an item from any step of its flow ("redo from here").
func (s *Service) Redo(ctx context.Context, project string, a Actor, id, step, why string) (*domain.Item, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeApprove, p.Name); err != nil {
		return nil, err
	}
	id, err = domain.NormalizeItemID(id)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	s.stopItemSessions(id)
	head := s.itemHead(ctx, p, id)
	it, err := s.mutateItem(ctx, p, id, domain.EvItemRedo, actorName(a), func(x *domain.Item) error {
		f, err := p.Cfg().FlowOf(x.Kind, x.Area)
		if err != nil {
			return err
		}
		if err := f.RedoFrom(step); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		if x.Status == domain.StatusIntegrated {
			return fmt.Errorf("%w: %s is integrated; create a new item", ErrConflict, x.ID)
		}
		if strings.TrimSpace(why) != "" {
			// The owner's correction: its lesson is learned once the item gets past it.
			x.PastRework = append(x.PastRework, domain.ReworkRecord{From: x.Step, To: step, By: actorName(a), Reason: why,
				Outcome: "redo", At: s.d.Clock.Now().UTC(), Commit: head})
		}
		x.Step = step
		x.StepStartedAt = s.d.Clock.Now().UTC()
		x.WaitingQuestion = ""
		if x.Claim != nil {
			x.Status = domain.StatusInProgress
		} else {
			x.Status = domain.StatusReady
			x.ClosedReason = ""
		}
		return nil
	}, domain.Payload{Note: why})
	s.runtime.Poke()
	return it, err
}

// SendMessage sends a short note tied to an item, always through the daemon.
func (s *Service) SendMessage(ctx context.Context, project string, a Actor, m domain.Message) (*domain.Message, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeMessage, p.Name); err != nil {
		return nil, err
	}
	m.Text = strings.TrimSpace(m.Text)
	if m.Text == "" || len(m.Text) > domain.MaxMessageLen {
		return nil, fmt.Errorf("%w: messages are 1 to %d characters", ErrInvalid, domain.MaxMessageLen)
	}
	if m.To == "" {
		return nil, fmt.Errorf("%w: a message needs a recipient (agent, role:<role> or item:<id>)", ErrInvalid)
	}
	if a.Kind == ActorAgent && m.ItemID == "" {
		m.ItemID = a.ItemID
	}
	if a.Kind == ActorAgent && m.ItemID == "" {
		return nil, fmt.Errorf("%w: agent messages are tied to an item", ErrInvalid)
	}
	m.ID = s.d.IDs.NewID()
	m.From = actorName(a)
	m.At = s.d.Clock.Now().UTC()
	if _, err := s.emit(ctx, p, domain.EvMessageSent, m.ItemID, m.From, domain.Payload{Message: &m}); err != nil {
		return nil, err
	}
	return &m, nil
}

// Inbox returns and marks delivered the messages for an agent.
func (s *Service) Inbox(ctx context.Context, project, agent, role, itemID string, mark bool) ([]*domain.Message, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	msgs := p.state.Inbox(agent, role, itemID)
	var out []*domain.Message
	for _, m := range msgs {
		mm := *m
		out = append(out, &mm)
		if mark {
			_, _ = s.emitLocked(ctx, p, domain.EvMessageDelivered, m.ItemID, agent, domain.Payload{Message: &domain.Message{ID: m.ID}})
		}
	}
	p.mu.Unlock()
	return out, nil
}

// Journal writes an agent's end-of-step note.
func (s *Service) Journal(ctx context.Context, project string, a Actor, text string) error {
	p, err := s.Project(project)
	if err != nil {
		return err
	}
	if err := a.Require(ScopeJournal, p.Name); err != nil {
		return err
	}
	_, err = s.emit(ctx, p, domain.EvJournal, a.ItemID, actorName(a), domain.Payload{Journal: &domain.JournalEntry{Agent: actorName(a), ItemID: a.ItemID, Step: a.Step, Text: truncate(text, 4000), At: s.d.Clock.Now().UTC()}})
	return err
}

// notifyOwner sends a notification when the kind interrupts, else it waits for the digest.
func (s *Service) notifyOwner(ctx context.Context, p *Project, kind, title, body string) {
	if s.d.Notifier == nil {
		return
	}
	urgent := map[string]bool{"rework_exhausted": true, "owner_approval": true, "secret": true, "deploy": true, "policy_violation": true, "accounts_exhausted": true, "question": true, "model_rule": true, "check_red_on_main": true, "trust": true, "update_failed": true,
		"ci_waits": true, "ci_denied": true, "queue_stuck": true}
	if !urgent[kind] {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	if err := s.d.Notifier.Notify(ctx, ports.Notification{Title: "[" + p.Name + "] " + title, Body: body, Urgent: true, Kind: kind}); err != nil {
		s.d.Log.Warn("notify owner", "err", err)
	}
}

func (s *Service) notifyOwnerQuestion(ctx context.Context, p *Project, q *domain.Question) {
	if s.d.Notifier == nil {
		return
	}
	var opts []string
	for _, o := range q.Options {
		l := o.Label
		if o.Recommended {
			l += " (recommended)"
		}
		opts = append(opts, l)
	}
	body := q.Context + "\n" + q.Decision + "\n- " + strings.Join(opts, "\n- ") + "\nIf nobody answers: " + q.IfNoAnswer + "\nAnswer: barnraise answer " + q.ID + " <option>"
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = s.d.Notifier.Notify(ctx, ports.Notification{Title: "[" + p.Name + "] " + q.ID + ": " + truncate(q.Decision, 80), Body: body, Urgent: q.Urgent, Options: opts, Kind: "question"})
	}()
}

// RecordViolation stores a tool call blocked by a hook; the owner hears about it at once.
func (s *Service) RecordViolation(ctx context.Context, project, agent, itemID, tool, rule, reason, excerpt string) error {
	p, err := s.Project(project)
	if err != nil {
		return err
	}
	_, err = s.emit(ctx, p, domain.EvPolicyViolation, itemID, agent, domain.Payload{Note: reason, Extra: map[string]any{"tool": tool, "rule": rule, "excerpt": truncate(excerpt, 500)}})
	if err == nil {
		go s.notifyOwner(context.Background(), p, "policy_violation", "Blocked: "+agent+" tried "+tool, reason+"\n"+truncate(excerpt, 300))
	}
	return err
}
