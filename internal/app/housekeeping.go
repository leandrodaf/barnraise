package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// housekeeping: heartbeat on every machine; global duties only on the machine
// holding refs/barnraise/lease/housekeeping (another takes over if it dies).
func (r *Runtime) housekeeping(ctx context.Context, p *Project, m domain.Machine) {
	s := r.s
	// Worktrees and processes are local: told to the others on their own timer.
	s.reportLocal(p, r.busyItems(p.Name))
	now := time.Now()
	every := p.Cfg().Lease.Renew
	if every <= 0 || every > 30*time.Second {
		every = 30 * time.Second
	}
	if now.Sub(r.lastHK[p.Name]) < every {
		return
	}
	r.lastHK[p.Name] = now
	m.LastSeen = s.d.Clock.Now().UTC()
	m.Version = s.d.Version
	m.BuildTime = s.d.BuildTime
	r.mu.Lock()
	m.Busy = len(r.running)
	m.Standby = r.standby
	r.mu.Unlock()
	m.TrackerErr = s.TrackerErr(p.Name)
	m.Accounts = nil
	for _, a := range p.Snapshot().AccountList() {
		if a.UsableOn(s.d.Machine) {
			m.Accounts = append(m.Accounts, a.ID)
		}
	}
	s.reconcileLogins(ctx)
	s.emitEphemeral(p, domain.EvMachineUpdated, m.Name, "barnraise", domain.Payload{MachineI: &m, Note: "heartbeat"})
	// Worktrees are local: every machine trims its own, lease or not.
	if now.Sub(r.lastTrim[p.Name]) > time.Hour {
		r.lastTrim[p.Name] = now
		busy := map[string]bool{}
		r.mu.Lock()
		for _, ls := range r.running {
			if ls.project == p.Name {
				busy[ls.itemID] = true
			}
		}
		r.mu.Unlock()
		go func() {
			if t := s.trimWorkspaces(context.Background(), p, busy); t.total() > 0 {
				s.d.Log.Info("worktrees trimmed", "project", p.Name, "finished", t.Finished, "unknown", t.Unknown, "refine", t.Refine,
					"scratch", t.Scratch, "trash", t.Trash, "stripped", t.Stripped)
			}
		}()
	}
	if m.Standby {
		// A machine in standby takes no duty: the cluster's housekeeping
		// (items of dead machines, imports, digests) goes to one that works.
		s.releaseLease(ctx, p, HousekeepingKey)
		s.releaseLease(ctx, p, AutopilotKey)
		s.releaseLease(ctx, p, TrackerKey)
		return
	}
	// The autopilot is a duty of its own: a machine whose advisor fails
	// gives it up and another one takes it.
	if s.autopilotDue(p, now) {
		if s.holdDuty(ctx, p, AutopilotKey) {
			go s.runAutopilot(context.WithoutCancel(ctx), p)
		} else {
			s.autopilotState(p.Name).running.Store(false)
		}
	}
	// The tracker repair is a duty of its own: a machine that cannot write
	// to the tracker gives it up and another one takes it.
	r.repairTracker(ctx, p, now)
	if !s.holdHousekeeping(ctx, p) {
		return
	}
	s.takeOverDeadItems(ctx, p)
	if n := s.expireProposals(ctx, p); n > 0 {
		s.d.Log.Info("proposals expired", "project", p.Name, "count", n)
	}
	if now.Sub(r.lastImport[p.Name]) > 2*time.Minute && p.Ports().Tracker != nil {
		r.mu.Lock()
		since := r.importedTo[p.Name]
		r.mu.Unlock()
		if since.IsZero() {
			since = now.Add(-30 * 24 * time.Hour)
		}
		r.lastImport[p.Name] = now
		go func() {
			if _, err := s.ImportFromTracker(context.Background(), p.Name, since.Add(-time.Minute)); err != nil {
				s.d.Log.Warn("tracker import; the next one tries again from the same point", "project", p.Name, "since", since, "err", err)
				return
			}
			r.mu.Lock()
			r.importedTo[p.Name] = now
			r.mu.Unlock()
		}()
	}
	if now.Sub(r.lastSweep[p.Name]) > 24*time.Hour {
		r.lastSweep[p.Name] = now
		go func() {
			if _, err := s.Sweep(context.Background(), p.Name); err != nil {
				s.d.Log.Warn("nightly sweep", "err", err)
			}
			s.releaseFinishedClaims(context.Background(), p)
		}()
	}
	if last := r.lastDigest[p.Name]; now.Sub(last) > 24*time.Hour {
		if !last.IsZero() {
			go s.sendDigest(context.Background(), p)
		}
		r.lastDigest[p.Name] = now
	}
	if last := r.lastWeekly[p.Name]; now.Sub(last) > 7*24*time.Hour {
		if !last.IsZero() {
			go s.weekly(context.Background(), p)
		}
		r.lastWeekly[p.Name] = now
	}
	s.finishGoalIfDone(ctx, p)
}

// holdHousekeeping claims or renews the housekeeping lease.
func (s *Service) holdHousekeeping(ctx context.Context, p *Project) bool {
	return s.holdDuty(ctx, p, HousekeepingKey)
}

// holdDuty claims, renews or takes over a project-wide duty held by one
// machine at a time (a git ref with a deadline).
func (s *Service) holdDuty(ctx context.Context, p *Project, key string) bool {
	a := p.Ports().Arbiter
	if a == nil {
		return true
	}
	p.mu.Lock()
	l, ok := p.leases[key]
	p.mu.Unlock()
	if ok {
		nl, err := a.Renew(ctx, l)
		if err == nil {
			p.mu.Lock()
			p.leases[key] = nl
			p.mu.Unlock()
			return true
		}
		if !errors.Is(err, ports.ErrLost) && !errors.Is(err, ports.ErrNotHeld) {
			return true // offline: keep the duty until we know better
		}
		p.mu.Lock()
		delete(p.leases, key)
		p.mu.Unlock()
	}
	nl, err := a.Claim(ctx, key, s.d.Machine)
	if errors.Is(err, ports.ErrTaken) {
		ttl := p.Cfg().Lease.TTL
		info, ierr := a.Inspect(ctx, key)
		if ierr == nil && info.Holder == s.d.Machine {
			// Ours from before a restart: adopt it.
			nl, err = a.Renew(ctx, ports.Lease{Key: key, Holder: s.d.Machine, Version: info.Version})
		} else {
			nl, err = a.TakeOver(ctx, key, s.d.Machine, ttl)
		}
	}
	if err != nil {
		return false
	}
	p.mu.Lock()
	p.leases[key] = nl
	p.mu.Unlock()
	return true
}

// takeOverDeadItems takes items whose machine stopped renewing their claim.
// The item migrates, not the session: the new machine continues from the
// last step pushed to the item's branch.
func (s *Service) takeOverDeadItems(ctx context.Context, p *Project) {
	a := p.Ports().Arbiter
	if a == nil {
		return
	}
	ttl := p.Cfg().Lease.TTL
	snap := p.Snapshot()
	byMachine := map[string][]string{}
	for _, it := range snap.ItemList() {
		if it.Claim == nil || it.Claim.Machine == s.d.Machine || it.Status.Terminal() {
			continue
		}
		info, err := a.Inspect(ctx, claimKey(it.ID))
		if err != nil {
			continue
		}
		var l ports.Lease
		if !info.Held {
			l, err = a.Claim(ctx, claimKey(it.ID), s.d.Machine)
		} else {
			l, err = a.TakeOver(ctx, claimKey(it.ID), s.d.Machine, ttl)
		}
		if err != nil {
			continue
		}
		prev := it.Claim.Machine
		_, err = s.mutateItem(ctx, p, it.ID, domain.EvItemClaimed, "barnraise", func(x *domain.Item) error {
			x.Claim = &domain.Claim{Machine: s.d.Machine, Since: s.d.Clock.Now().UTC()}
			if x.Status == domain.StatusPaused {
				x.Status = domain.StatusInProgress
			}
			return nil
		}, domain.Payload{Note: "taken over from machine " + prev + " (no renewal for " + ttl.String() + ")"})
		if err != nil {
			s.releaseLeaseValue(ctx, p, l)
			continue
		}
		p.mu.Lock()
		p.leases[it.ID] = l
		p.mu.Unlock()
		byMachine[prev] = append(byMachine[prev], fmt.Sprintf("%s %s", it.ID, it.Title))
	}
	for m, items := range byMachine {
		msg := fmt.Sprintf("Machine %s has been out for more than %s. %s took its items: %s. Nothing lost.", m, ttl, s.d.Machine, strings.Join(items, ", "))
		s.d.Log.Info(msg)
		go s.digestNote(p, msg)
	}
	if len(byMachine) > 0 {
		s.runtime.Poke()
	}
}

func (s *Service) digestNote(p *Project, msg string) {
	if s.d.Notifier == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = s.d.Notifier.Notify(ctx, ports.Notification{Title: "[" + p.Name + "] machines", Body: msg, Kind: "digest"})
}

// finishGoalIfDone closes the goal when its inventory is done; meanwhile
// only bugs and approved improvements run.
func (s *Service) finishGoalIfDone(ctx context.Context, p *Project) {
	snap := p.Snapshot()
	g := snap.ActiveGoal()
	if g == nil {
		return
	}
	prog := domain.ComputeProgress(snap.ItemList(), g.ID, s.d.Clock.Now(), 7*24*time.Hour)
	if prog.Total == 0 || prog.Done < prog.Total {
		return
	}
	gg := *g
	gg.Active = false
	gg.FinishedAt = s.d.Clock.Now().UTC()
	if _, err := s.emit(ctx, p, domain.EvGoalFinished, g.ID, "barnraise", domain.Payload{Goal: &gg}); err != nil {
		return
	}
	p.mu.Lock()
	q := &domain.Question{Context: ot(p, "goal.context", g.ID, g.Name, prog.Done, prog.Total),
		Decision: ot(p, "goal.decision"), Options: []domain.Option{ownerOpt(p, "new_goal", true), ownerOpt(p, "bugs_only", false)},
		IfNoAnswer: ot(p, "if.goal")}
	_, _ = s.askLocked(ctx, p, q, "barnraise")
	p.mu.Unlock()
}

// Digest is the daily summary for the owner.
type Digest struct {
	Project     string                 `json:"project"`
	Integrated  []string               `json:"integrated"`
	Goal        string                 `json:"goal"`
	Proposals   []domain.ProposalGroup `json:"proposals"`
	Accounts    []string               `json:"accounts"`
	Waiting     []string               `json:"waiting"`
	Alerts      []string               `json:"alerts"`
	GeneratedAt time.Time              `json:"generated_at"`
	// Savings is what the output filter kept out of the agents' context in the week.
	Savings string `json:"savings,omitempty"`
}

// BuildDigest summarizes the last day.
func (s *Service) BuildDigest(ctx context.Context, project string) (Digest, error) {
	p, err := s.Project(project)
	if err != nil {
		return Digest{}, err
	}
	snap := p.Snapshot()
	now := s.d.Clock.Now()
	d := Digest{Project: p.Name, GeneratedAt: now.UTC()}
	for _, it := range snap.ItemList() {
		if it.Status == domain.StatusIntegrated && now.Sub(it.IntegratedAt) < 24*time.Hour {
			d.Integrated = append(d.Integrated, it.ID+" "+it.Title)
		}
		if it.Status == domain.StatusWaitingOwner {
			d.Waiting = append(d.Waiting, it.ID+" "+it.Title)
		}
	}
	if g := snap.ActiveGoal(); g != nil {
		pr := domain.ComputeProgress(snap.ItemList(), g.ID, now, 7*24*time.Hour)
		d.Goal = fmt.Sprintf("%s: %d%% (%d of %d), pace %.1f/day", g.Name, pr.Percentage, pr.Done, pr.Total, pr.PerDay)
		if !pr.Forecast.IsZero() {
			d.Goal += ", forecast " + pr.Forecast.Format("2006-01-02")
		}
	}
	d.Proposals = domain.GroupProposals(snap.ProposalList())
	for _, a := range snap.AccountList() {
		line := fmt.Sprintf("%s: 5h %.0f%%, week %.0f%%", a.ID, a.Usage.FiveHourPct, a.Usage.WeeklyPct)
		if !a.Usage.WeeklyResets.IsZero() {
			line += ", resets " + a.Usage.WeeklyResets.Format("Mon 15:04")
		}
		d.Accounts = append(d.Accounts, line)
	}
	m, err := s.Metrics(ctx, p.Name, Owner(""), 7*24*time.Hour)
	if err == nil {
		d.Alerts = m.Alerts
		d.Savings = m.filterSavingsLine()
	}
	return d, nil
}

// Text renders a digest for a notification.
func (d Digest) Text() string {
	var b strings.Builder
	if d.Goal != "" {
		b.WriteString("Goal: " + d.Goal + "\n")
	}
	fmt.Fprintf(&b, "Integrated (%d): %s\n", len(d.Integrated), strings.Join(d.Integrated, "; "))
	if len(d.Waiting) > 0 {
		b.WriteString("Waiting for you: " + strings.Join(d.Waiting, "; ") + "\n")
	}
	for _, g := range d.Proposals {
		var titles []string
		for _, p := range g.Proposals {
			titles = append(titles, p.ID+" "+p.Title)
		}
		fmt.Fprintf(&b, "Proposals in %s (%d): %s\n", g.Area, len(g.Proposals), strings.Join(titles, "; "))
	}
	if len(d.Accounts) > 0 {
		b.WriteString("Accounts: " + strings.Join(d.Accounts, "; ") + "\n")
	}
	for _, a := range d.Alerts {
		b.WriteString("Alert: " + a + "\n")
	}
	if d.Savings != "" {
		b.WriteString("Output filter: " + d.Savings + "\n")
	}
	return b.String()
}

// weekly sends the proposed features (approve, reject or later in one tap)
// and turns the metrics that got worse into self-improvement proposals.
func (s *Service) weekly(ctx context.Context, p *Project) {
	m, err := s.Metrics(ctx, p.Name, System(), 7*24*time.Hour)
	if err == nil {
		for _, a := range m.Alerts {
			_, _ = s.Propose(ctx, p.Name, System(), domain.Proposal{Title: "Metric: " + truncate(a, 70), Problem: a, Evidence: "barnraise metrics --window 7d",
				Impact: "the goal moves slower or costs more", Size: "2h", Area: "barnraise", GoalLink: "measured self-improvement", Kind: "self_improvement", By: "barnraise"})
		}
	}
	if s.d.Notifier == nil {
		return
	}
	var lines []string
	var opts []string
	for _, pr := range p.Snapshot().ProposalList() {
		if (pr.Kind == "feature" || pr.Kind == "self_improvement") && (pr.Status == domain.ProposalOpen || pr.Status == domain.ProposalLater) {
			lines = append(lines, fmt.Sprintf("%s %s — %s (barnraise proposals approve|reject|later %s)", pr.ID, pr.Title, pr.Impact, pr.ID))
			opts = append(opts, pr.ID)
		}
	}
	if len(lines) == 0 {
		return
	}
	_ = s.d.Notifier.Notify(ctx, ports.Notification{Title: "[" + p.Name + "] proposed features this week", Body: strings.Join(lines, "\n"), Options: opts, Kind: "digest"})
}

func (s *Service) sendDigest(ctx context.Context, p *Project) {
	if s.d.Notifier == nil {
		return
	}
	d, err := s.BuildDigest(ctx, p.Name)
	if err != nil {
		return
	}
	_ = s.d.Notifier.Notify(ctx, ports.Notification{Title: "[" + p.Name + "] daily summary", Body: d.Text(), Kind: "digest"})
}

// UpsertAccount adds or changes an account (barnraise account add).
func (s *Service) UpsertAccount(ctx context.Context, project string, a Actor, acc domain.Account) (*domain.Account, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeAccounts, p.Name); err != nil {
		return nil, err
	}
	if acc.ID == "" || acc.Provider == "" {
		return nil, fmt.Errorf("%w: an account needs an id and a provider", ErrInvalid)
	}
	if _, _, _, ok := s.providerFor(p.Cfg(), acc.Provider); !ok {
		known := sortedKeys(s.d.Providers)
		if _, ok := s.d.Providers[EndpointCLI]; ok {
			known = append(known, sortedKeys(p.Cfg().APIProviders)...)
		}
		if s.d.ACP != nil {
			known = append(known, sortedKeys(p.Cfg().ACPProviders)...)
		}
		return nil, fmt.Errorf("%w: provider %q is not available (known: %s)", ErrInvalid, acc.Provider, strings.Join(known, ", "))
	}
	if acc.Status == "" {
		acc.Status = domain.AccountOK
	}
	if acc.AddedAt.IsZero() {
		acc.AddedAt = s.d.Clock.Now().UTC()
	}
	if _, err := s.emit(ctx, p, domain.EvAccountUpdated, acc.ID, actorName(a), domain.Payload{Account: &acc}); err != nil {
		return nil, err
	}
	s.runtime.Poke()
	return &acc, nil
}

// ReportUsage records the real limits the status line reports.
func (s *Service) ReportUsage(ctx context.Context, project string, a Actor, accountID string, u domain.Usage) error {
	p, err := s.Project(project)
	if err != nil {
		return err
	}
	if !a.Can(ScopeUsage, p.Name) && !(a.Kind == ActorAgent && a.Account == accountID) {
		return fmt.Errorf("%w: cannot report usage of %s", ErrForbidden, accountID)
	}
	p.mu.Lock()
	cur, ok := p.state.Accounts[accountID]
	if !ok {
		p.mu.Unlock()
		return fmt.Errorf("%w: account %s", ErrNotFound, accountID)
	}
	acc := *cur
	if u.UpdatedAt.IsZero() {
		u.UpdatedAt = s.d.Clock.Now().UTC()
	}
	acc.Usage = u
	if acc.Status == domain.AccountLimited && acc.Available(s.d.Clock.Now()) {
		acc.Status = domain.AccountOK
	}
	typ := domain.EvAccountUpdated
	if !acc.Available(s.d.Clock.Now()) {
		typ = domain.EvAccountLimitReached
	}
	_, err = s.emitLocked(ctx, p, typ, accountID, actorName(a), domain.Payload{Account: &acc})
	p.mu.Unlock()
	return err
}

func (s *Service) markAccountLimited(ctx context.Context, p *Project, id string, resets time.Time) {
	p.mu.Lock()
	cur, ok := p.state.Accounts[id]
	if !ok {
		p.mu.Unlock()
		return
	}
	acc := *cur
	acc.Usage.FiveHourPct = 100
	if resets.IsZero() {
		resets = s.d.Clock.Now().Add(time.Hour)
	}
	acc.Usage.FiveHourResets = resets
	acc.Usage.UpdatedAt = s.d.Clock.Now().UTC()
	acc.Status = domain.AccountLimited
	_, _ = s.emitLocked(ctx, p, domain.EvAccountLimitReached, id, "barnraise", domain.Payload{Account: &acc})
	p.mu.Unlock()
}

func (s *Service) notifyAccountsExhausted(ctx context.Context, p *Project) {
	snap := p.Snapshot()
	now := s.d.Clock.Now()
	for _, a := range snap.AccountList() {
		if a.Available(now) {
			return
		}
	}
	if len(snap.Accounts) == 0 {
		return
	}
	next := time.Time{}
	for _, a := range snap.AccountList() {
		for _, t := range []time.Time{a.Usage.FiveHourResets, a.Usage.WeeklyResets} {
			if !t.IsZero() && t.After(now) && (next.IsZero() || t.Before(next)) {
				next = t
			}
		}
	}
	msg := "Every account is out of limits."
	if !next.IsZero() {
		msg += " Next reset: " + next.Format("Mon 15:04") + "."
	}
	s.notifyOwner(ctx, p, "accounts_exhausted", "All accounts are out of limits", msg)
}

// MachineLeave returns this machine's items to the queue before leaving.
func (s *Service) MachineLeave(ctx context.Context, project string, a Actor) ([]string, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeMachines, p.Name); err != nil {
		return nil, err
	}
	s.runtime.StopProject(p.Name)
	var returned []string
	for _, it := range p.Snapshot().ItemList() {
		if it.Claim == nil || it.Claim.Machine != s.d.Machine || it.Status.Terminal() {
			continue
		}
		_, _ = s.commitStep(ctx, p, it.ID, it.ID+": machine leaving, work saved")
		_, err := s.mutateItem(ctx, p, it.ID, domain.EvItemReleased, actorName(a), func(x *domain.Item) error {
			x.Claim = nil
			if x.Status == domain.StatusInProgress || x.Status == domain.StatusQueued {
				x.Status = domain.StatusReady
			}
			return nil
		}, domain.Payload{Note: "machine " + s.d.Machine + " left; the item continues from its last step"})
		if err == nil {
			s.releaseLease(ctx, p, it.ID)
			returned = append(returned, it.ID)
		}
	}
	p.mu.Lock()
	hk, ok := p.leases[HousekeepingKey]
	delete(p.leases, HousekeepingKey)
	p.mu.Unlock()
	if ok {
		s.releaseLeaseValue(ctx, p, hk)
	}
	m := domain.Machine{Name: s.d.Machine, Left: true, LastSeen: s.d.Clock.Now().UTC()}
	_, _ = s.emit(ctx, p, domain.EvMachineLeft, s.d.Machine, actorName(a), domain.Payload{MachineI: &m})
	sort.Strings(returned)
	return returned, nil
}

// releaseFinishedClaims removes the claims this machine still holds on
// items that are finished. A release can fail when the network drops at the
// wrong moment; the claim is harmless (nobody takes a finished item) but it
// should not stay in the repository for ever.
func (s *Service) releaseFinishedClaims(ctx context.Context, p *Project) {
	arb := p.Ports().Arbiter
	if arb == nil {
		return
	}
	keys, err := arb.List(ctx, "claims/")
	if err != nil {
		return
	}
	snap := p.Snapshot()
	for _, key := range keys {
		id := strings.TrimPrefix(key, "claims/")
		it := snap.Items[id]
		if it == nil || !it.Status.Terminal() {
			continue
		}
		info, err := arb.Inspect(ctx, key)
		if err != nil || !info.Held || info.Holder != s.d.Machine {
			continue
		}
		if err := arb.Release(ctx, ports.Lease{Key: key, Holder: info.Holder, Version: info.Version, Renewed: info.Renewed}); err != nil {
			s.d.Log.Debug("release a finished item's claim", "item", id, "err", err)
		}
	}
}
