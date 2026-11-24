// Package app holds the use cases: claim, deliver, review, integrate,
// propose, set focus… It orchestrates the domain through ports. The daemon
// is the only authority: only it writes state, touches main and decides
// transitions.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Errors exposed to adapters (mapped to HTTP/MCP codes).
var (
	ErrNotFound    = errors.New("not found")
	ErrForbidden   = errors.New("forbidden")
	ErrInvalid     = errors.New("invalid request")
	ErrConflict    = errors.New("conflict")
	ErrUnknownProj = errors.New("unknown project")
	ErrPaused      = errors.New("barnraise is paused")
	// ErrStandby is ErrPaused for a machine: nothing starts on it.
	ErrStandby = fmt.Errorf("%w: this machine is in standby (barnraise standby off)", ErrPaused)
	ErrNoWork  = errors.New("no authorized work")
)

// Deps are the ports shared by all projects of a daemon.
type Deps struct {
	Clock  ports.Clock
	IDs    ports.IDGen
	Store  ports.EventStore
	Bus    ports.EventBus // optional
	Checks ports.CheckRunner
	// CopyTree copies a directory to a path that does not exist, sharing
	// blocks where the filesystem can; optional (cp is the default).
	CopyTree func(ctx context.Context, src, dst string) error
	Notifier ports.Notifier // optional
	// ResolveSecret turns a secret reference (keychain:…, env:…) into its
	// value; optional. Used for the environment of the owner's MCP servers.
	ResolveSecret func(ref string) (string, error)
	Providers     map[string]ports.Provider
	ACP           ports.Provider  // optional: runs the providers of `type: acp`
	Blobs         ports.BlobStore // optional
	Knowledge     ports.Knowledge // optional
	Exec          ports.Exec      // optional
	// Host reads this machine's processes and disk for the inventory; optional.
	Host    ports.Host
	Log     *slog.Logger
	Machine string
	Version string
	// BuildTime is the time of the commit this binary was built from.
	BuildTime time.Time
	// SelfUpdate: the daemon takes a newer peer's binary by itself.
	SelfUpdate bool
	DataDir    string // worktrees, logs, policy files
	SocketPath string
	BinaryPath string // the barnraise binary, for hooks and MCP config
	// Isolation are Barnraise's own confinement layers (Landlock, microVM).
	Isolation Isolation
	// Cloner copies build caches copy-on-write; optional.
	Cloner ports.TreeCloner
	// Prober and Installer answer what runs on this machine and install
	// the optional tools (ADR-0046); optional.
	Prober    ports.CapabilityProber
	Installer ports.ToolInstaller
	// Login runs the providers' own logins for the owner to follow from the
	// panel; optional.
	Login ports.ProviderLogin
}

// ProjectPorts are per-project adapters.
type ProjectPorts struct {
	Git      ports.Git
	Arbiter  ports.Arbiter
	Tracker  ports.Tracker  // optional (local tracker when nil)
	CodeHost ports.CodeHost // optional
	Triage   ports.Triage   // optional
	Advisor  Advisor        // optional: answers the owner about a decision
}

// Project is a repository the daemon drives.
type Project struct {
	Name   string
	cfgV   atomic.Pointer[domain.ProjectConfig]
	portsV atomic.Pointer[ProjectPorts]

	mu    sync.Mutex
	state *domain.State
	// claims held by this machine
	leases map[string]ports.Lease
	// items with a step running on this machine (agent session or check)
	running map[string]string // item -> runner (agent name or "driver")
	refined map[string]int    // draft item -> version the analyst last refined
	opening map[string]bool   // items whose pull request is being opened right now
	queueMu sync.Mutex
}

// Service is the application.
type Service struct {
	d        Deps
	mu       sync.RWMutex
	projects map[string]*Project
	hub      *Hub
	tokens   *TokenRegistry
	sessions *sessionRegistry
	runtime  *Runtime
	// trust is the provenance state (provenance.go).
	trust *trustState
	// telemetry is the short in-memory window of machine readings.
	telemetry telemetry
	// trackerMu serializes the tracker updates of each item (project/item -> *sync.Mutex).
	trackerMu sync.Map
	// wtLocks serializes setup and stripping of each worktree (dir -> *sync.Mutex).
	wtLocks sync.Map
	// local is what only this machine sees: its worktrees and their processes.
	local localView
	// stale is the stale-read warning's memory (staleread.go).
	stale staleState
	// caps is the last capability report of this machine (capabilities.go).
	caps capState
	// logins are the provider logins that run on this machine (login.go).
	logins loginRegistry
	// autopilot is each project's autopilot run (autopilot.go).
	autopilot sync.Map
}

// New creates the service.
func New(d Deps) *Service {
	if d.Clock == nil {
		d.Clock = SystemClock{}
	}
	if d.CopyTree == nil {
		d.CopyTree = copyTree
	}
	if d.IDs == nil {
		d.IDs = RandomIDs{}
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Machine == "" {
		d.Machine = "local"
	}
	s := &Service{d: d, projects: map[string]*Project{}, hub: NewHub(), tokens: NewTokenRegistry(), sessions: newSessionRegistry()}
	s.runtime = newRuntime(s)
	s.trust = newTrustState(d.BinaryPath)
	return s
}

// Deps returns the shared ports.
func (s *Service) Deps() Deps { return s.d }

// Hub returns the live event hub (SSE, webhooks).
func (s *Service) Hub() *Hub { return s.hub }

// Tokens returns the session token registry.
func (s *Service) Tokens() *TokenRegistry { return s.tokens }

// Runtime returns the agent runtime.
func (s *Service) Runtime() *Runtime { return s.runtime }

// Machine is this machine's name.
func (s *Service) Machine() string { return s.d.Machine }

// AddProject loads a project and replays its event log.
func (s *Service) AddProject(ctx context.Context, cfg *domain.ProjectConfig, pp ProjectPorts) (*Project, error) {
	if errs := cfg.Validate(); len(errs) > 0 {
		return nil, fmt.Errorf("project %s: %w", cfg.Name, errors.Join(errs...))
	}
	p := &Project{Name: cfg.Name, state: domain.NewState(cfg.Name), leases: map[string]ports.Lease{}, running: map[string]string{}}
	p.cfgV.Store(cfg)
	p.SetPorts(pp)
	// The replay and the registration happen under s.mu so that an event
	// ingested from another machine is either in the replay or applied to
	// the registered project, never lost between the two.
	s.mu.Lock()
	evs, err := s.d.Store.Load(ctx, cfg.Name, 0)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	for _, e := range evs {
		if err := p.state.Apply(e); err != nil {
			s.d.Log.Warn("skipping bad event", "id", e.ID, "err", err)
		}
	}
	s.projects[cfg.Name] = p
	s.mu.Unlock()
	// Items this machine held before a restart: their leases must be re-read.
	for _, it := range p.state.Items {
		if it.Claim != nil && it.Claim.Machine == s.d.Machine && !it.Status.Terminal() && pp.Arbiter != nil {
			if info, err := pp.Arbiter.Inspect(ctx, claimKey(it.ID)); err == nil && info.Held && info.Holder == s.d.Machine {
				p.leases[it.ID] = ports.Lease{Key: claimKey(it.ID), Holder: s.d.Machine, Version: info.Version, Renewed: info.Renewed}
			}
		}
	}
	if s.d.Knowledge != nil {
		_ = s.d.Knowledge.IndexItems(ctx, cfg.Name, p.state.ItemList(), p.state.ProposalList())
	}
	return p, nil
}

// ReloadConfig swaps a project's configuration.
func (s *Service) ReloadConfig(name string, cfg *domain.ProjectConfig) error {
	if errs := cfg.Validate(); len(errs) > 0 {
		return errors.Join(errs...)
	}
	p, err := s.Project(name)
	if err != nil {
		return err
	}
	p.cfgV.Store(cfg)
	return nil
}

// Project returns a project by name; "" picks the only project.
func (s *Service) Project(name string) (*Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if name == "" && len(s.projects) == 1 {
		for _, p := range s.projects {
			return p, nil
		}
	}
	p, ok := s.projects[name]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownProj, name)
	}
	return p, nil
}

// Projects lists project names.
func (s *Service) Projects() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for n := range s.projects {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Cfg returns the project's configuration (swapped atomically on reload).
func (p *Project) Cfg() *domain.ProjectConfig { return p.cfgV.Load() }

// Ports returns the project's adapters (swapped atomically on reload).
func (p *Project) Ports() *ProjectPorts { return p.portsV.Load() }

// SetPorts replaces the project's adapters.
func (p *Project) SetPorts(pp ProjectPorts) { p.portsV.Store(&pp) }

// Snapshot returns a deep-enough copy of the state for reading.
func (p *Project) Snapshot() *domain.State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return cloneState(p.state)
}

func cloneState(s *domain.State) *domain.State {
	c := *s
	c.Discussions = make(map[string][]*domain.DiscussionMessage, len(s.Discussions))
	for k, v := range s.Discussions {
		c.Discussions[k] = slices.Clone(v)
	}
	c.Items = make(map[string]*domain.Item, len(s.Items))
	for k, v := range s.Items {
		c.Items[k] = v.Clone()
	}
	c.Proposals = make(map[string]*domain.Proposal, len(s.Proposals))
	for k, v := range s.Proposals {
		pp := *v
		pp.Comments = append([]string(nil), v.Comments...)
		c.Proposals[k] = &pp
	}
	c.Goals = map[string]*domain.Goal{}
	for k, v := range s.Goals {
		g := *v
		c.Goals[k] = &g
	}
	c.Tweaks = map[string]*domain.Tweak{}
	for k, v := range s.Tweaks {
		t := *v
		c.Tweaks[k] = &t
	}
	c.Questions = map[string]*domain.Question{}
	for k, v := range s.Questions {
		q := *v
		q.Options = append([]domain.Option(nil), v.Options...)
		c.Questions[k] = &q
	}
	c.Accounts = map[string]*domain.Account{}
	for k, v := range s.Accounts {
		a := *v
		c.Accounts[k] = &a
	}
	c.Machines = map[string]*domain.Machine{}
	for k, v := range s.Machines {
		m := *v
		c.Machines[k] = &m
	}
	c.Sessions = map[string]*domain.SessionInfo{}
	for k, v := range s.Sessions {
		ss := *v
		c.Sessions[k] = &ss
	}
	c.Messages = make([]*domain.Message, len(s.Messages))
	for i, m := range s.Messages {
		mm := *m
		c.Messages[i] = &mm
	}
	c.Journal = append([]*domain.JournalEntry(nil), s.Journal...)
	c.Lessons = make(map[string]*domain.Lesson, len(s.Lessons))
	for k, v := range s.Lessons {
		c.Lessons[k] = v.Clone()
	}
	if s.Focus != nil {
		f := *s.Focus
		c.Focus = &f
	}
	c.Seen = nil
	c.Worktrees = domain.CloneWorktrees(s.Worktrees)
	// The ledger grows with the log: it is copied only for who asks (Project.Ledger).
	c.Ledger = nil
	return &c
}

// emitLocked appends an event to the log, applies it and fans it out.
// Callers hold p.mu.
func (s *Service) emitLocked(ctx context.Context, p *Project, typ, subject, actor string, pl domain.Payload) (domain.Event, error) {
	e := domain.NewEvent(s.d.IDs.NewID(), typ, p.Name, subject, s.d.Machine, actor, s.d.Clock.Now(), pl)
	stored, err := s.d.Store.Append(ctx, e)
	if err != nil {
		return e, fmt.Errorf("store event %s: %w", typ, err)
	}
	if len(stored) == 1 {
		e = stored[0]
	}
	if err := p.state.Apply(e); err != nil {
		return e, err
	}
	s.fanout(p, e)
	return e, nil
}

func (s *Service) fanout(p *Project, e domain.Event) {
	s.hub.Publish(e)
	if s.d.Bus != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.d.Bus.Publish(ctx, e); err != nil {
				s.d.Log.Debug("bus publish failed", "type", e.Type, "err", err)
			}
		}()
	}
	if s.d.Knowledge != nil && (e.Type == domain.EvItemCreated || e.Type == domain.EvItemUpdated || e.Type == domain.EvProposalCreated) {
		pl, _ := e.DecodePayload()
		go func() {
			var items []*domain.Item
			var props []*domain.Proposal
			if pl.Item != nil {
				items = append(items, pl.Item)
			}
			if pl.Proposal != nil {
				props = append(props, pl.Proposal)
			}
			_ = s.d.Knowledge.IndexItems(context.Background(), p.Name, items, props)
		}()
	}
}

// emit takes the project lock.
func (s *Service) emit(ctx context.Context, p *Project, typ, subject, actor string, pl domain.Payload) (domain.Event, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return s.emitLocked(ctx, p, typ, subject, actor, pl)
}

// Ingest applies an event replicated from another machine.
func (s *Service) Ingest(ctx context.Context, e domain.Event) error {
	if e.Machine == s.d.Machine {
		return nil
	}
	liveOnly := e.LiveOnly()
	s.mu.RLock()
	p := s.projects[e.Project]
	if p == nil {
		// The project is not loaded here (yet). Keep the event so that
		// AddProject replays it; dropping it would lose it for good, since
		// the replication cursor moves past it.
		defer s.mu.RUnlock()
		if liveOnly || e.Project == "" {
			return nil
		}
		_, err := s.d.Store.Append(ctx, e)
		return err
	}
	s.mu.RUnlock()
	if liveOnly {
		// Live-only state (heartbeats, sessions): applied, never stored.
		p.mu.Lock()
		err := p.state.Apply(e)
		delete(p.state.Seen, e.ID)
		p.mu.Unlock()
		if err == nil {
			s.hub.Publish(e)
			if pl, perr := e.DecodePayload(); perr == nil && pl.MachineI != nil {
				s.telemetry.add(pl.MachineI, e.Time)
			}
		}
		return err
	}
	stored, err := s.d.Store.Append(ctx, e)
	if err != nil {
		return err
	}
	if len(stored) == 0 {
		return nil // already have it
	}
	p.mu.Lock()
	err = p.state.Apply(stored[0])
	p.mu.Unlock()
	if err == nil {
		s.hub.Publish(stored[0])
		s.runtime.Poke()
		s.dropFinishedWorkspace(ctx, p, stored[0])
		if stored[0].Type == domain.EvItemIntegrated {
			s.mainMovedAsync(p, "", []string{stored[0].Subject}, stored[0].Subject+" integrated on "+stored[0].Machine)
		}
	}
	return err
}

// dropFinishedWorkspace removes the local worktree of an item that another
// machine finished (for example after taking it over from this one).
func (s *Service) dropFinishedWorkspace(ctx context.Context, p *Project, e domain.Event) {
	g := p.Ports().Git
	if g == nil {
		return
	}
	pl, err := e.DecodePayload()
	if err != nil || pl.Item == nil || !pl.Item.Status.Terminal() {
		return
	}
	for _, key := range []string{pl.Item.ID, altKey(pl.Item.ID)} {
		s.retireWorktree(ctx, p, key, s.WorktreeDir(p.Name, key), retireRemove)
	}
}

// mutateItem clones an item, applies fn, bumps its version and emits typ.
// Callers hold p.mu.
func (s *Service) mutateItemLocked(ctx context.Context, p *Project, id, typ, actor string, fn func(it *domain.Item) error, extra domain.Payload) (*domain.Item, error) {
	cur, ok := p.state.Items[id]
	if !ok {
		return nil, fmt.Errorf("%w: item %s", ErrNotFound, id)
	}
	it := cur.Clone()
	if err := fn(it); err != nil {
		return nil, err
	}
	it.Version = cur.Version + 1
	it.UpdatedAt = s.d.Clock.Now().UTC()
	extra.Item = it
	if _, err := s.emitLocked(ctx, p, typ, id, actor, extra); err != nil {
		return nil, err
	}
	return it.Clone(), nil
}

func (s *Service) mutateItem(ctx context.Context, p *Project, id, typ, actor string, fn func(it *domain.Item) error, extra domain.Payload) (*domain.Item, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return s.mutateItemLocked(ctx, p, id, typ, actor, fn, extra)
}

// SystemClock is the real clock.
type SystemClock struct{}

// Now returns the current time.
func (SystemClock) Now() time.Time { return time.Now() }

// RandomIDs creates sortable random ids: unix-millis hex + random suffix.
type RandomIDs struct{}

// NewID returns a new unique id.
func (RandomIDs) NewID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%013x-%s", time.Now().UnixMilli(), hex.EncodeToString(b[:]))
}

func claimKey(itemID string) string { return "claims/" + itemID }

// HousekeepingKey is the lease of global duties.
const HousekeepingKey = "lease/housekeeping"
