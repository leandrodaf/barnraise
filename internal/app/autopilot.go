package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// The autopilot takes the owner's decisions: the questions agents ask, the
// plans and deliveries waiting for approval, and the proposals. The advisor
// (the project's strongest model, reading only) weighs each one and its
// answer is applied with its reasons. What only the owner can do (an
// account, money, a device, an access, a personal choice) is left to the
// owner, with what to do, and not asked again until the decision changes.
// Only the machine that holds the project's housekeeping runs it, so a
// decision is never taken twice; a paused project, or a machine in standby,
// takes none.

// autopilotEvery is the least time between the starts of two runs.
var autopilotEvery = 2 * time.Minute

// advisorActor is who decides on autopilot, as the log and the owner see it.
var advisorActor = Actor{Kind: ActorSystem, Name: "advisor", Scopes: []string{ScopeAll}}

type autopilotState struct {
	running atomic.Bool
	last    atomic.Int64 // unix nanoseconds of the last start
	// failed holds, per decision, when the advisor last failed on it and
	// how many times in a row: the decision waits before the next try, and
	// the others go on.
	mu     sync.Mutex
	failed map[string]autopilotFailure
	// inARow counts the advisor's failures on this machine, whatever the
	// decision; benched is when this machine may take the duty again.
	inARow  atomic.Int32
	benched atomic.Int64
}

// autopilotHandOver is how many failures in a row make a machine give the
// autopilot to another one, and how long it then stays out.
const (
	autopilotHandOver = 3
	autopilotBench    = 30 * time.Minute
)

type autopilotFailure struct {
	at    time.Time
	times int
}

// autopilotRetry is how long a decision waits after the advisor failed on
// it n times in a row: 5, 10, 20 minutes… at most two hours.
func autopilotRetry(n int) time.Duration {
	d := 5 * time.Minute
	for i := 1; i < n && d < 2*time.Hour; i++ {
		d *= 2
	}
	return min(d, 2*time.Hour)
}

// waiting reports whether a decision still waits after a failure.
func (st *autopilotState) waiting(thread string, now time.Time) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	f, ok := st.failed[thread]
	return ok && now.Sub(f.at) < autopilotRetry(f.times)
}

// fail records a failure of the advisor on a decision; it returns how many
// in a row there were.
func (st *autopilotState) fail(thread string, now time.Time) int {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.failed == nil {
		st.failed = map[string]autopilotFailure{}
	}
	f := st.failed[thread]
	f.at, f.times = now, f.times+1
	st.failed[thread] = f
	return f.times
}

func (st *autopilotState) ok(thread string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.failed, thread)
}

func (s *Service) autopilotState(project string) *autopilotState {
	v, _ := s.autopilot.LoadOrStore(project, &autopilotState{})
	return v.(*autopilotState)
}

// SetAutopilot switches a project's autopilot.
func (s *Service) SetAutopilot(ctx context.Context, project string, a Actor, on bool) error {
	p, err := s.Project(project)
	if err != nil {
		return err
	}
	if err := a.Require(ScopeApprove, p.Name); err != nil {
		return err
	}
	if p.Snapshot().Autopilot == on {
		return nil
	}
	note := "off"
	if on {
		note = "on"
		if p.Ports().Advisor == nil {
			return fmt.Errorf("%w: %s has no advisor (no Claude account with capacity), so nobody could take the decisions", ErrInvalid, p.Name)
		}
	}
	if _, err := s.emit(ctx, p, domain.EvAutopilot, "", actorName(a), domain.Payload{Note: note}); err != nil {
		return err
	}
	if on {
		s.autopilotState(p.Name).last.Store(0) // what waits is weighed at the next housekeeping
	}
	return nil
}

// AutopilotProjects are the projects whose autopilot is on.
func (s *Service) AutopilotProjects() []string {
	var out []string
	for _, name := range s.Projects() {
		if p, err := s.Project(name); err == nil && p.Snapshot().Autopilot {
			out = append(out, name)
		}
	}
	return out
}

// autopilotDecision is one decision the autopilot may take.
type autopilotDecision struct {
	thread string    // Q-7, or the item for an approval
	since  time.Time // when the decision was asked: a later "left to you" stands
	awaits string    // plan or delivery, for an approval
}

// autopilotWork is what waits for the owner now and was not left to them.
func (s *Service) autopilotWork(ctx context.Context, p *Project) (decisions []autopilotDecision, proposals bool) {
	snap := p.Snapshot()
	left := func(thread string, since time.Time) bool {
		for _, m := range snap.Discussions[thread] {
			if m.LeftToOwner && !m.At.Before(since) {
				return true
			}
		}
		return false
	}
	for _, q := range snap.Questions {
		if q.Open() && !left(q.ID, q.AskedAt) {
			decisions = append(decisions, autopilotDecision{thread: q.ID, since: q.AskedAt})
		}
	}
	if o, err := s.Overview(ctx, System(), OverviewQuery{Project: p.Name, Brief: true, Limit: overviewLimitMax}); err == nil {
		for _, po := range o.Projects {
			for _, io := range po.Items {
				if io.Awaits == "" {
					continue
				}
				it := snap.Items[io.ID]
				if it == nil || left(io.ID, it.StepStartedAt) {
					continue
				}
				decisions = append(decisions, autopilotDecision{thread: io.ID, since: it.StepStartedAt, awaits: io.Awaits})
			}
		}
	}
	sort.Slice(decisions, func(i, j int) bool { return decisions[i].since.Before(decisions[j].since) })
	for _, pr := range snap.Proposals {
		if pr.Status == domain.ProposalOpen {
			proposals = true
			break
		}
	}
	return decisions, proposals
}

// autopilotDue reports whether the autopilot should run for the project now,
// and marks the start when it should.
func (s *Service) autopilotDue(p *Project, now time.Time) bool {
	snap := p.Snapshot()
	if !snap.Autopilot || snap.Paused || p.Ports().Advisor == nil {
		return false
	}
	st := s.autopilotState(p.Name)
	if now.UnixNano() < st.benched.Load() {
		return false
	}
	if now.Sub(time.Unix(0, st.last.Load())) < autopilotEvery || !st.running.CompareAndSwap(false, true) {
		return false
	}
	st.last.Store(now.UnixNano())
	return true
}

// autopilotOn reports whether the autopilot may still decide.
func autopilotOn(p *Project) bool {
	snap := p.Snapshot()
	return snap.Autopilot && !snap.Paused
}

// runAutopilot takes every decision that waits, then weighs the open
// proposals. It returns how many decisions were applied.
func (s *Service) runAutopilot(ctx context.Context, p *Project) int {
	st := s.autopilotState(p.Name)
	defer st.running.Store(false)
	decisions, proposals := s.autopilotWork(ctx, p)
	applied := 0
	for _, d := range decisions {
		if !autopilotOn(p) {
			return applied
		}
		now := s.d.Clock.Now()
		if st.waiting(d.thread, now) {
			continue
		}
		ok, err := s.autopilotDecide(ctx, p, d)
		if errors.Is(err, errAdvisorFailed) {
			// Said once in the conversation, then tried again later.
			if n := st.fail(d.thread, now); n == 1 {
				s.noteAdvisorFailure(ctx, p, d.thread, err)
			}
			s.d.Log.Warn("autopilot: the advisor did not answer", "project", p.Name, "thread", d.thread, "err", err)
			if st.inARow.Add(1) >= autopilotHandOver {
				// This machine's advisor does not work: another machine
				// takes the duty; this one stays out for a while.
				st.inARow.Store(0)
				st.benched.Store(now.Add(autopilotBench).UnixNano())
				s.releaseLease(ctx, p, AutopilotKey)
				s.d.Log.Warn("autopilot: handed to another machine", "project", p.Name, "for", autopilotBench)
				return applied
			}
			continue
		}
		st.inARow.Store(0)
		st.ok(d.thread)
		if err != nil {
			s.d.Log.Warn("autopilot: a decision was not taken", "project", p.Name, "thread", d.thread, "err", err)
		}
		if ok {
			applied++
		}
	}
	if proposals && autopilotOn(p) {
		verdicts, err := s.triage(ctx, p, func(pr *domain.Proposal) bool { return pr.Status == domain.ProposalOpen })
		if err != nil {
			s.d.Log.Warn("autopilot: the advisor gave no verdict on the proposals", "project", p.Name, "err", err)
		}
		for _, v := range verdicts {
			if !autopilotOn(p) {
				break
			}
			if _, err := s.DecideProposal(ctx, p.Name, advisorActor, v.ID, v.Decision, ot(p, "autopilot.reason", v.Reason)); err != nil {
				s.d.Log.Warn("autopilot: a proposal was not decided", "project", p.Name, "proposal", v.ID, "err", err)
				continue
			}
			applied++
		}
	}
	if applied > 0 {
		s.d.Log.Info("autopilot decided", "project", p.Name, "count", applied)
	}
	return applied
}

// autopilotDecide talks one decision over with the advisor and applies the
// answer it suggests, or leaves it to the owner with what to do.
func (s *Service) autopilotDecide(ctx context.Context, p *Project, d autopilotDecision) (bool, error) {
	ask := ot(p, "autopilot.ask")
	if d.awaits != "" {
		ask += ot(p, "autopilot.approve", d.awaits)
	}
	conv, err := s.Discuss(ctx, p.Name, advisorActor, d.thread, ask)
	if err != nil {
		return false, fmt.Errorf("%w: %v", errAdvisorFailed, err)
	}
	last := conv[len(conv)-1]
	option, note := strings.TrimSpace(last.SuggestOption), strings.TrimSpace(last.SuggestNote)
	if option == "" || strings.EqualFold(option, "owner") {
		why := note
		if why == "" {
			why = last.Text
		}
		return false, s.leaveToOwner(ctx, p, last, why)
	}
	reason := ot(p, "autopilot.reason", note)
	switch {
	case strings.HasPrefix(d.thread, "Q-"):
		_, err = s.Answer(ctx, p.Name, advisorActor, d.thread, option, reason)
	case d.awaits != "":
		approve := strings.HasPrefix(strings.ToLower(option), "approve")
		err = s.Approve(ctx, p.Name, advisorActor, d.thread, d.awaits, approve, reason)
	default:
		return false, fmt.Errorf("%w: %s is not a decision the autopilot takes", ErrInvalid, d.thread)
	}
	if err != nil {
		// An answer that does not fit (an option that is not there, a step
		// that moved) is the owner's to give.
		return false, s.leaveToOwner(ctx, p, last, err.Error())
	}
	done := *last
	done.Delegated, done.LeftToOwner, done.Text, done.At = true, false, ot(p, "autopilot.done", option), s.d.Clock.Now().UTC()
	return true, s.emitDiscussion(ctx, p, &done, "advisor")
}

// errAdvisorFailed: the advisor gave no answer on a decision.
var errAdvisorFailed = errors.New("the advisor did not answer")

// noteAdvisorFailure says once, in the decision's conversation, that the
// advisor failed on this machine and why, so the owner and whoever fixes it
// can read it.
func (s *Service) noteAdvisorFailure(ctx context.Context, p *Project, thread string, err error) {
	m := &domain.DiscussionMessage{Thread: thread, Role: domain.DiscussAdvisor, By: "advisor",
		Text: ot(p, "autopilot.failed", s.d.Machine, err.Error()), At: s.d.Clock.Now().UTC()}
	if e := s.emitDiscussion(ctx, p, m, "advisor"); e != nil {
		s.d.Log.Warn("autopilot: the failure was not recorded", "project", p.Name, "thread", thread, "err", e)
	}
}

// leaveToOwner records, in the decision's conversation, that the autopilot
// left it to the owner and why.
func (s *Service) leaveToOwner(ctx context.Context, p *Project, last *domain.DiscussionMessage, why string) error {
	m := *last
	m.Delegated, m.LeftToOwner, m.SuggestOption, m.SuggestNote = false, true, "", ""
	m.Text, m.At = ot(p, "autopilot.left", strings.TrimSpace(why)), s.d.Clock.Now().UTC()
	return s.emitDiscussion(ctx, p, &m, "advisor")
}
