package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/fakeprovider"
	"github.com/leandrodaf/barnraise/internal/config"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

func TestMain(m *testing.M) {
	testkit.IsolateGit()
	os.Exit(m.Run())
}

// testYAML is the project used by most tests: the blank template (feature
// and bug flows) plus small flows that isolate one behavior each.
const testYAML = `project: demo
checks:
  main: { run: "true" }
flows:
  simple:
    steps:
      implement: { role: implementer }
      check: { run: checks.main }
      integrate: { run: merge_queue }
  reviewed:
    steps:
      implement: { role: implementer }
      review: { role: reviewer, model_family: different_from(implement) }
      integrate: { run: merge_queue }
    rework:
      limit: { same_model: 1, escalate: 1 }
  post:
    steps:
      draft: { role: implementer }
      owner: { approve: owner }
      integrate: { run: merge_queue }
  planned:
    steps:
      plan: { role: implementer, output: plan.md }
      approval: { approve: owner }
      implement: { role: implementer }
      integrate: { run: merge_queue }
  scripted:
    steps:
      implement: { role: implementer }
      lint: { run: "make lint" }
      integrate: { run: merge_queue }
  approved:
    steps:
      implement: { role: implementer }
      signoff: { approve: analyst }
      integrate: { run: merge_queue }
lease: { renew: 1s, ttl: 3s }
`

type envOpts struct {
	yaml      string
	git       bool
	fakeGit   *testkit.FakeGit
	cfg       []func(*domain.ProjectConfig)
	machine   string
	store     *testkit.Store
	backend   *testkit.ArbiterBackend
	noArbiter bool
	host      bool
	tracker   bool
	triage    bool
	knowledge bool
	exec      bool
	clock     *testkit.Clock
	repo      *testkit.Repo
	files     map[string]string
}

type opt func(*envOpts)

func withGit() opt                              { return func(o *envOpts) { o.git = true } }
func withFakeGit(g *testkit.FakeGit) opt        { return func(o *envOpts) { o.fakeGit = g } }
func withCfg(f func(*domain.ProjectConfig)) opt { return func(o *envOpts) { o.cfg = append(o.cfg, f) } }
func withMachine(m string) opt                  { return func(o *envOpts) { o.machine = m } }
func withStore(s *testkit.Store) opt            { return func(o *envOpts) { o.store = s } }
func withBackend(b *testkit.ArbiterBackend) opt { return func(o *envOpts) { o.backend = b } }
func withoutArbiter() opt                       { return func(o *envOpts) { o.noArbiter = true } }
func withHost() opt                             { return func(o *envOpts) { o.host = true } }
func withTracker() opt                          { return func(o *envOpts) { o.tracker = true } }
func withTriage() opt                           { return func(o *envOpts) { o.triage = true } }
func withKnowledge() opt                        { return func(o *envOpts) { o.knowledge = true } }
func withExec() opt                             { return func(o *envOpts) { o.exec = true } }
func withClock(c *testkit.Clock) opt            { return func(o *envOpts) { o.clock = c } }
func withRepo(r *testkit.Repo) opt              { return func(o *envOpts) { o.repo = r } }
func withFiles(f map[string]string) opt         { return func(o *envOpts) { o.files = f } }

// env is one daemon with one project, wired to fakes.
type env struct {
	t       *testing.T
	ctx     context.Context
	svc     *Service
	p       *Project
	cfg     *domain.ProjectConfig
	clock   *testkit.Clock
	ids     *testkit.IDs
	store   *testkit.Store
	checks  *testkit.Checks
	notes   *testkit.Notifier
	prov    *fakeprovider.Provider
	repo    *testkit.Repo
	fakeGit *testkit.FakeGit
	arb     ports.Arbiter
	backend *testkit.ArbiterBackend
	host    *testkit.CodeHost
	tracker *testkit.Tracker
	triage  *testkit.Triage
	know    *testkit.Knowledge
	exec    *testkit.Exec
	machine string
	root    string
}

func newEnv(t *testing.T, opts ...opt) *env {
	t.Helper()
	o := envOpts{yaml: testYAML, machine: "m1"}
	for _, f := range opts {
		f(&o)
	}
	e := &env{t: t, ctx: context.Background(), machine: o.machine}
	e.clock = o.clock
	if e.clock == nil {
		e.clock = testkit.NewClock()
	}
	e.ids = testkit.NewIDs(o.machine + "-")
	e.store = o.store
	if e.store == nil {
		e.store = testkit.NewStore()
	}
	e.checks = testkit.NewChecks()
	e.notes = &testkit.Notifier{}
	e.prov = fakeprovider.New("fake", "fake")
	pp := ProjectPorts{}
	switch {
	case o.repo != nil:
		e.repo = o.repo
	case o.git:
		e.repo = testkit.NewRepo(t, e.clock, o.files)
	}
	if e.repo != nil {
		pp.Git, pp.Arbiter, e.root = e.repo.Git, e.repo.Arbiter, e.repo.Root
		e.arb = e.repo.Arbiter
	} else {
		e.root = t.TempDir()
		if o.fakeGit != nil {
			e.fakeGit = o.fakeGit
			pp.Git = o.fakeGit
		}
		if !o.noArbiter {
			e.backend = o.backend
			if e.backend == nil {
				e.backend = testkit.NewArbiterBackend()
			}
			e.arb = e.backend.Handle(e.clock)
			pp.Arbiter = e.arb
		}
	}
	if o.host {
		e.host = &testkit.CodeHost{}
		pp.CodeHost = e.host
	}
	if o.tracker {
		e.tracker = &testkit.Tracker{}
		pp.Tracker = e.tracker
	}
	if o.triage {
		e.triage = &testkit.Triage{}
		pp.Triage = e.triage
	}
	cfg, err := config.Compile(e.root, []byte(o.yaml))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	cfg.Models = fakeRouter(cfg.Models)
	for _, f := range o.cfg {
		f(cfg)
	}
	e.cfg = cfg
	deps := Deps{Clock: e.clock, IDs: e.ids, Store: e.store, Checks: e.checks, Notifier: e.notes,
		Providers: map[string]ports.Provider{"fake": e.prov}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Machine: o.machine, Version: "test", DataDir: t.TempDir(), SocketPath: filepath.Join(t.TempDir(), "s.sock"), BinaryPath: "/bin/barnraise"}
	if o.knowledge {
		e.know = &testkit.Knowledge{}
		deps.Knowledge = e.know
	}
	if o.exec {
		e.exec = &testkit.Exec{}
		deps.Exec = e.exec
	}
	e.svc = New(deps)
	e.svc.Runtime().Sandbox = false
	p, err := e.svc.AddProject(e.ctx, cfg, pp)
	if err != nil {
		t.Fatalf("add project: %v", err)
	}
	e.p = p
	t.Cleanup(func() {
		e.svc.Runtime().StopAll()
		// Driver steps started by a tick may still be pushing: let them end
		// before the temporary repository is removed.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			e.p.mu.Lock()
			n := 0
			for _, who := range e.p.running {
				if who == "driver" {
					n++
				}
			}
			e.p.mu.Unlock()
			if n == 0 {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	return e
}

// fakeRouter sends every model class to the fake provider.
func fakeRouter(r domain.ModelRouter) domain.ModelRouter {
	for class := range r.Classes {
		r.Classes[class] = []domain.ModelChoice{{Provider: "fake", Model: "fake-large"}, {Provider: "fake", Model: "fake-small"}}
	}
	r.Classes["cheap"] = []domain.ModelChoice{{Provider: "fake", Model: "fake-small"}, {Provider: "fake", Model: "fake-large"}}
	return r
}

func crit(text string) []domain.Criterion {
	return []domain.Criterion{{Text: text, Check: "main"}}
}

const ears = "WHEN the user opens the screen, THE SYSTEM SHALL show the list"

func owner() Actor { return Owner("ana") }

// agent is an agent actor of a role working on an item step.
func agent(role, item, step string) Actor {
	return Actor{Kind: ActorAgent, Name: role + "-m1-1", Role: role, Project: "demo", ItemID: item, Step: step, Account: "fake-1",
		Scopes: RoleScopes(role, role == "reviewer" || role == "critic"), Family: "fake", Model: "fake-large"}
}

func (e *env) create(in NewItem) *domain.Item {
	e.t.Helper()
	res, err := e.svc.CreateItem(e.ctx, "demo", owner(), in)
	if err != nil {
		e.t.Fatalf("create %q: %v", in.Title, err)
	}
	if res.Item == nil {
		e.t.Fatalf("create %q gave no item: %+v", in.Title, res)
	}
	return res.Item
}

// ready creates a ready item of a kind in an area.
func (e *env) ready(kind, title, area string) *domain.Item {
	e.t.Helper()
	it := e.create(NewItem{Title: title, Kind: kind, Area: area, Criteria: crit(ears), Priority: "P1"})
	if it.Status != domain.StatusReady {
		e.t.Fatalf("%s is %s, want ready", it.ID, it.Status)
	}
	return it
}

func (e *env) item(id string) *domain.Item {
	e.t.Helper()
	it, err := e.svc.GetItem("demo", owner(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return it
}

func slot(role string) Slot {
	return Slot{Agent: role + "-m1-1", Role: role, Machine: domain.Machine{Name: "m1", Labels: []string{"linux"}}}
}

// next asks for work for a role and fails unless it gets want.
func (e *env) next(role, want string) *Assignment {
	e.t.Helper()
	a, err := e.svc.NextWork(e.ctx, "demo", slot(role))
	if err != nil {
		e.t.Fatalf("next work for %s: %v (want %s)", role, err, want)
	}
	if want != "" && a.Item.ID != want {
		e.t.Fatalf("next work for %s = %s, want %s", role, a.Item.ID, want)
	}
	e.svc.doneRunning(e.p, a.Item.ID, "")
	return a
}

// nextBusy is next without freeing the item: its session keeps running.
func (e *env) nextBusy(role, want string) *Assignment {
	e.t.Helper()
	a, err := e.svc.NextWork(e.ctx, "demo", slot(role))
	if err != nil || (want != "" && a.Item.ID != want) {
		e.t.Fatalf("next work for %s = %+v, %v; want %s", role, a, err, want)
	}
	return a
}

// start claims a ready item for an implementer slot, even when other work is held.
func (e *env) start(id string) *Assignment {
	e.t.Helper()
	a, err := e.svc.claim(e.ctx, e.p, id, slot("implementer"))
	if err != nil {
		e.t.Fatalf("claim %s: %v", id, err)
	}
	e.svc.doneRunning(e.p, id, "")
	return a
}

// workspace creates the item's worktree and writes files in it.
func (e *env) workspace(id string, files map[string]string) string {
	e.t.Helper()
	dir, err := e.svc.ensureWorkspace(e.ctx, e.p, id)
	if err != nil {
		e.t.Fatalf("workspace %s: %v", id, err)
	}
	for name, content := range files {
		testkit.WriteFile(e.t, dir, name, content)
	}
	return dir
}

// deliver submits a delivery as the role of the item's current step.
func (e *env) deliver(id string, d domain.Delivery) (*domain.Item, error) {
	cur := e.item(id)
	d.ItemID = id
	if d.Summary == "" {
		d.Summary = "did it"
	}
	if d.Status == "" {
		d.Status = domain.DeliveryDone
	}
	it, err := e.svc.Deliver(e.ctx, "demo", agent("implementer", id, cur.Step), d)
	e.svc.doneRunning(e.p, id, "")
	return it, err
}

func (e *env) mustDeliver(id string, d domain.Delivery) *domain.Item {
	e.t.Helper()
	it, err := e.deliver(id, d)
	if err != nil {
		e.t.Fatalf("deliver %s: %v", id, err)
	}
	return it
}

func approveReview(summary string) domain.Review {
	return domain.Review{Criteria: domain.VerdictPass, Code: domain.VerdictPass, Decision: domain.ReviewApprove, Summary: summary}
}

func reworkReview(text string) domain.Review {
	return domain.Review{Criteria: domain.VerdictFail, Code: domain.VerdictPass, Decision: domain.ReviewRework, Summary: "needs work",
		Findings: []domain.Finding{{Text: text, Severity: "major"}}}
}

// review submits a review as a reviewer of another model family.
func (e *env) review(id string, r domain.Review) (*domain.Item, error) {
	cur := e.item(id)
	r.ItemID = id
	a := agent("reviewer", id, cur.Step)
	a.Family, a.Model = "other", "other-model"
	it, err := e.svc.Review(e.ctx, "demo", a, r)
	e.svc.doneRunning(e.p, id, "")
	return it, err
}

// drive runs the driver step the item is at (check, script, approval, queue entry).
func (e *env) drive(id string) {
	e.t.Helper()
	it := e.item(id)
	f, err := e.cfg.FlowFor(it.Kind)
	if err != nil {
		e.t.Fatal(err)
	}
	st, ok := f.Step(it.Step)
	if !ok {
		e.t.Fatalf("%s has no step %q", id, it.Step)
	}
	if err := e.svc.runDriverStep(e.ctx, e.p, it, st); err != nil {
		e.t.Fatalf("driver step %s of %s: %v", st.Name, id, err)
	}
}

func (e *env) wantStatus(id string, st domain.Status, step string) *domain.Item {
	e.t.Helper()
	it := e.item(id)
	if it.Status != st || (step != "" && it.Step != step) {
		e.t.Fatalf("%s is %s at %q, want %s at %q", id, it.Status, it.Step, st, step)
	}
	return it
}

// eventually polls cond for up to 10 seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func wantErr(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error %v, want %v", err, target)
	}
}

func (e *env) question(id string) *domain.Question {
	e.t.Helper()
	snap := e.p.Snapshot()
	it := snap.Items[id]
	if it == nil || it.WaitingQuestion == "" {
		e.t.Fatalf("%s waits for no question", id)
	}
	q := snap.Questions[it.WaitingQuestion]
	if q == nil {
		e.t.Fatalf("question %s missing", it.WaitingQuestion)
	}
	return q
}

func (e *env) types(subject string) string { return strings.Join(e.store.Types(subject), " ") }

func (e *env) account(id string) {
	e.t.Helper()
	if _, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: id, Provider: "fake", CeilingPct: 100}); err != nil {
		e.t.Fatal(err)
	}
}

var _ = fmt.Sprint
