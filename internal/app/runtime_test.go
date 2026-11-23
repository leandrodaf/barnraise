package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/fakeprovider"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

// idle waits until no session runs on this machine.
func (e *env) idle() {
	e.t.Helper()
	r := e.svc.Runtime()
	eventually(e.t, "sessions to end", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.running) == 0
	})
}

func (e *env) fill() {
	e.t.Helper()
	r := e.svc.Runtime()
	r.fillSlots(e.ctx, e.p, r.machine())
}

// actorOf resolves the session token a provider received.
func (e *env) actorOf(spec ports.SessionSpec) Actor {
	a, ok := e.svc.Tokens().Resolve(spec.Env["BARNRAISE_TOKEN"])
	if !ok {
		e.t.Errorf("session token does not resolve: %v", spec.Env)
	}
	return a
}

func TestRuntimeSessionWithoutDeliveryTwiceBlocks(t *testing.T) {
	e := newEnv(t)
	e.account("fake-1")
	it := e.ready("simple", "lazy agent", "x")
	e.fill()
	e.idle()
	got := e.item(it.ID)
	if got.Attempts != 1 || got.Status != domain.StatusInProgress {
		t.Fatalf("first session without delivery: %+v", got)
	}
	inbox, _ := e.svc.Inbox(e.ctx, "demo", "x", "", it.ID, false)
	if len(inbox) != 1 || !strings.Contains(inbox[0].Text, "ended without a delivery") {
		t.Fatalf("inbox %+v", inbox)
	}
	e.fill()
	e.idle()
	e.wantStatus(it.ID, domain.StatusBlocked, "implement")
	if q := e.question(it.ID); !strings.Contains(q.Context, "ended twice without a delivery") {
		t.Fatal(q.Context)
	}
	if specs := e.prov.Specs(); len(specs) != 2 || specs[1].Env["BARNRAISE_ITEM"] != it.ID {
		t.Fatalf("specs %d", len(specs))
	}
	m, _ := e.svc.Metrics(e.ctx, "demo", owner(), time.Hour)
	if len(m.TokensByRole) == 0 {
		t.Fatal("usage recorded")
	}
	tl, _ := e.svc.Timeline(e.ctx, "demo", owner(), it.ID)
	var journal bool
	for _, te := range tl {
		if te.Type == domain.EvJournal && te.Summary == "ok" {
			journal = true
		}
	}
	if !journal {
		t.Fatal("the session's final text goes to the journal")
	}
}

func TestRuntimeReviewSessionWithoutReviewAsksTheOwner(t *testing.T) {
	e := newEnv(t)
	e.account("fake-1")
	it := e.ready("reviewed", "silent reviewer", "x")
	e.start(it.ID)
	e.mustDeliver(it.ID, domain.Delivery{})
	for range 2 {
		e.fill()
		e.idle()
	}
	got := e.wantStatus(it.ID, domain.StatusWaitingOwner, "review")
	if got.WaitingQuestion == "" {
		t.Fatal("question")
	}
}

func TestRuntimeAgentDelivers(t *testing.T) {
	e := newEnv(t, withGit())
	e.account("fake-1")
	RoleText = func(c *domain.ProjectConfig, role string) (string, error) { return "You are the " + role + ".", nil }
	defer func() { RoleText = nil }()
	e.svc.Runtime().EgressProxy = "127.0.0.1:3128"
	testkit.WriteFile(t, e.root, ".barnraise/constitution.md", "Be kind to users.")
	it := e.ready("simple", "Cards", "cards")
	var mu sync.Mutex
	var seen ports.SessionSpec
	e.prov.Handle(func(ctx context.Context, spec ports.SessionSpec, emit func(ports.ProviderEvent)) (ports.SessionResult, error) {
		mu.Lock()
		seen = spec
		mu.Unlock()
		emit(ports.ProviderEvent{Kind: ports.PEToolUse, Tool: "Read"})
		emit(ports.ProviderEvent{Kind: ports.PEUsage, Usage: &domain.TokenUsage{Input: 10, Output: 5, CacheRead: 3, CacheWrite: 1, CostUSD: 0.1}})
		testkit.WriteFile(t, spec.Workdir, "app/cards.txt", "cards\n")
		a := e.actorOf(spec)
		if _, err := e.svc.Deliver(ctx, "demo", a, domain.Delivery{Status: domain.DeliveryDone, Summary: "cards list",
			Criteria: []domain.CriterionMark{{ID: "C1", Met: true, Evidence: "unit test"}}}); err != nil {
			t.Errorf("deliver: %v", err)
		}
		return ports.SessionResult{Text: "done"}, nil
	})
	e.fill()
	e.idle()
	got := e.wantStatus(it.ID, domain.StatusInProgress, "check")
	if !got.Criteria[0].Met || got.Runs["implement"].Family != "fake" || got.Runs["implement"].Model != "fake-large" {
		t.Fatalf("delivered %+v", got)
	}
	mu.Lock()
	spec := seen
	mu.Unlock()
	if spec.Model != "fake-large" || spec.Account.ID != "fake-1" || spec.Env["BARNRAISE_STEP"] != "implement" || spec.Env["HTTPS_PROXY"] != "http://127.0.0.1:3128" ||
		!strings.Contains(spec.SystemPrompt, "You are the implementer.") || !strings.Contains(spec.SystemPrompt, "Be kind to users.") || !strings.Contains(spec.Prompt, "# Item "+it.ID+": Cards") {
		t.Fatalf("spec %+v", spec)
	}
	var settings map[string]any
	data, err := os.ReadFile(spec.SettingsFile)
	if err != nil || json.Unmarshal(data, &settings) != nil || settings["hooks"] == nil {
		t.Fatalf("settings %s %v", data, err)
	}
	if _, err := os.Stat(spec.MCPConfig); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(spec.Workdir, ".barnraise-session", "policy.json")); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.svc.Tokens().Resolve(spec.Env["BARNRAISE_TOKEN"]); ok {
		t.Fatal("the session token is revoked when the session ends")
	}
	if log := testkit.RunGit(t, e.repo.Root, "--git-dir", e.repo.Remote, "log", "--format=%s", got.Branch); !strings.Contains(log, it.ID+": done step implement") {
		t.Fatalf("the step is committed and pushed: %s", log)
	}
	// One tick drives the check and the merge queue.
	r := e.svc.Runtime()
	e.prov.Handle(nil)
	r.Tick(e.ctx)
	eventually(t, "check step", func() bool { return e.item(it.ID).Status == domain.StatusQueued })
	r.Tick(e.ctx)
	eventually(t, "integration", func() bool { return e.item(it.ID).Status == domain.StatusIntegrated })
	if sessions := r.Sessions(); len(sessions) != 0 {
		t.Fatal(sessions)
	}
}

func TestRuntimeLimitMarksTheAccount(t *testing.T) {
	e := newEnv(t)
	e.account("fake-1")
	it := e.ready("simple", "limited", "x")
	resets := e.clock.Now().Add(3 * time.Hour)
	e.prov.Handle(func(ctx context.Context, spec ports.SessionSpec, emit func(ports.ProviderEvent)) (ports.SessionResult, error) {
		emit(ports.ProviderEvent{Kind: ports.PELimit, ResetsAt: resets})
		return ports.SessionResult{Limited: true}, nil
	})
	e.fill()
	e.idle()
	acc := e.p.Snapshot().Accounts["fake-1"]
	if acc.Status != domain.AccountLimited || !acc.Usage.FiveHourResets.Equal(resets) {
		t.Fatalf("account %+v", acc)
	}
	if got := e.item(it.ID); got.Attempts != 0 {
		t.Fatal("a limit is not the agent's fault")
	}
	// No account left: nothing starts and the owner hears about it.
	e.fill()
	e.idle()
	if len(e.prov.Specs()) != 1 {
		t.Fatal("no session without an account")
	}
	if !strings.Contains(strings.Join(e.notes.Kinds(), ","), "accounts_exhausted") {
		t.Fatal(e.notes.Kinds())
	}
	if si := e.p.Snapshot().Sessions["implementer-m1-1"]; si == nil || si.State != "failed" {
		t.Fatalf("session state %+v", si)
	}
}

func TestRuntimeExperimentsShare(t *testing.T) {
	e := newEnv(t, withCfg(func(c *domain.ProjectConfig) {
		c.Experiments = []domain.ExperimentDef{
			{Name: "never", Share: 0, Prompt: "NEVER"},
			{Name: "terse", Share: 100, Role: "implementer", Model: "fake/fake-small", Prompt: "Be terse.", Env: map[string]string{"TERSE": "1"}},
		}
	}))
	e.account("fake-1")
	e.ready("simple", "experiment", "x")
	e.fill()
	e.idle()
	spec := e.prov.Specs()[0]
	if spec.Model != "fake-small" || spec.Env["BARNRAISE_EXPERIMENT"] != "terse" || spec.Env["TERSE"] != "1" || !strings.Contains(spec.SystemPrompt, "Be terse.") || strings.Contains(spec.SystemPrompt, "NEVER") {
		t.Fatalf("spec %+v", spec)
	}
	if !inShare("x", 100) || inShare("x", 0) {
		t.Fatal("inShare")
	}
	if _, ok := parseChoice("no-slash"); ok {
		t.Fatal("parseChoice")
	}
	if c, ok := parseChoice("fake/m@high"); !ok || c.Effort != "high" {
		t.Fatal(c)
	}
}

func TestRuntimePauseStopsSessions(t *testing.T) {
	e := newEnv(t)
	e.account("fake-1")
	it := e.ready("simple", "long", "x")
	started := make(chan struct{})
	e.prov.Handle(func(ctx context.Context, spec ports.SessionSpec, emit func(ports.ProviderEvent)) (ports.SessionResult, error) {
		close(started)
		<-ctx.Done()
		return ports.SessionResult{}, ctx.Err()
	})
	e.fill()
	<-started
	if s := e.svc.Runtime().Sessions(); len(s) != 1 || s[0]["item"] != it.ID {
		t.Fatalf("sessions %v", s)
	}
	if err := e.svc.Pause(e.ctx, "demo", owner(), "stop"); err != nil {
		t.Fatal(err)
	}
	e.idle()
	if got := e.item(it.ID); got.Attempts != 0 || got.Status != domain.StatusInProgress {
		t.Fatalf("a stopped session is not the agent's fault: %+v", got)
	}
	e.svc.Runtime().Tick(e.ctx) // paused: nothing starts
	if len(e.prov.Specs()) != 1 {
		t.Fatal("paused")
	}
}

func TestRuntimeStartFailures(t *testing.T) {
	e := newEnv(t)
	e.account("fake-1")
	it := e.ready("simple", "x", "x")
	e.prov.FailOpen(errors.New("binary missing"))
	e.fill()
	if si := e.p.Snapshot().Sessions["implementer-m1-1"]; si == nil || si.State != "failed" || !strings.Contains(si.Detail, "binary missing") {
		t.Fatalf("session %+v", si)
	}
	if _, busy := e.p.running[it.ID]; busy {
		t.Fatal("the item is free again")
	}
	// One try per pass: the other implementer slots do not repeat a failure
	// that belongs to the machine.
	failed := len(e.prov.Specs())
	if failed != 1 {
		t.Fatalf("the item is tried once per pass, got %d", failed)
	}
	states := len(e.store.Types("implementer-m1-1"))
	e.fill() // the same failure again: tried, but not reported twice
	if got := len(e.prov.Specs()); got != 2 {
		t.Fatalf("the next pass tries again: %d", got)
	}
	if got := len(e.store.Types("implementer-m1-1")); got != states {
		t.Fatalf("a repeated failure must not be recorded again: %d events, want %d", got, states)
	}
	failed = 2
	e.prov.FailOpen(nil)
	RoleText = func(*domain.ProjectConfig, string) (string, error) { return "", errors.New("no role file") }
	e.fill()
	RoleText = nil
	r := e.svc.Runtime()
	r.MaxSessions = -1
	e.fill()
	r.MaxSessions = 0
	if got := len(e.prov.Specs()); got != failed {
		t.Fatalf("no session may open without a role prompt or with sessions off: %d opens, want %d", got, failed)
	}
}

func TestRuntimeRenewLeases(t *testing.T) {
	e := newEnv(t)
	a := e.ready("simple", "a", "x")
	b := e.ready("simple", "b", "x")
	e.start(a.ID)
	e.start(b.ID)
	r := e.svc.Runtime()
	before := e.p.leases[a.ID].Version
	r.renewLeases(e.ctx, e.p)
	if e.p.leases[a.ID].Version == before {
		t.Fatal("renewed")
	}
	// Another machine took b: the lease is dropped here.
	other := e.backend.Handle(testkit.NewClock())
	l := e.p.leases[b.ID]
	if err := e.arb.Release(e.ctx, l); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Claim(e.ctx, claimKey(b.ID), "m2"); err != nil {
		t.Fatal(err)
	}
	r.lastRenew = map[string]time.Time{}
	r.renewLeases(e.ctx, e.p)
	if _, ok := e.p.leases[b.ID]; ok {
		t.Fatal("a lost lease is dropped")
	}
	e.backend.SetOffline(true)
	r.lastRenew = map[string]time.Time{}
	r.renewLeases(e.ctx, e.p)
	if _, ok := e.p.leases[a.ID]; !ok {
		t.Fatal("offline keeps the lease")
	}
	e.backend.SetOffline(false)
	r.renewLeases(e.ctx, e.p) // too soon: nothing
	none := newEnv(t, withoutArbiter())
	none.svc.Runtime().renewLeases(none.ctx, none.p)
}

func TestRuntimeRunAndHousekeeping(t *testing.T) {
	e := newEnv(t, withTracker())
	e.account("fake-1")
	if _, err := e.svc.Propose(e.ctx, "demo", owner(), domain.Proposal{Title: "Feature idea", Problem: "p", Evidence: "e", Impact: "i", Size: "1h", Area: "a", GoalLink: "g", Kind: "feature"}); err != nil {
		t.Fatal(err)
	}
	r := e.svc.Runtime()
	r.MaxSessions = -1
	r.SetProbe(func() domain.Machine { return domain.Machine{CPUs: 8, Slots: 2, Labels: []string{"gpu-less"}} })
	if r.tickEvery() != time.Second {
		t.Fatal(r.tickEvery())
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	r.lastDigest["demo"], r.lastWeekly["demo"] = old, old
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	eventually(t, "heartbeat", func() bool {
		m := e.p.Snapshot().Machines["m1"]
		return m != nil && m.Version == "test" && len(m.Accounts) == 1 && m.CPUs == 8
	})
	eventually(t, "daily and weekly summaries", func() bool {
		var digest, weekly bool
		for _, n := range e.notes.Sent() {
			digest = digest || strings.Contains(n.Title, "daily summary")
			weekly = weekly || strings.Contains(n.Title, "proposed features this week")
		}
		return digest && weekly
	})
	if e.backend.Holder(HousekeepingKey) != "m1" {
		t.Fatal("housekeeping lease")
	}
	r.Poke()
	r.Poke()
	cancel()
	<-done
	if !r.stopped {
		t.Fatal("Run stops every session at the end")
	}
}

func TestWeeklyTurnsAlertsIntoProposals(t *testing.T) {
	e := newEnv(t)
	if err := e.svc.RecordViolation(e.ctx, "demo", "a", "", "Bash", "r", "x", "sudo"); err != nil {
		t.Fatal(err)
	}
	e.svc.weekly(e.ctx, e.p)
	props, _, _ := e.svc.ListProposals("demo", owner(), false)
	if len(props) != 1 || props[0].Kind != "self_improvement" || !strings.HasPrefix(props[0].Title, "Metric: ") {
		t.Fatalf("%+v", props)
	}
	if n := e.notes.Sent(); len(n) == 0 || !strings.Contains(n[len(n)-1].Title, "proposed features") {
		t.Fatalf("self-improvement proposals go to the owner weekly: %+v", n)
	}
	e.svc.d.Notifier = nil
	e.svc.weekly(e.ctx, e.p)
	e.svc.sendDigest(e.ctx, e.p)
	e.svc.digestNote(e.p, "x")
	e.svc.notifyOwner(e.ctx, e.p, "question", "x", "y")
	e.svc.notifyOwnerQuestion(e.ctx, e.p, &domain.Question{})
}

// A review must run on another model. With a single model configured that
// can never happen, so the owner is told once instead of the item waiting
// in silence.
func TestRuntimeTellsTheOwnerWhenNoOtherModelCanReview(t *testing.T) {
	e := newEnv(t, withCfg(func(c *domain.ProjectConfig) {
		for class := range c.Models.Classes {
			c.Models.Classes[class] = []domain.ModelChoice{{Provider: "fake", Model: "fake-large"}}
		}
	}))
	e.account("fake-1")
	it := e.ready("reviewed", "one model only", "x")
	e.start(it.ID)
	e.mustDeliver(it.ID, domain.Delivery{})
	e.wantStatus(it.ID, domain.StatusInProgress, "review")
	e.fill()
	e.fill()
	var told []string
	for _, n := range e.notes.Sent() {
		if n.Kind == "model_rule" {
			told = append(told, n.Body)
		}
	}
	if len(told) != 1 {
		t.Fatalf("the owner is told once, got %d: %v", len(told), e.notes.Kinds())
	}
	for _, want := range []string{"step review of " + it.ID, "different model from step implement", "fake-large", "models.classes"} {
		if !strings.Contains(told[0], want) {
			t.Fatalf("the message must say what to do; %q lacks %q", told[0], want)
		}
	}
	if len(e.prov.Specs()) != 0 {
		t.Fatal("no session may review with the model that did the work")
	}
}

// A session gets Barnraise's MCP server and only the servers the owner
// allowed for that role in barnraise.yaml.
func TestRuntimeSessionGetsOnlyTheAllowedMCPServers(t *testing.T) {
	e := newEnv(t, withCfg(func(c *domain.ProjectConfig) {
		c.MCP = map[string]domain.MCPServerDef{
			"figma":   {Command: "figma-mcp", Args: []string{"--stdio"}, Env: map[string]string{"FIGMA_TOKEN": "keychain:figma", "MODE": "read"}, Roles: []string{"implementer"}},
			"docs":    {URL: "https://docs.example/mcp"},
			"billing": {Command: "billing-mcp", Roles: []string{"reviewer"}},
		}
	}))
	e.svc.d.ResolveSecret = func(ref string) (string, error) {
		if ref == "keychain:figma" {
			return "s3cret", nil
		}
		return "", errors.New("unknown " + ref)
	}
	e.account("fake-1")
	e.ready("simple", "needs figma", "x")
	e.fill()
	specs := e.prov.Specs()
	if len(specs) != 1 {
		t.Fatalf("specs %d", len(specs))
	}
	data, err := os.ReadFile(specs[0].MCPConfig)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Servers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			URL     string            `json:"url"`
			Type    string            `json:"type"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Servers) != 3 || doc.Servers["barnraise"].Command == "" {
		t.Fatalf("the implementer gets barnraise, figma and docs: %s", data)
	}
	if _, ok := doc.Servers["billing"]; ok {
		t.Fatal("a server allowed for the reviewer must not reach the implementer")
	}
	f := doc.Servers["figma"]
	if f.Command != "figma-mcp" || len(f.Args) != 1 || f.Env["FIGMA_TOKEN"] != "s3cret" || f.Env["MODE"] != "read" {
		t.Fatalf("figma %+v", f)
	}
	if d := doc.Servers["docs"]; d.URL != "https://docs.example/mcp" || d.Type != "http" {
		t.Fatalf("docs %+v", d)
	}
	e.idle()

	// A secret that cannot be read stops the session instead of starting it half-configured.
	bad := newEnv(t, withCfg(func(c *domain.ProjectConfig) {
		c.MCP = map[string]domain.MCPServerDef{"figma": {Command: "figma-mcp", Env: map[string]string{"FIGMA_TOKEN": "keychain:missing"}}}
	}))
	bad.svc.d.ResolveSecret = func(ref string) (string, error) { return "", errors.New("not in the keychain") }
	bad.account("fake-1")
	bad.ready("simple", "no secret", "x")
	bad.fill()
	if len(bad.prov.Specs()) != 0 {
		t.Fatal("no session without the server's secret")
	}
}

// Codex on Linux runs every command inside bubblewrap. Where bubblewrap
// cannot start, Codex gets no work (it would fail every command and spend
// tokens doing it), and the owner is told why once.
func TestRuntimeGivesNoWorkToAProviderWhoseSandboxCannotRun(t *testing.T) {
	onlyCodex := withCfg(func(c *domain.ProjectConfig) {
		for class := range c.Models.Classes {
			c.Models.Classes[class] = []domain.ModelChoice{{Provider: "codex", Model: "gpt"}, {Provider: "fake", Model: "fake-large"}}
		}
	})
	e := newEnv(t, onlyCodex)
	codex := fakeprovider.New("codex", "openai")
	e.svc.d.Providers["codex"] = codex
	e.svc.Runtime().NoSandboxProviders = map[string]string{"codex": codexNeedsBwrap}
	if _, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: "codex-1", Provider: "codex", CeilingPct: 100}); err != nil {
		t.Fatal(err)
	}
	it := e.ready("simple", "codex only", "x")
	e.fill()
	e.fill()
	if len(codex.Specs()) != 0 || len(e.prov.Specs()) != 0 {
		t.Fatal("no session may start: codex cannot run here and no other account exists")
	}
	var told []string
	for _, n := range e.notes.Sent() {
		if strings.Contains(n.Body, "bubblewrap") {
			told = append(told, n.Body)
		}
	}
	if len(told) != 1 || !strings.Contains(told[0], "apparmor_restrict_unprivileged_userns") {
		t.Fatalf("the owner is told once, with the fix: %v", told)
	}
	// With another provider's account the work goes there instead.
	e.account("fake-1")
	e.fill()
	if len(codex.Specs()) != 0 || len(e.prov.Specs()) != 1 || e.prov.Specs()[0].Env["BARNRAISE_ITEM"] != it.ID {
		t.Fatalf("the item goes to the provider that can run: codex %d fake %d", len(codex.Specs()), len(e.prov.Specs()))
	}
	e.idle()

	// Where the sandbox works, Codex is used normally.
	ok := newEnv(t, onlyCodex)
	codex2 := fakeprovider.New("codex", "openai")
	ok.svc.d.Providers["codex"] = codex2
	ok.svc.Runtime().NoSandboxProviders = nil
	if _, err := ok.svc.UpsertAccount(ok.ctx, "demo", owner(), domain.Account{ID: "codex-1", Provider: "codex", CeilingPct: 100}); err != nil {
		t.Fatal(err)
	}
	ok.ready("simple", "codex fine", "x")
	ok.fill()
	if len(codex2.Specs()) != 1 {
		t.Fatalf("codex sessions %d", len(codex2.Specs()))
	}
	ok.idle()
}
