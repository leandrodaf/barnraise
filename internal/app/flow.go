package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// stepOutcome is a finished step, from any source.
type stepOutcome struct {
	Item     string
	Step     string
	Outcome  domain.Outcome
	Reason   string
	Actor    string
	Payload  domain.Payload
	RunInfo  *domain.StepRun
	Criteria []domain.CriterionMark
	Files    []string
}

// finishStep applies the flow engine's decision. The flow is code: Barnraise
// decides the next step, the rework and the integration.
func (s *Service) finishStep(ctx context.Context, p *Project, o stepOutcome) (*domain.Item, domain.Transition, error) {
	p.mu.Lock()
	cur, ok := p.state.Items[o.Item]
	if !ok {
		p.mu.Unlock()
		return nil, domain.Transition{}, fmt.Errorf("%w: item %s", ErrNotFound, o.Item)
	}
	if cur.Step != o.Step {
		p.mu.Unlock()
		return nil, domain.Transition{}, fmt.Errorf("%w: %s is at step %q, not %q", ErrConflict, o.Item, cur.Step, o.Step)
	}
	if cur.Status.Terminal() {
		p.mu.Unlock()
		return nil, domain.Transition{}, fmt.Errorf("%w: %s is %s", ErrConflict, cur.ID, cur.Status)
	}
	f, err := p.Cfg().FlowFor(cur.Kind)
	if err != nil {
		p.mu.Unlock()
		return nil, domain.Transition{}, err
	}
	tr, err := f.Decide(cur, o.Step, o.Outcome, o.Reason)
	if err != nil {
		p.mu.Unlock()
		return nil, tr, err
	}
	now := s.d.Clock.Now().UTC()
	focus := p.state.Focus
	focusActive := focus.Active(p.state.ItemList(), now)
	park := false
	evType := domain.EvItemStepFinished
	it, err := s.mutateItemLocked(ctx, p, o.Item, evType, o.Actor, func(it *domain.Item) error {
		for _, m := range o.Criteria {
			if err := it.MarkCriterion(m.ID, m.Met, m.Evidence); err != nil {
				return fmt.Errorf("%w: %v", ErrInvalid, err)
			}
		}
		if len(o.Files) > 0 {
			it.FilesTouched = domain.Dedupe(append(it.FilesTouched, o.Files...))
		}
		if o.RunInfo != nil {
			if it.Runs == nil {
				it.Runs = map[string]domain.StepRun{}
			}
			it.Runs[o.Step] = *o.RunInfo
		}
		it.Attempts = 0
		it.WaitingQuestion = ""
		switch tr.Kind {
		case domain.TransAdvance:
			it.Step = tr.NextStep
			it.StepStartedAt = now
			it.Status = domain.StatusInProgress
			if next, ok := f.Step(tr.NextStep); ok && next.Type == domain.StepMergeQueue {
				it.Status = domain.StatusQueued
			}
			if focusActive && !focus.Includes(it) && it.Lane == domain.LaneGoal && it.Status == domain.StatusInProgress {
				// Outside the focus: finish the current step and wait.
				it.Status = domain.StatusReady
				it.Parked = true
				it.Claim = nil
				park = true
			}
		case domain.TransRework:
			it.Rework = append(it.Rework, domain.ReworkRecord{From: o.Step, To: tr.NextStep, By: o.Actor, Reason: o.Reason, Outcome: string(o.Outcome), Tier: tr.Tier, At: now})
			it.Tier = tr.Tier
			it.Step = tr.NextStep
			it.StepStartedAt = now
			it.Status = domain.StatusInProgress
		case domain.TransOwner:
			it.Step = tr.NextStep
			it.Status = domain.StatusWaitingOwner
		case domain.TransBlocked:
			it.Status = domain.StatusBlocked
		case domain.TransComplete:
			it.Status = domain.StatusIntegrated
			it.IntegratedAt = now
			it.Claim = nil
		}
		return nil
	}, o.Payload)
	if err != nil {
		p.mu.Unlock()
		return nil, tr, err
	}
	switch tr.Kind {
	case domain.TransRework:
		rr := it.Rework[len(it.Rework)-1]
		_, _ = s.emitLocked(ctx, p, domain.EvReworkStarted, it.ID, o.Actor, domain.Payload{Rework: &rr})
		_, _ = s.emitLocked(ctx, p, domain.EvMessageSent, it.ID, "barnraise", domain.Payload{Message: &domain.Message{
			ID: s.d.IDs.NewID(), From: "barnraise", To: "item:" + it.ID, ItemID: it.ID, At: now,
			Text: fmt.Sprintf("Rework round %d from %s: %s", len(it.Rework), o.Step, truncate(o.Reason, 1500)),
		}})
	case domain.TransOwner, domain.TransBlocked:
		if tr.Kind == domain.TransBlocked {
			_, _ = s.emitLocked(ctx, p, domain.EvItemBlocked, it.ID, o.Actor, domain.Payload{Note: o.Reason})
		}
		q := s.ownerQuestionFor(it, o, tr)
		qid, err := s.askLocked(ctx, p, q, "barnraise")
		if err != nil {
			s.d.Log.Warn("owner question", "item", it.ID, "err", err)
		} else if upd, err := s.mutateItemLocked(ctx, p, it.ID, domain.EvItemWaitingOwner, "barnraise", func(x *domain.Item) error {
			x.WaitingQuestion = qid
			return nil
		}, domain.Payload{Note: q.Decision}); err == nil {
			it = upd
		}
	}
	p.mu.Unlock()

	if park {
		s.releaseLease(ctx, p, it.ID)
	}
	if tr.Kind == domain.TransComplete {
		s.releaseLease(ctx, p, it.ID)
	}
	if tr.Kind == domain.TransOwner {
		s.notifyOwner(ctx, p, "rework_exhausted", fmt.Sprintf("%s needs you", it.ID), it.Title+": "+truncate(o.Reason, 300))
		if len(it.Rework) > 0 && o.Outcome != domain.OutcomeNeedsContext {
			s.analyzeExhaustedRework(ctx, p, it)
		}
	}
	s.syncTracker(p, it.ID, false)
	s.runtime.Poke()
	return it, tr, nil
}

func (s *Service) ownerQuestionFor(it *domain.Item, o stepOutcome, tr domain.Transition) *domain.Question {
	q := &domain.Question{ItemID: it.ID, Urgent: true}
	switch {
	case tr.Kind == domain.TransBlocked:
		q.Context = fmt.Sprintf("%s (%s) is blocked at step %s: %s", it.ID, it.Title, o.Step, truncate(o.Reason, 500))
		q.Decision = "How should it continue?"
		q.Options = []domain.Option{{Label: "Retry the step", Recommended: true}, {Label: "Abandon the item"}}
		q.IfNoAnswer = "The item stays blocked; agents work on other items."
	case o.Outcome == domain.OutcomeNeedsContext:
		q.Context = fmt.Sprintf("%s (%s) needs information at step %s.", it.ID, it.Title, o.Step)
		q.Decision = truncate(o.Reason, 800)
		q.Options = []domain.Option{{Label: "Answer and continue", Recommended: true}, {Label: "Abandon the item"}}
		q.IfNoAnswer = "The item waits; agents work on other items."
	default:
		q.Context = fmt.Sprintf("%s (%s) came back %d times; last reason: %s", it.ID, it.Title, len(it.Rework), truncate(o.Reason, 400))
		q.Decision = "How should it continue?"
		q.Options = []domain.Option{{Label: "Retry with a stronger model", Recommended: true}, {Label: "Accept with a caveat"}, {Label: "Split the item"}, {Label: "Abandon the item"}}
		q.IfNoAnswer = "The item waits; agents work on other items."
	}
	return q
}

// analyzeExhaustedRework classifies why an item exhausted its rework and
// proposes the fix (role, standard or check), for the owner to approve.
func (s *Service) analyzeExhaustedRework(ctx context.Context, p *Project, it *domain.Item) {
	from := map[string]int{}
	for _, r := range it.Rework {
		from[r.From]++
	}
	top, n := "", 0
	for k, v := range from {
		if v > n || (v == n && k < top) {
			top, n = k, v
		}
	}
	cause := "the plan or the criteria"
	switch {
	case strings.Contains(top, "check"):
		cause = "a check (possibly flaky or too strict)"
	case strings.Contains(top, "review") || strings.Contains(top, "design") || strings.Contains(top, "look"):
		cause = "the reviewer's expectations versus the implementer's role or the area standard"
	case strings.Contains(top, "integrate"):
		cause = "conflicts with main (items too large or overlapping)"
	}
	last := it.Rework[len(it.Rework)-1].Reason
	_, _ = s.Propose(ctx, p.Name, System(), domain.Proposal{
		Title:    fmt.Sprintf("Rework exhausted on %s: fix %s", it.ID, top),
		Problem:  fmt.Sprintf("%s came back %d times, %d of them from step %s; the likely cause is %s.", it.ID, len(it.Rework), n, top, cause),
		Evidence: truncate(last, 600), Impact: "an item waits for the owner and the fleet spends tokens on retries",
		Size: "1h", Area: def(it.Area, "general"), GoalLink: "fewer rework rounds per item", Kind: "self_improvement", By: "barnraise"})
}

// StepDone is the public input to finish a driver step by a person or CI.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Deliver records an agent delivery. Loose text does not move an item.
func (s *Service) Deliver(ctx context.Context, project string, a Actor, d domain.Delivery) (*domain.Item, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeDeliver, p.Name); err != nil {
		return nil, err
	}
	if d.ItemID == "" {
		d.ItemID = a.ItemID
	}
	d.ItemID, err = domain.NormalizeItemID(d.ItemID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := checkItemAccess(a, d.ItemID); err != nil {
		return nil, err
	}
	p.mu.Lock()
	cur, ok := p.state.Items[d.ItemID]
	if !ok {
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: item %s", ErrNotFound, d.ItemID)
	}
	if d.Step == "" {
		d.Step = cur.Step
	}
	if d.Step != cur.Step || cur.Status != domain.StatusInProgress {
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: %s is at step %s (%s), not %s; this delivery is late", ErrConflict, cur.ID, cur.Step, cur.Status, d.Step)
	}
	if a.Kind == ActorAgent && a.Step != "" && a.Step != d.Step {
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: your session is for step %s", ErrForbidden, a.Step)
	}
	f, _ := p.Cfg().FlowFor(cur.Kind)
	var st domain.StepDef
	if f != nil {
		st, _ = f.Step(d.Step)
	}
	if st.Type != domain.StepAgent || st.Review {
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: step %s takes a review, not a delivery", ErrInvalid, d.Step)
	}
	if err := d.Validate(cur); err != nil {
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if st.Output == "failing_test" && d.Status == domain.DeliveryDone && len(d.Files) == 0 {
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: a reproduce step must list the failing test file in files", ErrInvalid)
	}
	p.mu.Unlock()
	d.Agent = actorName(a)
	d.At = s.d.Clock.Now().UTC()
	outcome, _ := d.Status.Outcome()

	if s.hasAlt(p, d.ItemID, d.Step) {
		return s.deliverAttempt(ctx, p, a, d)
	}
	// Commit and push the step's work before the transition: if this
	// machine dies, nothing is lost.
	if sha, err := s.commitStep(ctx, p, d.ItemID, fmt.Sprintf("%s: %s step %s", d.ItemID, strings.ToLower(string(d.Status)), d.Step)); err == nil && sha != "" {
		d.Commit = sha
	} else if err != nil {
		if strings.Contains(err.Error(), "secret") {
			return nil, err
		}
		s.d.Log.Warn("commit step", "item", d.ItemID, "err", err)
	}
	if _, err := s.emit(ctx, p, domain.EvDeliverySubmitted, d.ItemID, d.Agent, domain.Payload{Delivery: &d}); err != nil {
		return nil, err
	}
	reason := d.Reason
	if outcome == domain.OutcomeConcerns {
		reason = strings.Join(d.Concerns, "; ")
	}
	run := runInfo(a)
	it, tr, err := s.finishStep(ctx, p, stepOutcome{Item: d.ItemID, Step: d.Step, Outcome: outcome, Reason: reason, Actor: d.Agent,
		Criteria: d.Criteria, Files: d.Files, RunInfo: run, Payload: domain.Payload{Note: d.Summary}})
	if err != nil {
		return nil, err
	}
	s.afterDelivery(ctx, p, it, d, tr)
	return it, nil
}

func runInfo(a Actor) *domain.StepRun {
	if a.Kind != ActorAgent {
		return nil
	}
	return &domain.StepRun{Agent: a.Name, Account: a.Account, Provider: providerOf(a), Model: a.Model, Family: a.Family}
}

func providerOf(a Actor) string {
	if i := strings.IndexByte(a.Account, '-'); i > 0 {
		return a.Account[:i]
	}
	return ""
}

// afterDelivery publishes the step on the PR and marks it ready on DONE.
func (s *Service) afterDelivery(ctx context.Context, p *Project, it *domain.Item, d domain.Delivery, tr domain.Transition) {
	if p.Ports().CodeHost == nil || it.PR == "" {
		return
	}
	pr := prFromRef(it.PR)
	go func() {
		ctx := context.Background()
		body := fmt.Sprintf("**%s · %s** — %s\n\n%s", d.Step, d.Status, d.Agent, d.Summary)
		if len(d.Concerns) > 0 {
			body += "\n\nConcerns:\n- " + strings.Join(d.Concerns, "\n- ")
		}
		_ = p.Ports().CodeHost.Comment(ctx, pr, body)
		_ = p.Ports().CodeHost.UpdatePR(ctx, pr, prTitle(it), prBody(p, it))
		if d.Status == domain.DeliveryDone && isImplementStep(p, it, d.Step) {
			_ = p.Ports().CodeHost.MarkReady(ctx, pr)
		}
	}()
}

func isImplementStep(p *Project, it *domain.Item, step string) bool {
	f, err := p.Cfg().FlowFor(it.Kind)
	if err != nil {
		return false
	}
	st, ok := f.Step(step)
	return ok && st.Type == domain.StepAgent && st.Output == "" && !st.Review
}

// Review records a review with two verdicts.
func (s *Service) Review(ctx context.Context, project string, a Actor, r domain.Review) (*domain.Item, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeReview, p.Name); err != nil {
		return nil, err
	}
	if r.ItemID == "" {
		r.ItemID = a.ItemID
	}
	r.ItemID, err = domain.NormalizeItemID(r.ItemID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := checkItemAccess(a, r.ItemID); err != nil {
		return nil, err
	}
	if err := r.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	p.mu.Lock()
	cur, ok := p.state.Items[r.ItemID]
	if !ok {
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: item %s", ErrNotFound, r.ItemID)
	}
	if r.Step == "" {
		r.Step = cur.Step
	}
	if r.Step != cur.Step || cur.Status != domain.StatusInProgress {
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: %s is at step %s (%s), not %s; this review is late", ErrConflict, cur.ID, cur.Step, cur.Status, r.Step)
	}
	f, _ := p.Cfg().FlowFor(cur.Kind)
	var st domain.StepDef
	if f != nil {
		st, _ = f.Step(r.Step)
	}
	isReview := (st.Type == domain.StepAgent && st.Review) || (st.Type == domain.StepApproval && st.Approver != "owner")
	if !isReview {
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: step %s does not take a review", ErrInvalid, r.Step)
	}
	if a.Kind == ActorAgent && stepRole(st) != a.Role {
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: step %s is reviewed by role %s", ErrForbidden, r.Step, stepRole(st))
	}
	// The reviewer must not be the author's model family when the step says so.
	if d := st.DifferentFrom(); d != "" && a.Kind == ActorAgent && cur.Runs != nil {
		if prev, ok := cur.Runs[d]; ok && prev.Family != "" && prev.Family == a.Family && prev.Model == a.Model {
			p.mu.Unlock()
			return nil, fmt.Errorf("%w: step %s must be reviewed by a different model than %s", ErrForbidden, r.Step, d)
		}
	}
	areaOf := p.Cfg().AreaOfFile
	same, other := domain.SplitFindings(cur, r.Findings, areaOf)
	p.mu.Unlock()
	r.Reviewer = actorName(a)
	r.Family = a.Family
	r.At = s.d.Clock.Now().UTC()
	if _, err := s.emit(ctx, p, domain.EvReviewSubmitted, r.ItemID, r.Reviewer, domain.Payload{Review: &r}); err != nil {
		return nil, err
	}
	// Findings of other areas become proposals; same-area findings are the rework reason.
	for _, fnd := range other {
		_, _ = s.Propose(ctx, p.Name, System(), domain.Proposal{Title: truncate(fnd.Text, 80), Problem: fnd.Text, Evidence: fmt.Sprintf("review of %s at %s:%d", r.ItemID, fnd.File, fnd.Line),
			Impact: def(fnd.Severity, "minor"), Size: "unknown", Area: fnd.Area, GoalLink: "found while reviewing " + r.ItemID, Kind: "improvement", By: r.Reviewer})
	}
	outcome := r.Outcome()
	if outcome != domain.OutcomeApprove && len(same) == 0 {
		// Every finding was elsewhere: nothing to fix here.
		outcome = domain.OutcomeApprove
	}
	reason := r.Summary
	if len(same) > 0 {
		reason += "\n" + domain.FormatFindings(same)
	}
	it, tr, err := s.finishStep(ctx, p, stepOutcome{Item: r.ItemID, Step: r.Step, Outcome: outcome, Reason: reason, Actor: r.Reviewer, RunInfo: runInfo(a)})
	if err != nil {
		return nil, err
	}
	if p.Ports().CodeHost != nil && it.PR != "" {
		pr := prFromRef(it.PR)
		body := fmt.Sprintf("**%s · %s** — %s\n\nCriteria: %s · Code: %s\n\n%s\n%s", r.Step, r.Decision, r.Reviewer, r.Criteria, r.Code, r.Summary, domain.FormatFindings(r.Findings))
		go func() { _ = p.Ports().CodeHost.Review(context.Background(), pr, r.Decision == domain.ReviewApprove, body) }()
	}
	_ = tr
	return it, nil
}

// ReportCheck records a check result from CI, a webhook or another machine.
func (s *Service) ReportCheck(ctx context.Context, project string, a Actor, res domain.CheckResult) (*domain.Item, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeChecksReport, p.Name); err != nil {
		return nil, err
	}
	res.ItemID, err = domain.NormalizeItemID(res.ItemID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if res.ID == "" {
		res.ID = s.d.IDs.NewID()
	}
	res.At = s.d.Clock.Now().UTC()
	if _, err := s.emit(ctx, p, domain.EvCheckCompleted, res.ItemID, actorName(a), domain.Payload{Check: &res}); err != nil {
		return nil, err
	}
	return s.resolvePendingCheck(ctx, p, res)
}

// resolvePendingCheck finishes the current check step once every check of it reported.
func (s *Service) resolvePendingCheck(ctx context.Context, p *Project, res domain.CheckResult) (*domain.Item, error) {
	p.mu.Lock()
	it, ok := p.state.Items[res.ItemID]
	if !ok {
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: item %s", ErrNotFound, res.ItemID)
	}
	f, _ := p.Cfg().FlowFor(it.Kind)
	st, _ := f.Step(it.Step)
	p.mu.Unlock()
	if st.Type != domain.StepCheck {
		return it.Clone(), nil // late result: recorded, nothing to move
	}
	names := s.checkNames(p, st)
	if !containsStr(names, res.Check) {
		return it.Clone(), nil
	}
	results := s.checkResultsSince(ctx, p, it.ID, it.StepStartedAt)
	passed, reason, complete := aggregateChecks(names, results)
	if !complete {
		return it.Clone(), nil
	}
	outcome := domain.OutcomePass
	if !passed {
		outcome = domain.OutcomeFail
	}
	out, _, err := s.finishStep(ctx, p, stepOutcome{Item: it.ID, Step: st.Name, Outcome: outcome, Reason: reason, Actor: "checks"})
	return out, err
}

func containsStr(s []string, v string) bool { return domain.Contains(s, v) }

// checkNames are the checks a check step runs.
func (s *Service) checkNames(p *Project, st domain.StepDef) []string {
	name := domain.TrimChecks(st.Run)
	if name == "all" || name == "checks" {
		return sortedKeys(p.Cfg().Checks)
	}
	return []string{name}
}

func (s *Service) checkResultsSince(ctx context.Context, p *Project, itemID string, since interface{ IsZero() bool }) map[string]domain.CheckResult {
	out := map[string]domain.CheckResult{}
	evs, err := s.d.Store.Query(ctx, queryChecks(p.Name, itemID))
	if err != nil {
		return out
	}
	it := p.Snapshot().Items[itemID]
	for _, e := range evs {
		if it != nil && e.Time.Before(it.StepStartedAt) {
			continue
		}
		pl, err := e.DecodePayload()
		if err != nil || pl.Check == nil {
			continue
		}
		out[pl.Check.Check] = *pl.Check
	}
	return out
}

func aggregateChecks(names []string, results map[string]domain.CheckResult) (passed bool, reason string, complete bool) {
	passed = true
	var reasons []string
	for _, n := range names {
		r, ok := results[n]
		if !ok {
			return false, "", false
		}
		if !r.Passed {
			passed = false
			reasons = append(reasons, fmt.Sprintf("check %s failed: %s", n, r.Summary))
		}
	}
	return passed, strings.Join(reasons, "\n"), true
}
