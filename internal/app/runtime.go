package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Runtime opens, watches and closes agent sessions through provider
// adapters. Stuck agents are detected by events, never by reading panes.
type Runtime struct {
	s       *Service
	poke    chan struct{}
	mu      sync.Mutex
	running map[string]*liveSession // by agent name
	probe   func() domain.Machine
	stopped bool
	// standby: the owner's switch for this machine. While it is on, the
	// runtime starts nothing here (no agent, no driver step, no merge), in
	// any project; standbyWhy is the owner's note.
	standby    bool
	standbyWhy string
	// SessionTimeout bounds one step session.
	SessionTimeout time.Duration
	// MaxSessions caps concurrent sessions on this machine (0 = machine slots).
	MaxSessions int
	// draining holds new sessions while a newer binary waits for the running ones.
	draining  atomic.Bool
	lastHK    map[string]time.Time
	lastRenew map[string]time.Time
	// queues counts the merge-queue passes started by Tick, which fetch and
	// push: StopAll waits for them as it waits for sessions.
	queues     sync.WaitGroup
	lastImport map[string]time.Time
	lastMirror map[string]time.Time
	// importedTo is where the last complete tracker import started: the next
	// one lists what changed since then. A failed import leaves it in place.
	importedTo map[string]time.Time
	lastSweep  map[string]time.Time
	lastTrim   map[string]time.Time
	lastDigest map[string]time.Time
	lastWeekly map[string]time.Time
	// startErrs remembers the last start failure of each agent, so a
	// failure that repeats every pass (accounts out of limits, a missing
	// binary) is logged once, not every few seconds.
	startErrs map[string]string
	// startErrSaid is when each start failure was last told to the
	// cluster: the agent's failed state is live only, so a machine that
	// restarts (or joins) would never learn it from a failure logged once.
	startErrSaid map[string]time.Time
	gpu          *[]string // engine arguments for the machine's GPU, probed once
	// Sandbox turns on the provider's native sandbox in session settings.
	Sandbox bool
	// EgressProxy routes agent traffic through the allow-list proxy (host:port).
	EgressProxy string
	// KeyBroker, with the egress proxy, seals provider keys (see ports.KeyBroker).
	KeyBroker ports.KeyBroker
	// MinFreeMemGB holds new sessions while the machine has less memory
	// available than this (default 3 GB). Sessions already running are never
	// stopped for it: builds, tests and emulators of the items in progress
	// get the memory first, instead of being killed by the kernel.
	MinFreeMemGB float64
	// NoSandboxProviders lists providers that cannot run a command on this
	// machine because their mandatory sandbox does not work here (Codex on
	// Linux needs bubblewrap). They get no work instead of failing every command.
	NoSandboxProviders map[string]string // provider -> why, and what to do
}

// ReadOnlyOnHost lists providers whose sessions cannot run commands or
// change files outside a container (they have no hook Barnraise can use as
// a barrier, so the adapter turns those tools off). They only get steps of
// roles that review: an implementer step there would end without code.
var ReadOnlyOnHost = map[string]bool{"gemini": true}

// codexNeedsBwrap explains why Codex gets no work on a machine where bubblewrap cannot start.
const codexNeedsBwrap = "Codex runs every command inside bubblewrap, which cannot create user namespaces on this machine. " +
	"On Ubuntu: allow it with an AppArmor profile for bwrap, or `sudo sysctl kernel.apparmor_restrict_unprivileged_userns=0`. " +
	"Until then this machine gives Codex no work"

type liveSession struct {
	agent   string
	project string
	itemID  string
	step    string
	dir     string // the worktree the session works in
	sess    ports.Session
	cancel  context.CancelFunc
	started time.Time
	// run marks the outputs this session's filter kept, in outputs.
	run, outputs string
	// role, provider and model say who the session is, for its watchers.
	role, provider, model string
	// feed is the session's live lines (live.go).
	feed *liveFeed
	// halted is set when Barnraise stops the session (pause, takeover, close,
	// shutdown): its end is not the agent's fault.
	halted atomic.Bool
}

// stop ends a session on Barnraise's behalf.
func (ls *liveSession) stop() {
	ls.halted.Store(true)
	_ = ls.sess.Stop()
}

// isStopped reports whether the runtime is shutting down.
// Standby reports whether this machine is in standby, and why.
func (r *Runtime) Standby() (bool, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.standby, r.standbyWhy
}

// SetStandby switches the machine's standby. Turning it on stops the
// sessions running here, in every project, as a pause does: their steps are
// saved and the items continue from their last step later. Turning it off
// wakes the loop. It returns how many sessions were stopped.
func (r *Runtime) SetStandby(on bool, why string) int {
	r.mu.Lock()
	changed := r.standby != on
	r.standby, r.standbyWhy = on, why
	if !on {
		r.standbyWhy = ""
	}
	var list []*liveSession
	if on {
		for _, ls := range r.running {
			list = append(list, ls)
		}
	}
	r.mu.Unlock()
	for _, ls := range list {
		ls.stop()
	}
	if changed {
		r.s.d.Log.Info("standby", "on", on, "why", why, "sessions_stopped", len(list))
	}
	if !on {
		r.Poke()
	}
	return len(list)
}

// SetDraining stops new sessions from starting on this machine while the
// running ones end (a newer binary waits for them). Nothing is stopped.
func (r *Runtime) SetDraining(on bool) {
	if r.draining.Swap(on) != on {
		r.s.d.Log.Info("draining", "on", on)
	}
	if !on {
		r.Poke()
	}
}

// Draining reports whether new sessions wait for the running ones to end.
func (r *Runtime) Draining() bool { return r.draining.Load() }

func (r *Runtime) isStopped() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopped
}

type sessionRegistry struct{}

func newSessionRegistry() *sessionRegistry { return &sessionRegistry{} }

// sandboxWorks checks that bubblewrap can really start here (AppArmor or
// containers may forbid user namespaces even when the binary exists).
func sandboxWorks() bool {
	if _, err := exec.LookPath("bwrap"); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "bwrap", "--ro-bind", "/", "/", "--dev", "/dev", "--unshare-net", "true").Run() == nil
}

func newRuntime(s *Service) *Runtime {
	bwrapErr := error(nil)
	if !sandboxWorks() {
		bwrapErr = errors.New("sandbox unavailable")
	}
	r := &Runtime{s: s, poke: make(chan struct{}, 1), running: map[string]*liveSession{}, SessionTimeout: 3 * time.Hour,
		lastHK: map[string]time.Time{}, lastRenew: map[string]time.Time{}, lastImport: map[string]time.Time{}, lastMirror: map[string]time.Time{}, importedTo: map[string]time.Time{}, lastSweep: map[string]time.Time{}, lastTrim: map[string]time.Time{},
		lastDigest: map[string]time.Time{}, lastWeekly: map[string]time.Time{}, Sandbox: bwrapErr == nil, NoSandboxProviders: map[string]string{}}
	// Where Landlock works, Codex runs confined by it instead of bubblewrap.
	if bwrapErr != nil && goruntime.GOOS == "linux" && !s.landlockReady() {
		r.NoSandboxProviders["codex"] = codexNeedsBwrap
	}
	return r
}

// SetProbe installs the machine capability probe.
func (r *Runtime) SetProbe(f func() domain.Machine) { r.probe = f }

// Poke wakes the loop (new item, delivery, focus change…).
func (r *Runtime) Poke() {
	select {
	case r.poke <- struct{}{}:
	default:
	}
}

func (r *Runtime) machine() domain.Machine {
	if r.probe != nil {
		m := r.probe()
		m.Name = r.s.d.Machine
		return m
	}
	return domain.Machine{Name: r.s.d.Machine, CPUs: 4, Slots: 4}
}

// Run drives every project until ctx ends.
func (r *Runtime) Run(ctx context.Context) {
	t := time.NewTicker(r.tickEvery())
	defer t.Stop()
	r.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			r.StopAll()
			return
		case <-t.C:
		case <-r.poke:
		}
		r.Tick(ctx)
	}
}

// tickEvery is the loop period: fast enough for the shortest lease renewal.
func (r *Runtime) tickEvery() time.Duration {
	every := 5 * time.Second
	for _, name := range r.s.Projects() {
		if p, err := r.s.Project(name); err == nil && p.Cfg().Lease.Renew > 0 && p.Cfg().Lease.Renew < every {
			every = p.Cfg().Lease.Renew
		}
	}
	return every
}

// Tick runs one pass: leases, housekeeping, driver steps, merge queue, agent slots.
func (r *Runtime) Tick(ctx context.Context) {
	if r.isStopped() {
		// Shutting down (an update replaces the binary meanwhile): no duty,
		// no trust check, no new work. A check that saw the new binary on
		// disk gave every item of the machine away during an update.
		return
	}
	m := r.machine()
	for _, name := range r.s.Projects() {
		p, err := r.s.Project(name)
		if err != nil {
			continue
		}
		r.renewLeases(ctx, p)
		r.housekeeping(ctx, p, m)
		snap := p.Snapshot()
		if on, _ := r.Standby(); snap.Paused || on {
			continue
		}
		r.s.driveHeld(ctx, p)
		if r.isStopped() {
			return
		}
		r.queues.Add(1)
		go func() {
			defer r.queues.Done()
			if _, err := r.s.ProcessQueue(ctx, p.Name); err != nil {
				r.s.d.Log.Warn("merge queue", "project", p.Name, "err", err)
			}
		}()
		r.fillSlots(ctx, p, m)
	}
}

// slotsFor lists the agent slots of a project on this machine.
func (r *Runtime) slotsFor(p *Project, m domain.Machine) []Slot {
	var out []Slot
	for _, role := range sortedKeys(p.Cfg().Roles) {
		rd := p.Cfg().Roles[role]
		if !r.s.machineFits(p, role, m) {
			continue
		}
		for i := 1; i <= rd.Count; i++ {
			out = append(out, Slot{Agent: fmt.Sprintf("%s-%s-%d", role, domain.Slug(m.Name, 20), i), Role: role, Machine: m, Reviewer: rd.Reviewer})
		}
	}
	return out
}

func (r *Runtime) fillSlots(ctx context.Context, p *Project, m domain.Machine) {
	if r.isStopped() || r.MaxSessions < 0 || r.draining.Load() {
		return
	}
	if on, _ := r.Standby(); on {
		return
	}
	limit := r.MaxSessions
	if limit <= 0 {
		limit = m.Slots
	}
	if limit <= 0 {
		limit = 1
	}
	if why := r.underPressure(m); why != "" {
		if r.newStartErr(p.Name+"/machine", why) {
			r.s.d.Log.Warn("holding new sessions: "+why, "machine", m.Name)
		}
		return
	}
	r.newStartErr(p.Name+"/machine", "")
	// Every session here would be refused: claiming an item only to fail to
	// start it would hold it away from the machines that can. Housekeeping
	// gives back what this machine already holds.
	if len(r.s.machineDistrust(ctx, p)) > 0 {
		return
	}
	// Items already tried in this pass, started or not: a session that ends
	// at once (or a start that fails) is not tried again by the next free
	// slot of the same pass. The next pass decides again.
	failed := map[string]bool{}
	// Items refused for their own files (provenance): the slot takes its
	// next item in the same pass, or one refused item would hold every
	// other item of its role on this machine for as long as it is refused.
	untrusted := map[string]bool{}
	for _, slot := range r.slotsFor(p, m) {
		r.mu.Lock()
		_, busy := r.running[slot.Agent]
		n := len(r.running)
		r.mu.Unlock()
		if busy {
			continue
		}
		if n >= limit {
			return
		}
		for r.fillSlot(ctx, p, slot, failed, untrusted) {
		}
	}
}

// fillSlot gives one slot its next item and starts the session. It reports
// whether the slot should ask again in this pass: only when the item it got
// was refused for provenance, which is the item's matter and not the
// slot's or the machine's.
func (r *Runtime) fillSlot(ctx context.Context, p *Project, slot Slot, failed, untrusted map[string]bool) bool {
	a, err := r.s.nextWorkSkipping(ctx, p.Name, slot, untrusted)
	if err != nil {
		// No authorized work: the agent records nothing and ends its turn.
		return false
	}
	if failed[a.Item.ID] {
		// Another slot just failed to start this item for a reason that
		// is the machine's, not the slot's: do not try again this pass.
		r.s.doneRunning(p, a.Item.ID, slot.Agent)
		return false
	}
	dual := false
	if dualEligible(a.Item, a.Step) && !r.s.hasAlt(p, a.Item.ID, a.Step.Name) {
		if _, err := r.s.ensureWorkspace(ctx, p, a.Item.ID); err == nil {
			if _, err := r.s.prepareAlt(ctx, p, a.Item.ID, a.Step); err == nil {
				dual = true
			} else {
				r.s.d.Log.Warn("second attempt not prepared", "item", a.Item.ID, "err", err)
			}
		}
	}
	if err := r.start(ctx, p, slot, a, ""); err != nil {
		failed[a.Item.ID] = true
		r.s.doneRunning(p, a.Item.ID, slot.Agent)
		if a.Step.Name == RefineStep {
			r.s.forgetRefine(p, a.Item.ID) // it never ran: refine it on a later pass
		}
		// Recorded once per agent and item: the agent's state must say it
		// failed, without repeating it on every pass.
		key := p.Name + "/" + slot.Agent + "/" + a.Item.ID
		if r.newStartErr(key, err.Error()) {
			r.s.d.Log.Warn("start session", "agent", slot.Agent, "item", a.Item.ID, "err", err)
			r.s.sessionState(ctx, p, slot, a, "failed", err.Error(), "", "", "")
			r.saidStartErr(key, true)
			switch {
			case errors.Is(err, domain.ErrNoOtherModel):
				r.s.notifyOwner(ctx, p, "model_rule", a.Item.ID+" cannot be reviewed", err.Error())
			case strings.Contains(err.Error(), "bubblewrap"):
				r.s.notifyOwner(ctx, p, "model_rule", a.Item.ID+" waits: a provider cannot run here", err.Error())
			}
		}
		if r.saidStartErr(key, false) {
			r.s.sessionState(ctx, p, slot, a, "failed", err.Error(), "", "", "")
		}
		if errors.Is(err, errUntrustedItem) {
			untrusted[a.Item.ID] = true
			return true
		}
		return false
	}
	failed[a.Item.ID] = true
	r.newStartErr(p.Name+"/"+slot.Agent+"/"+a.Item.ID, "")
	r.mu.Lock()
	delete(r.startErrSaid, p.Name+"/"+slot.Agent+"/"+a.Item.ID)
	r.mu.Unlock()
	if dual {
		slotB := Slot{Agent: slot.Agent + "-b", Role: slot.Role, Machine: slot.Machine, Reviewer: slot.Reviewer}
		p.mu.Lock()
		p.running[altKey(a.Item.ID)] = slotB.Agent
		p.mu.Unlock()
		if err := r.start(ctx, p, slotB, a, "b"); err != nil {
			r.s.d.Log.Warn("second attempt", "item", a.Item.ID, "err", err)
			r.s.doneRunning(p, altKey(a.Item.ID), slotB.Agent)
			if r.s.hasAlt(p, a.Item.ID, a.Step.Name) {
				r.s.altEnded(ctx, p, a.Item.ID)
			}
		}
	}
	return false
}

// underPressure says why this machine should not start another session
// right now ("" when it may). Only what is measured counts: a machine
// that reports nothing is not held.
// minFreeTmpGB is the free space of the temporary folder below which no
// session starts.
const minFreeTmpGB = 1.0

func (r *Runtime) underPressure(m domain.Machine) string {
	min := r.MinFreeMemGB
	if min == 0 {
		min = 3
	}
	if min > 0 && m.FreeMemGB > 0 && m.FreeMemGB < min {
		return fmt.Sprintf("%.1f GB of memory available, under the %.0f GB kept free for the work in progress", m.FreeMemGB, min)
	}
	if m.TmpFreeGB > 0 && m.TmpFreeGB < minFreeTmpGB {
		// A full temporary folder fails every command of every session.
		return fmt.Sprintf("%.2f GB free in the temporary folder, under the %.0f GB sessions need", m.TmpFreeGB, minFreeTmpGB)
	}
	r.mu.Lock()
	running := len(r.running)
	r.mu.Unlock()
	if running > 0 && m.CPUs > 0 && m.FreeCPU > 0 && m.FreeCPU < 0.5 {
		return fmt.Sprintf("the %d CPUs are busy (%.1f free)", m.CPUs, m.FreeCPU)
	}
	return ""
}

// accountLimits records the usage of an account's windows as the provider
// reports it during a session. Providers repeat it after every request, so
// only a real change (a point of a window, a new reset time) is recorded.
func (r *Runtime) accountLimits(ctx context.Context, p *Project, accountID string, u domain.Usage) {
	p.mu.Lock()
	cur, ok := p.state.Accounts[accountID]
	var old domain.Usage
	if ok {
		old = cur.Usage
	}
	p.mu.Unlock()
	if !ok {
		return
	}
	// A report may carry only some windows: keep what it does not mention.
	if u.FiveHourResets.IsZero() && u.FiveHourPct == 0 {
		u.FiveHourPct, u.FiveHourResets = old.FiveHourPct, old.FiveHourResets
	}
	if u.WeeklyResets.IsZero() && u.WeeklyPct == 0 {
		u.WeeklyPct, u.WeeklyResets = old.WeeklyPct, old.WeeklyResets
	}
	if u.WeeklyTopPct == 0 {
		u.WeeklyTopPct = old.WeeklyTopPct
	}
	u.ContextPct = old.ContextPct
	moved := func(a, b float64) bool { d := a - b; return d >= 1 || d <= -1 }
	if !moved(u.FiveHourPct, old.FiveHourPct) && !moved(u.WeeklyPct, old.WeeklyPct) && !moved(u.WeeklyTopPct, old.WeeklyTopPct) &&
		u.FiveHourResets.Equal(old.FiveHourResets) && u.WeeklyResets.Equal(old.WeeklyResets) && !old.UpdatedAt.IsZero() {
		return
	}
	if err := r.s.ReportUsage(ctx, p.Name, System(), accountID, u); err != nil {
		r.s.d.Log.Debug("account limits", "account", accountID, "err", err)
	}
}

// newStartErr records the start failure of an item ("" = it started) and
// reports whether it differs from the previous one.
func (r *Runtime) newStartErr(key, msg string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.startErrs == nil {
		r.startErrs = map[string]string{}
	}
	if r.startErrs[key] == msg {
		return false
	}
	if msg == "" {
		delete(r.startErrs, key)
	} else {
		r.startErrs[key] = msg
	}
	return true
}

// restateStartErr is how often a start failure that repeats is told to the
// cluster again.
const restateStartErr = 5 * time.Minute

// saidStartErr records that a start failure was just told (now) or says
// whether it is time to tell it again.
func (r *Runtime) saidStartErr(key string, now bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.startErrSaid == nil {
		r.startErrSaid = map[string]time.Time{}
	}
	if !now && time.Since(r.startErrSaid[key]) < restateStartErr {
		return false
	}
	r.startErrSaid[key] = time.Now()
	return true
}

// start opens a provider session for an assignment.
func (r *Runtime) start(ctx context.Context, p *Project, slot Slot, a *Assignment, attempt string) error {
	s := r.s
	var dir string
	var err error
	if attempt == "b" {
		dir, err = s.prepareAlt(ctx, p, a.Item.ID, a.Step)
	} else if a.Step.Name == RefineStep {
		dir, err = s.refineWorkspace(ctx, p, a.Item.ID)
	} else {
		dir, err = s.ensureWorkspace(ctx, p, a.Item.ID)
	}
	if err != nil {
		return err
	}
	// Sent back by the merge queue for a conflict: Barnraise rebases, the
	// agent only resolves (rebasefix.go).
	var conflicts []string
	if attempt != "b" && a.Step.Name != RefineStep {
		conflicts = s.prepareRebase(ctx, p, p.Snapshot().Items[a.Item.ID], a.Step.Name, dir)
	}
	// A .barnraise/ file left stale by an earlier session is put back before it is judged.
	s.restoreBarnraiseFiles(ctx, p, a.Item.ID, dir)
	if err := s.checkProvenance(ctx, p, a.Item.ID, dir); err != nil {
		return err
	}
	p.mu.Lock()
	it := p.state.Items[a.Item.ID].Clone()
	accounts := p.state.AccountList()
	p.mu.Unlock()
	role := slot.Role
	rd := p.Cfg().Roles[role]
	class := a.Step.Class
	if class == "" || class == "auto" {
		class = def(it.Class, def(rd.Class, "normal"))
	}
	if class == "auto" {
		class = "normal"
	}
	avoidFamily, avoidModel := "", ""
	if d := a.Step.DifferentFrom(); d != "" && it.Runs != nil {
		if prev, ok := it.Runs[d]; ok {
			avoidFamily, avoidModel = prev.Family, prev.Model
		}
	}
	now := s.d.Clock.Now()
	blocked := ""
	usable := func(provider string) bool {
		_, cli, _, ok := s.providerFor(p.Cfg(), provider)
		if !ok {
			return false
		}
		// Inside a container the provider's own sandbox is not used: the container is the boundary.
		if why, no := r.NoSandboxProviders[cli]; no && p.Cfg().Container.Image == "" {
			if domain.PickAccount(accounts, provider, s.d.Machine, p.Name, now) != nil {
				blocked = why // an account is ready, only the sandbox stops it
			}
			return false
		}
		if ReadOnlyOnHost[provider] && p.Cfg().Container.Image == "" && !rd.Reviewer {
			return false
		}
		if len(rd.Providers) > 0 && !domain.Contains(rd.Providers, provider) {
			return false
		}
		return domain.PickAccount(accounts, provider, s.d.Machine, p.Name, now) != nil
	}
	// A step that should differ from another (a review from the work it
	// reviews) prefers a different model, but never waits for one: the
	// class's own models follow, the reviewed one included. The owner chose
	// a strong model reviewing over a stall or a weaker reviewer.
	choices, err := p.Cfg().Models.Route(domain.RouteRequest{Class: class, FixedModel: a.Step.Model, Tier: it.Tier, AvoidFamily: avoidFamily, AvoidModel: avoidModel, Usable: usable})
	if avoidModel != "" || avoidFamily != "" {
		same, serr := p.Cfg().Models.Route(domain.RouteRequest{Class: class, FixedModel: a.Step.Model, Tier: it.Tier, Usable: usable})
		if serr == nil {
			for _, c := range same {
				if !slices.Contains(choices, c) {
					choices = append(choices, c)
				}
			}
			if errors.Is(err, domain.ErrNoOtherModel) || len(choices) > 0 {
				err = nil
			}
		}
	}
	if errors.Is(err, domain.ErrNoOtherModel) {
		return fmt.Errorf("step %s of %s must run on a different model from step %s: %w (add a second model to the class in `models.classes`, or an account of another provider)",
			a.Step.Name, it.ID, a.Step.DifferentFrom(), err)
	}
	if err != nil && blocked != "" {
		return fmt.Errorf("%w: %s", err, blocked)
	}
	if err != nil {
		s.notifyAccountsExhausted(ctx, p)
		return err
	}
	// The strongest model of the class that an account here may run now:
	// one at its own limit gives the step to the next.
	choice, acc := s.pickChoice(choices, accounts, p.Name, now)
	if acc == nil {
		// Every model of the class is at its limit: rather than wait days,
		// the step runs one rung down its provider's ladder.
		if fc, facc, ok := s.ladderFallback(p.Cfg().Models, choices, avoidModel, accounts, p.Name, now); ok {
			s.d.Log.Info("every model of the class is at its limit: the step runs one rung down",
				"item", it.ID, "step", a.Step.Name, "class", class, "model", fc.Model, "same_as_the_step_it_reviews", fc.Model == avoidModel)
			choice, acc = fc, facc
		}
	}
	if acc == nil {
		why, until := s.noCapacity(choices, accounts, p.Name, now)
		s.setWaiting(p, it.ID+" "+a.Step.Name+": "+why, until)
		return errors.New(why)
	}
	s.setWaiting(p, "", time.Time{})
	// The account as this machine runs it: its login folder here.
	acc = acc.On(s.d.Machine, s.d.DataDir)
	prov, cli, endpoint, _ := s.providerFor(p.Cfg(), choice.Provider)
	apiKey := ""
	if endpoint != nil {
		if apiKey, err = s.apiKey(acc); err != nil {
			return err
		}
	}
	experiment := ""
	for _, ex := range p.Cfg().Experiments {
		if (ex.Role == "" || ex.Role == role) && inShare(it.ID+ex.Name, ex.Share) {
			experiment = ex.Name
			if ex.Model != "" {
				if mc, ok := parseChoice(ex.Model); ok {
					choice = mc
				}
			}
		}
	}
	wtKey := it.ID
	if attempt == "b" {
		wtKey = altKey(it.ID)
	}
	if err := s.runHook(ctx, p, domain.HookPreSession, wtKey, dir); err != nil {
		return err
	}
	family := p.Cfg().Models.Family(choice.Provider)
	actor := Actor{Kind: ActorAgent, Name: slot.Agent, Role: role, Project: p.Name, ItemID: it.ID, Step: a.Step.Name, Account: acc.ID,
		Scopes: RoleScopes(role, a.Step.Review || a.Step.Type == domain.StepApproval), Family: family, Model: choice.Model, Attempt: attempt,
		Session: NewSecret("ses_")[:20]}
	token := s.tokens.Issue(actor, r.SessionTimeout+sessionTokenGrace)
	prompt, err := s.BuildStepPrompt(ctx, p, it, a.Step, role, slot.Agent)
	if err != nil {
		s.tokens.Revoke(token)
		return err
	}
	prompt.Prompt += rebaseBrief(conflicts)
	if experiment != "" {
		for _, ex := range p.Cfg().Experiments {
			if ex.Name == experiment && ex.Prompt != "" {
				prompt.System += "\n\n" + ex.Prompt
			}
		}
	}
	sessDir := filepath.Join(dir, ".barnraise-session")
	env := map[string]string{
		"BARNRAISE_TOKEN": token, "BARNRAISE_SOCKET": s.d.SocketPath, "BARNRAISE_PROJECT": p.Name, "BARNRAISE_ITEM": it.ID,
		"BARNRAISE_STEP": a.Step.Name, "BARNRAISE_AGENT": slot.Agent, "BARNRAISE_ROLE": role, "BARNRAISE_ACCOUNT": acc.ID,
		"BARNRAISE_POLICY": filepath.Join(sessDir, "policy.json"), "BARNRAISE_WORKTREE": dir,
		"DISABLE_AUTOUPDATER": "1",
	}
	mergeEnv(env, s.worktreeEnv(p, wtKey, dir)) // its own ports, compose project, database
	maps.Copy(env, s.StoreEnv(p))
	run := s.d.IDs.NewID()
	maps.Copy(env, domain.OutfilterEnv(p.Cfg().Outfilter, run))
	if r.EgressProxy != "" {
		proxy := "http://" + r.EgressProxy
		env["HTTPS_PROXY"], env["HTTP_PROXY"], env["https_proxy"], env["http_proxy"] = proxy, proxy, proxy, proxy
		env["NO_PROXY"], env["no_proxy"] = "localhost,127.0.0.1", "localhost,127.0.0.1"
	}
	if err := r.egressTrust(env, sessDir); err != nil {
		s.tokens.Revoke(token)
		return err
	}
	if attempt != "" {
		env["BARNRAISE_ATTEMPT"] = attempt
	}
	if experiment != "" {
		env["BARNRAISE_EXPERIMENT"] = experiment
		for _, ex := range p.Cfg().Experiments {
			if ex.Name == experiment {
				maps.Copy(env, ex.Env)
			}
		}
	}
	mcpPath, settingsPath, err := r.writeSessionFiles(p, sessDir, dir, env, actor)
	if err != nil {
		s.tokens.Revoke(token)
		return err
	}
	policyPath := filepath.Join(sessDir, "policy.json")
	seal, err := sealFiles(mcpPath, settingsPath, policyPath)
	if err != nil {
		s.tokens.Revoke(token)
		return err
	}
	env[domain.PolicyHashEnv] = seal[policyPath]
	sctx, cancel := context.WithTimeout(context.Background(), r.SessionTimeout)
	system := prompt.System
	if note := s.mcpPromptNote(context.Background(), p, role); note != "" {
		system += "\n\n" + note
	}
	spec := ports.SessionSpec{Agent: slot.Agent, Role: role, Account: *acc, Model: choice.Model, Effort: choice.Effort, Workdir: dir,
		Prompt: prompt.Prompt, SystemPrompt: system, Env: env, MCPConfig: mcpPath, SettingsFile: settingsPath,
		Timeout: r.SessionTimeout, RunAsUser: acc.LinuxUser, Endpoint: endpoint}
	if ct := p.Cfg().Container; ct.Image != "" {
		cs, err := r.containerSpec(p, ct, dir, sessDir, acc, cli, env)
		if err != nil {
			s.tokens.Revoke(token)
			cancel()
			return err
		}
		spec.Container = cs
	} else if spec.Confine, err = r.sessionConfine(p, dir, acc, cli, endpoint, env); err != nil {
		s.tokens.Revoke(token)
		cancel()
		return err
	}
	if spec.ACP, err = s.acpAgent(p.Cfg(), choice.Provider); err != nil {
		s.tokens.Revoke(token)
		cancel()
		return err
	}
	if endpoint != nil {
		// Last, after every file of the session is written: the key lives
		// only in the CLI's environment. With the egress proxy, not even
		// there: a placeholder the proxy swaps on the provider's host.
		sealed, release := r.sealKey(endpoint, apiKey)
		env[ports.EndpointKeyEnv] = sealed
		cancelCtx := cancel
		cancel = func() { cancelCtx(); release() }
	}
	if err := verifySeal(seal); err != nil {
		cancel()
		s.tokens.Revoke(token)
		return err
	}
	sess, err := prov.Open(sctx, spec)
	if err != nil {
		cancel()
		s.tokens.Revoke(token)
		return err
	}
	ls := &liveSession{agent: slot.Agent, project: p.Name, itemID: it.ID, step: a.Step.Name, dir: dir, sess: sess, cancel: cancel, started: s.d.Clock.Now(),
		run: run, outputs: filepath.Join(sessDir, domain.OutputsDir), role: slot.Role, provider: choice.Provider, model: choice.String(), feed: newLiveFeed()}
	r.mu.Lock()
	r.running[slot.Agent] = ls
	r.mu.Unlock()
	s.sessionState(ctx, p, slot, a, "starting", experiment, acc.ID, choice.Provider, choice.String())
	go r.watch(p, slot, a, ls, *acc, choice, token, it.Version, attempt)
	return nil
}

func parseChoice(s string) (domain.ModelChoice, bool) {
	prov, rest, ok := strings.Cut(s, "/")
	if !ok {
		return domain.ModelChoice{}, false
	}
	model, effort, _ := strings.Cut(rest, "@")
	return domain.ModelChoice{Provider: prov, Model: model, Effort: effort}, true
}

// inShare deterministically puts share% of keys in an experiment.
func inShare(key string, share int) bool {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32()%100) < share
}

// containerSpec says what a session's container may see: the item's
// worktree, the repository's .git (a worktree's commits live there), the
// agent's own account, the daemon's socket and the barnraise binary the
// hooks call. Nothing else of the machine: not the owner's home, not the
// daemon's state, not other items.
func (r *Runtime) containerSpec(p *Project, ct domain.ContainerDef, worktree, sessDir string, acc *domain.Account, provider string, env map[string]string) (*ports.ContainerSpec, error) {
	home := filepath.Join(sessDir, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	cs := &ports.ContainerSpec{Engine: def(ct.Engine, "docker"), Image: ct.Image, Network: ct.Network, Home: home,
		User: fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())}
	add := func(path string, ro bool) {
		if path == "" {
			return
		}
		if _, err := os.Stat(path); err != nil {
			return
		}
		if ro {
			path += ":ro"
		}
		if !domain.Contains(cs.Mounts, path) {
			cs.Mounts = append(cs.Mounts, path)
		}
	}
	add(worktree, false)
	add(filepath.Join(p.Cfg().Root, ".git"), false)
	add(r.s.d.SocketPath, false)
	add(r.s.d.BinaryPath, true)
	// The project's shared stores, at the same path as outside: what the
	// setup put there is what the agent's tools find.
	for _, dir := range r.s.storeDirs(p) {
		add(dir, false)
	}
	cfgDir := acc.ConfigDir
	if cfgDir == "" {
		// The CLI's own login: its folder under the owner's home, and only that.
		host, _ := os.UserHomeDir()
		switch provider {
		case "claude":
			cfgDir = filepath.Join(host, ".claude")
			env["CLAUDE_CONFIG_DIR"] = cfgDir
		case "codex":
			cfgDir = filepath.Join(host, ".codex")
			env["CODEX_HOME"] = cfgDir
		case "gemini":
			// The CLI reads $GEMINI_CLI_HOME/.gemini; only that folder is mounted.
			cfgDir = filepath.Join(host, ".gemini")
			env["GEMINI_CLI_HOME"] = host
		}
	}
	add(cfgDir, false)
	host, _ := os.UserHomeDir()
	for _, m := range ct.Mounts {
		path, ro := m, false
		if p, ok := strings.CutSuffix(m, ":ro"); ok {
			path, ro = p, true
		}
		if rest, ok := strings.CutPrefix(path, "~/"); ok {
			path = filepath.Join(host, rest)
		}
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("container.mounts: %q must be an absolute path (or start with ~/)", m)
		}
		if why := domain.ForbiddenMount(path, host); why != "" {
			return nil, fmt.Errorf("container.mounts: %s is never mounted into an agent's container: %s", m, why)
		}
		add(path, ro)
	}
	cs.Extra = r.gpuArgs(cs.Engine, ct.GPU)
	return cs, nil
}

// GPUProbe reports what the machine offers a container: whether the engine
// has the NVIDIA runtime, and the render devices under /dev/dri with the
// group that may use them. Replaceable in tests.
var GPUProbe = func(engine string) (nvidia bool, dri string, renderGID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, engine, "info", "--format", "{{json .Runtimes}}").Output(); err == nil && strings.Contains(string(out), "nvidia") {
		nvidia = true
	}
	if ents, err := os.ReadDir("/dev/dri"); err == nil && len(ents) > 0 {
		dri = "/dev/dri"
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), "renderD") {
				if out, err := exec.CommandContext(ctx, "stat", "-c", "%g", filepath.Join(dri, e.Name())).Output(); err == nil {
					renderGID = strings.TrimSpace(string(out))
				}
				break
			}
		}
	}
	return nvidia, dri, renderGID
}

// KVMProbe reports the hardware virtualization device and the group that
// may use it ("" when the machine has none). Replaceable in tests.
var KVMProbe = func() (dev, gid string) {
	st, err := os.Stat("/dev/kvm")
	if err != nil {
		return "", ""
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		gid = strconv.FormatUint(uint64(sys.Gid), 10)
	}
	return "/dev/kvm", gid
}

// gpuArgs gives the container the machine's GPU. It is on by default:
// building a game, a 3D scene or a model needs it to test the result.
func (r *Runtime) gpuArgs(engine, mode string) []string {
	if mode == "none" {
		return nil
	}
	r.mu.Lock()
	if r.gpu == nil {
		r.mu.Unlock()
		nv, dri, gid := GPUProbe(engine)
		var args []string
		if nv {
			args = append(args, "--gpus", "all")
		}
		if _, err := exec.LookPath("nvidia-smi"); err == nil && !nv {
			r.s.d.Log.Warn("this machine has an NVIDIA GPU but the container engine has no nvidia runtime: containers will not see it (install the NVIDIA Container Toolkit)", "engine", engine)
		}
		if dri != "" {
			args = append(args, "--device", dri)
			if gid != "" {
				args = append(args, "--group-add", gid)
			}
		}
		// Hardware virtualization, for Android emulators inside the container.
		if dev, gid := KVMProbe(); dev != "" {
			args = append(args, "--device", dev)
			if gid != "" {
				args = append(args, "--group-add", gid)
			}
		}
		r.mu.Lock()
		r.gpu = &args
	}
	args := *r.gpu
	r.mu.Unlock()
	if mode == "all" && !domain.Contains(args, "--gpus") {
		args = append([]string{"--gpus", "all"}, args...) // asked for explicitly: let the engine say so if it cannot
	}
	return args
}

// writeSessionFiles writes the MCP config, the provider settings with the
// Barnraise hooks, and the compiled policy the hook reads without network.
func (r *Runtime) writeSessionFiles(p *Project, sessDir, worktree string, env map[string]string, a Actor) (string, string, error) {
	if err := os.MkdirAll(sessDir, 0o700); err != nil {
		return "", "", err
	}
	bin := r.s.d.BinaryPath
	if bin == "" {
		bin = "barnraise"
	}
	home, _ := os.UserHomeDir()
	pol := p.Cfg().Policy
	pol.Home = home
	pol.Worktree = worktree
	// Its own check logs and its worktree's git state are the session's to
	// read, though they sit in Barnraise's folders.
	pol.ReadAlso = append(pol.ReadAlso,
		filepath.Join(r.s.d.DataDir, "logs", p.Name, "*-"+a.ItemID+"-*"),
		filepath.Join(repoGit(p), "worktrees", filepath.Base(worktree), "**"))
	pol.MainBranch = p.Cfg().MainBranch
	pol.PrivateTmp = p.Cfg().Container.Image != "" // the container's /tmp is in memory and its own
	if it := p.Snapshot().Items[a.ItemID]; it != nil {
		pol.Unlocked = append([]string(nil), it.Unlocked...)
	}
	// The agent profile's command rules join the project's.
	for _, rx := range domain.AgentDenyCommands() {
		if !domain.Contains(pol.DenyCommands, rx) {
			pol.DenyCommands = append(pol.DenyCommands, rx)
		}
	}
	polData, _ := json.MarshalIndent(pol, "", "  ")
	if err := os.WriteFile(filepath.Join(sessDir, "policy.json"), polData, 0o600); err != nil {
		return "", "", err
	}
	mcpEnv := map[string]string{}
	for _, k := range []string{"BARNRAISE_TOKEN", "BARNRAISE_SOCKET", "BARNRAISE_PROJECT", "BARNRAISE_ITEM", "BARNRAISE_STEP", "BARNRAISE_AGENT", "BARNRAISE_ROLE"} {
		mcpEnv[k] = env[k]
	}
	servers := map[string]any{"barnraise": map[string]any{"command": bin, "args": []string{"mcp"}, "env": mcpEnv}}
	// Only what the owner allowed for this role: barnraise.yaml's `mcp:`,
	// and the servers of this machine the panel lets agents use.
	extra, missing, err := r.s.sessionMCP(context.Background(), p, a.Role)
	if err != nil {
		return "", "", err
	}
	if len(missing) > 0 {
		r.s.d.Log.Warn("MCP servers agents may use are not on this machine: the session runs without them",
			"project", p.Name, "agent", a.Name, "servers", strings.Join(missing, ", "))
	}
	for name, d := range extra {
		srv := map[string]any{}
		if d.URL != "" {
			srv["url"] = d.URL
			srv["type"] = def(d.Type, "http")
			if len(d.Headers) > 0 {
				srv["headers"] = d.Headers
			}
		} else {
			srv["command"] = d.Command
			srv["args"] = append([]string{}, d.Args...)
		}
		if len(d.Env) > 0 {
			srv["env"] = d.Env
		}
		servers[name] = srv
	}
	mcp := map[string]any{"mcpServers": servers}
	mcpData, _ := json.MarshalIndent(mcp, "", "  ")
	mcpPath := filepath.Join(sessDir, "mcp.json")
	if err := os.WriteFile(mcpPath, mcpData, 0o600); err != nil {
		return "", "", err
	}
	hook := func(sub string) []map[string]any {
		return []map[string]any{{"matcher": "*", "hooks": []map[string]any{{"type": "command", "command": bin + " hook " + sub, "timeout": 10}}}}
	}
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse":       hook("pre-tool"),
			"PostToolUse":      hook("post-tool"),
			"SessionStart":     hook("prime"),
			"PreCompact":       hook("prime"),
			"UserPromptSubmit": hook("mail"),
			"Stop":             hook("stop"),
		},
		"statusLine":  map[string]any{"type": "command", "command": bin + " statusline"},
		"permissions": map[string]any{"defaultMode": "bypassPermissions", "deny": []string{"Read(~/.ssh/**)", "Read(~/.config/gh/**)", "Read(~/.aws/**)", "Edit(.github/workflows/**)", "Edit(.barnraise/**)", "Edit(barnraise.yaml)", "Edit(criteria.json)", "Edit(.barnraise-session/*.json)"}},
		"env":         map[string]string{"DISABLE_AUTOUPDATER": "1"},
	}
	gitCfg := [][2]string{{"commit.gpgsign", "false"}, {"tag.gpgsign", "false"}}
	// Inside a container the container is the sandbox (bubblewrap cannot nest there).
	if r.Sandbox && p.Cfg().Container.Image == "" && !r.microVMSessions() {
		settings["sandbox"] = map[string]any{"enabled": true, "failIfUnavailable": true, "allowUnsandboxedCommands": false,
			"network": map[string]any{"allowedDomains": domain.AllowHostNames(pol.AllowHosts)}}
		// The sandbox guards files such as .bashrc and .mcp.json by mounting
		// placeholders over them in the working folder; the agent's git
		// would list them as new files and fail to read or add them. Its git
		// ignores them; the worktree and the owner's checkout do not change.
		if ex, err := sandboxExcludes(sessDir); err == nil {
			gitCfg = append(gitCfg, [2]string{"core.excludesFile", ex})
		}
		// A virtual X server for screenshots (policy.virtual_display): X
		// writes its lock in /tmp, a fixed path, so the sandbox lets the
		// session write there. Unix sockets stay closed; the server and the
		// game meet on local TCP inside the sandbox.
		if pol.VirtualDisplay {
			perms := settings["permissions"].(map[string]any)
			perms["allow"] = append(asStrings(perms["allow"]), "Edit(//tmp/**)")
		}
	}
	// The agent's git, set through the environment for every provider:
	// it does not sign (the owner's key needs a passphrase nobody can type
	// in a headless session, and the sandbox keeps ~/.gnupg read-only, so
	// every commit failed); and inside the sandbox it ignores the sandbox's
	// placeholder files. The owner's own git and Barnraise's commits keep
	// the owner's settings.
	n := 0
	for _, kv := range gitCfg {
		env[fmt.Sprintf("GIT_CONFIG_KEY_%d", n)], env[fmt.Sprintf("GIT_CONFIG_VALUE_%d", n)] = kv[0], kv[1]
		n++
	}
	env["GIT_CONFIG_COUNT"] = fmt.Sprint(n)
	settings["env"] = map[string]string{"DISABLE_AUTOUPDATER": "1"}
	for k, v := range env {
		if strings.HasPrefix(k, "GIT_CONFIG_") {
			settings["env"].(map[string]string)[k] = v
		}
	}
	setData, _ := json.MarshalIndent(settings, "", "  ")
	setPath := filepath.Join(sessDir, "settings.json")
	if err := os.WriteFile(setPath, setData, 0o600); err != nil {
		return "", "", err
	}
	meta, _ := json.MarshalIndent(map[string]any{"agent": a.Name, "role": a.Role, "item": a.ItemID, "step": a.Step, "project": a.Project}, "", "  ")
	_ = os.WriteFile(filepath.Join(sessDir, "session.json"), meta, 0o600)
	return mcpPath, setPath, nil
}

// sandboxPlaceholders are the files Claude Code's sandbox mounts over in the
// working folder to keep commands from writing them.
var sandboxPlaceholders = []string{".bashrc", ".bash_profile", ".bash_login", ".profile", ".zshrc", ".zshenv", ".zprofile", ".zlogin",
	".gitconfig", ".gitmodules", ".ripgreprc", ".mcp.json", ".claude/", ".idea/", ".vscode/", ".idea", ".vscode", ".claude"}

// sandboxExcludes writes the ignore file of a sandboxed session's git: the
// owner's own global ignores, then the sandbox's placeholders, which match
// only when untracked (a project's own .gitmodules stays tracked).
func sandboxExcludes(sessDir string) (string, error) {
	var b strings.Builder
	home, _ := os.UserHomeDir()
	global := filepath.Join(home, ".config", "git", "ignore")
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		global = filepath.Join(x, "git", "ignore")
	}
	if own, err := os.ReadFile(global); err == nil {
		b.Write(own)
		b.WriteString("\n")
	}
	b.WriteString("# Claude Code's sandbox placeholders (barnraise)\n")
	for _, f := range sandboxPlaceholders {
		b.WriteString("/" + f + "\n")
	}
	path := filepath.Join(sessDir, "git-ignore")
	return path, os.WriteFile(path, []byte(b.String()), 0o600)
}

// watch follows a session's events until it ends, then decides what the end means.
func (r *Runtime) watch(p *Project, slot Slot, a *Assignment, ls *liveSession, acc domain.Account, choice domain.ModelChoice, token string, startVersion int, attempt string) {
	s := r.s
	ctx := context.Background()
	runKey := a.Item.ID
	if attempt == "b" {
		runKey = altKey(a.Item.ID)
	}
	defer func() {
		ls.cancel()
		s.tokens.Revoke(token)
		ls.feed.close(s.d.Clock.Now(), "the session ended")
		r.mu.Lock()
		delete(r.running, slot.Agent)
		r.mu.Unlock()
		s.doneRunning(p, runKey, slot.Agent)
		r.Poke()
	}()
	var usage domain.TokenUsage
	procDir := s.WorktreeDir(p.Name, runKey)
	if a.Step.Name == RefineStep {
		procDir = s.WorktreeDir(p.Name, "_refine-"+a.Item.ID)
	}
	procMark := s.procMark(procDir)
	limited := false
	var resets time.Time
	lastState := time.Time{}
	safety := time.NewTicker(maxDur(p.Cfg().SafetyCommit, time.Minute))
	defer safety.Stop()
	pulse := time.NewTicker(pulseEvery)
	defer pulse.Stop()
	events := ls.sess.Events()
loop:
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				break loop
			}
			ls.feed.touch(ev, s.d.Clock.Now())
			if l, show := liveLineOf(ev, s.d.Clock.Now()); show {
				ls.feed.publish(l)
			}
			switch ev.Kind {
			case ports.PEUsage:
				if ev.Limits != nil {
					r.accountLimits(ctx, p, acc.ID, *ev.Limits)
				}
				if ev.Usage != nil {
					usage.Input = maxI(usage.Input, ev.Usage.Input)
					usage.Output = maxI(usage.Output, ev.Usage.Output)
					usage.CacheRead = maxI(usage.CacheRead, ev.Usage.CacheRead)
					usage.CacheWrite = maxI(usage.CacheWrite, ev.Usage.CacheWrite)
					usage.CostUSD = ev.Usage.CostUSD
				}
			case ports.PEPermission:
				s.providerViolation(ctx, p, slot.Agent, a.Item.ID, ev)
			case ports.PELimit:
				limited = true
				resets = ev.ResetsAt
			case ports.PEToolUse:
				s.noteToolFiles(ctx, p, a.Item.ID, ls.dir, ev.Tool, ev.Input)
				if ev.Tool == "Read" || ev.Tool == "Grep" || ev.Tool == "Glob" {
					usage.ExploreReads++
				}
				if time.Since(lastState) > 15*time.Second {
					lastState = time.Now()
					s.sessionState(ctx, p, slot, a, "working", ev.Tool, acc.ID, choice.Provider, choice.String(), usage)
				}
			}
		case <-pulse.C:
			ls.feed.beat(s.d.Clock.Now())
		case <-safety.C:
			// Safety commit of pending changes.
			// Not while a rebase Barnraise started waits for this session.
			if s.settleUnfinished(ctx, p, a.Item.ID, ls.dir, true) {
				_, _ = s.commitStep(ctx, p, a.Item.ID, "chore("+a.Item.ID+"): safety commit during "+a.Step.Name)
			}
		}
	}
	res, err := ls.sess.Wait()
	if res.Usage.Total() > usage.Total() {
		usage = res.Usage
	}
	if res.Limited {
		limited = true
		if !res.ResetsAt.IsZero() {
			resets = res.ResetsAt
		}
	}
	usage.Agent, usage.Account, usage.Provider, usage.Model, usage.Role, usage.ItemID, usage.Step = slot.Agent, acc.ID, choice.Provider, choice.Model, slot.Role, a.Item.ID, a.Step.Name
	usage.Duration = s.d.Clock.Now().Sub(ls.started)
	usage.CPUSeconds, usage.PeakRSSBytes = s.procSince(procDir, procMark)
	usage.FilteredRuns, usage.FilterSaved = filterSavings(ls.outputs, ls.run)
	_, _ = s.emit(ctx, p, domain.EvAgentUsage, a.Item.ID, slot.Agent, domain.Payload{Usage: &usage})
	if limited {
		why := res.Text
		if err != nil {
			why += " " + err.Error()
		}
		if domain.LimitedModel(why, choice.Model) {
			s.markModelLimited(acc.ID, choice.Model, resets)
		} else {
			s.markAccountLimited(ctx, p, acc.ID, resets)
		}
	}
	if strings.TrimSpace(res.Text) != "" {
		_, _ = s.emit(ctx, p, domain.EvJournal, a.Item.ID, slot.Agent, domain.Payload{Journal: &domain.JournalEntry{Agent: slot.Agent, ItemID: a.Item.ID, Step: a.Step.Name, Text: truncate(res.Text, 2000), At: s.d.Clock.Now().UTC()}})
	}
	state := "done"
	detail := ""
	if err != nil {
		state, detail = "failed", err.Error()
	}
	s.sessionState(ctx, p, slot, a, state, detail, acc.ID, choice.Provider, choice.String(), usage)
	if attempt == "b" {
		// The second attempt ends either way: if it did not deliver, attempt a alone decides.
		p.mu.Lock()
		cur := p.state.Items[a.Item.ID]
		pending := cur != nil && cur.Alt != nil && !cur.Alt.BEnded
		p.mu.Unlock()
		if pending {
			s.altEnded(ctx, p, a.Item.ID)
		}
		return
	}
	// Did the step end with a delivery or review?
	p.mu.Lock()
	cur := p.state.Items[a.Item.ID]
	if cur != nil && cur.Alt != nil && cur.Alt.A != nil {
		p.mu.Unlock()
		return // delivered; waiting for the second attempt
	}
	moved := cur == nil || cur.Step != a.Step.Name || cur.Status != domain.StatusInProgress || cur.Version > startVersion && hasStepEvent(cur, a.Step.Name)
	attempts := 0
	if cur != nil {
		attempts = cur.Attempts
	}
	p.mu.Unlock()
	if moved || cur == nil {
		return
	}
	if limited || errors.Is(err, context.Canceled) || ls.halted.Load() || r.isStopped() {
		return // not the agent's fault: another account or a resume picks it up
	}
	if attempts+1 < 2 {
		_, _ = s.mutateItem(ctx, p, a.Item.ID, domain.EvItemUpdated, "barnraise", func(x *domain.Item) error {
			x.Attempts++
			return nil
		}, domain.Payload{Note: "session ended without a delivery"})
		_, _ = s.SendMessage(ctx, p.Name, System(), domain.Message{To: "item:" + a.Item.ID, ItemID: a.Item.ID,
			Text: "Your previous session ended without a delivery. Finish the step and end it with exactly one deliver (or review)."})
		return
	}
	reason := "the agent session ended twice without a delivery"
	if detail != "" {
		reason += ": " + detail
	}
	if a.Step.Review || a.Step.Type == domain.StepApproval {
		_, _, _ = s.finishStep(ctx, p, stepOutcome{Item: a.Item.ID, Step: a.Step.Name, Outcome: domain.OutcomeNeedsContext, Reason: reason, Actor: slot.Agent})
		return
	}
	_, _, _ = s.finishStep(ctx, p, stepOutcome{Item: a.Item.ID, Step: a.Step.Name, Outcome: domain.OutcomeBlocked, Reason: reason, Actor: slot.Agent})
}

// refineWorkspace is a read-only checkout of main for the analyst: drafts get no branch or PR.
func (s *Service) refineWorkspace(ctx context.Context, p *Project, itemID string) (string, error) {
	if p.Ports().Git == nil {
		return p.Cfg().Root, nil
	}
	dir := s.WorktreeDir(p.Name, "_refine-"+itemID)
	main, err := p.Ports().Git.MainHead(ctx)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return dir, p.Ports().Git.Checkout(ctx, dir, main)
	}
	if err := p.Ports().Git.AddDetachedWorktree(ctx, dir, main); err != nil {
		return "", err
	}
	s.excludeFromGit(dir, "/.barnraise-session/")
	return dir, nil
}

// mainWorkspace is a read-only checkout of the main branch as the remote has
// it now: fetched first, so what others pushed is there, and apart from the
// owner's own checkout, which may lag or hold their edits.
func (s *Service) mainWorkspace(ctx context.Context, p *Project) (string, error) {
	g := p.Ports().Git
	if g == nil {
		return p.Cfg().Root, nil
	}
	fctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	_ = g.Fetch(fctx) // offline: the last fetch still answers
	cancel()
	main, err := g.MainHead(ctx)
	if err != nil {
		return "", err
	}
	dir := s.WorktreeDir(p.Name, "_main")
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return dir, g.Checkout(ctx, dir, main)
	}
	if err := g.AddDetachedWorktree(ctx, dir, main); err != nil {
		return "", err
	}
	return dir, nil
}

// hasStepEvent is a cheap check that the step's run was recorded.
func hasStepEvent(it *domain.Item, step string) bool {
	_, ok := it.Runs[step]
	return ok && it.Step != step
}

func maxI(a, b int64) int64 {
	if b > a {
		return b
	}
	return a
}

func maxDur(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// sessionState publishes the agent's state for the panel.
func (s *Service) sessionState(ctx context.Context, p *Project, slot Slot, a *Assignment, state, detail, account, provider, model string, sofar ...domain.TokenUsage) {
	info := &domain.SessionInfo{Agent: slot.Agent, Role: slot.Role, Account: account, Provider: provider, Model: model, State: state, Detail: truncate(detail, 200), At: s.d.Clock.Now().UTC(), Machine: s.d.Machine}
	for _, u := range sofar {
		info.Tokens, info.CostUSD = u.Total(), u.CostUSD
	}
	if a != nil && a.Item != nil {
		info.ItemID, info.Step = a.Item.ID, a.Step.Name
	}
	s.emitEphemeral(p, domain.EvAgentSession, info.ItemID, slot.Agent, domain.Payload{Session: info})
}

// emitEphemeral applies and fans out an event without storing it (heartbeats, live session state).
func (s *Service) emitEphemeral(p *Project, typ, subject, actor string, pl domain.Payload) {
	e := domain.NewEvent(s.d.IDs.NewID(), typ, p.Name, subject, s.d.Machine, actor, s.d.Clock.Now(), pl)
	p.mu.Lock()
	_ = p.state.Apply(e)
	delete(p.state.Seen, e.ID)
	p.mu.Unlock()
	if pl.MachineI != nil {
		s.telemetry.add(pl.MachineI, e.Time)
	}
	s.fanout(p, e)
}

// StopAll ends every session (daemon shutdown).
func (r *Runtime) StopAll() {
	r.mu.Lock()
	r.stopped = true
	list := make([]*liveSession, 0, len(r.running))
	for _, ls := range r.running {
		list = append(list, ls)
	}
	r.mu.Unlock()
	for _, ls := range list {
		ls.stop()
	}
	// Each session's watcher still saves the step (commit, push) after the
	// agent ends: wait for it, so a shutdown does not cut a push in half.
	deadline := time.Now().Add(stopAllWait)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		n := len(r.running)
		r.mu.Unlock()
		if n == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	queues := make(chan struct{})
	go func() { r.queues.Wait(); r.s.stale.passes.Wait(); close(queues) }()
	select {
	case <-queues:
	case <-time.After(time.Until(deadline)):
	}
}

// stopAllWait bounds how long StopAll waits for sessions to save their step.
const stopAllWait = 30 * time.Second

// StopProject ends the sessions of a project (pause).
func (r *Runtime) StopProject(project string) {
	r.mu.Lock()
	var list []*liveSession
	for _, ls := range r.running {
		if ls.project == project {
			list = append(list, ls)
		}
	}
	r.mu.Unlock()
	for _, ls := range list {
		ls.stop()
	}
}

func (s *Service) stopItemSessions(itemID string) {
	r := s.runtime
	r.mu.Lock()
	var list []*liveSession
	for _, ls := range r.running {
		if ls.itemID == itemID {
			list = append(list, ls)
		}
	}
	r.mu.Unlock()
	for _, ls := range list {
		ls.stop()
	}
}

// Sessions lists live sessions on this machine.
func (r *Runtime) Sessions() []map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]string
	for _, ls := range r.running {
		out = append(out, map[string]string{"agent": ls.agent, "project": ls.project, "item": ls.itemID, "step": ls.step, "since": ls.started.Format(time.RFC3339)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["agent"] < out[j]["agent"] })
	return out
}

// renewLeases renews this machine's claims; a lost lease stops the work on it.
func (r *Runtime) renewLeases(ctx context.Context, p *Project) {
	if p.Ports().Arbiter == nil {
		return
	}
	every := p.Cfg().Lease.Renew
	if every <= 0 {
		every = 30 * time.Second
	}
	if time.Since(r.lastRenew[p.Name]) < every {
		return
	}
	r.lastRenew[p.Name] = time.Now()
	p.mu.Lock()
	leases := map[string]ports.Lease{}
	maps.Copy(leases, p.leases)
	p.mu.Unlock()
	for _, rn := range renewAll(ctx, p.Ports().Arbiter, leases) {
		id, nl, err := rn.id, rn.nl, rn.err
		switch {
		case err == nil:
			p.mu.Lock()
			if _, still := p.leases[id]; still {
				p.leases[id] = nl
			}
			p.mu.Unlock()
		case errors.Is(err, ports.ErrLost) || errors.Is(err, ports.ErrNotHeld):
			p.mu.Lock()
			_, still := p.leases[id]
			p.mu.Unlock()
			if !still {
				continue // released meanwhile (the item was integrated): nothing was lost
			}
			r.s.d.Log.Warn("lease lost; another machine took the item", "item", id)
			r.s.stopItemSessions(id)
			p.mu.Lock()
			il, iok := p.issueLeases[id]
			delete(p.leases, id)
			delete(p.issueLeases, id)
			delete(p.running, id)
			p.mu.Unlock()
			// The machine that took the item takes its issue too.
			if iok {
				r.s.releaseLeaseValue(ctx, p, il)
			}
		default:
			// Offline: keep working on the current step; we cannot claim or integrate meanwhile.
			r.s.d.Log.Debug("lease renew failed", "item", id, "err", err)
		}
	}
	// The tracker entries of the items held here, renewed the same way; an
	// item whose issue another Barnraise took stops too.
	p.mu.Lock()
	issues := map[string]ports.Lease{}
	maps.Copy(issues, p.issueLeases)
	p.mu.Unlock()
	for _, rn := range renewAll(ctx, p.Ports().Arbiter, issues) {
		id, l, nl, err := rn.id, rn.l, rn.nl, rn.err
		switch {
		case err == nil:
			p.mu.Lock()
			if _, still := p.issueLeases[id]; still {
				p.issueLeases[id] = nl
			}
			p.mu.Unlock()
		case errors.Is(err, ports.ErrLost) || errors.Is(err, ports.ErrNotHeld):
			p.mu.Lock()
			_, still := p.issueLeases[id]
			delete(p.issueLeases, id)
			p.mu.Unlock()
			if still {
				r.s.d.Log.Warn("issue lease lost; another Barnraise took the issue", "item", id, "issue", l.Key)
				r.s.stopItemSessions(id)
				p.mu.Lock()
				delete(p.running, id)
				p.mu.Unlock()
			}
		default:
			r.s.d.Log.Debug("issue lease renew failed", "item", id, "err", err)
		}
	}
}

// renewal is one lease renewed: what it was, what it became, and how.
type renewal struct {
	id    string
	l, nl ports.Lease
	err   error
}

// renewParallel is how many leases are renewed at once. Each renewal is a
// round trip to the git server; one after the other, a cluster holding a
// few dozen leases spent its whole pass renewing them, and the heartbeat
// and every step waited behind.
const renewParallel = 4

// renewAll renews the leases at once, a few at a time, and returns every
// result in the order of their ids.
func renewAll(ctx context.Context, a ports.Arbiter, leases map[string]ports.Lease) []renewal {
	out := make([]renewal, 0, len(leases))
	for id, l := range leases {
		out = append(out, renewal{id: id, l: l})
	}
	slices.SortFunc(out, func(x, y renewal) int { return strings.Compare(x.id, y.id) })
	sem := make(chan struct{}, renewParallel)
	var wg sync.WaitGroup
	for i := range out {
		wg.Add(1)
		sem <- struct{}{}
		go func(rn *renewal) {
			defer wg.Done()
			defer func() { <-sem }()
			rn.nl, rn.err = a.Renew(ctx, rn.l)
		}(&out[i])
	}
	wg.Wait()
	return out
}

func asStrings(v any) []string {
	out, _ := v.([]string)
	return out
}
