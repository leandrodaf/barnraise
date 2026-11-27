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

// holdSessions makes every session wait until the returned function is
// called. A fake session that ends at once lets the next free slot pick the
// same item again within one pass; how often that happens depends on the
// machine's speed, so tests that count sessions hold them instead.
func (e *env) holdSessions() (release func()) {
	ch := make(chan struct{})
	var once sync.Once
	e.prov.Handle(func(ctx context.Context, _ ports.SessionSpec, _ func(ports.ProviderEvent)) (ports.SessionResult, error) {
		select {
		case <-ch:
		case <-ctx.Done():
		}
		return ports.SessionResult{}, nil
	})
	release = func() { once.Do(func() { close(ch) }) }
	e.t.Cleanup(release)
	return release
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
	if log := testkit.RunGit(t, e.repo.Root, "--git-dir", e.repo.Remote, "log", "--format=%s", got.Branch); !strings.Contains(log, "chore("+it.ID+"): done step implement") {
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
	// The violation's own notification is sent in the background and may
	// come after the weekly one: look for the weekly one among them.
	sent := false
	for _, n := range e.notes.Sent() {
		sent = sent || strings.Contains(n.Title, "proposed features")
	}
	if !sent {
		t.Fatalf("self-improvement proposals go to the owner weekly: %+v", e.notes.Sent())
	}
	// Without a notification channel nothing of this fails.
	q := newEnv(t, withoutNotifier())
	q.svc.weekly(q.ctx, q.p)
	q.svc.sendDigest(q.ctx, q.p)
	q.svc.digestNote(q.p, "x")
	q.svc.notifyOwner(q.ctx, q.p, "question", "x", "y")
	q.svc.notifyOwnerQuestion(q.ctx, q.p, &domain.Question{})
}

// A review prefers another model but never waits for one: with a single
// model configured, the review runs on it (a fresh session), and nobody is
// asked about it. The owner chose a strong reviewer over a stalled item.
func TestRuntimeReviewsWithTheSameModelWhenItIsTheOnlyOne(t *testing.T) {
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
	release := e.holdSessions()
	e.fill()
	specs := e.prov.Specs()
	if len(specs) != 1 || specs[0].Model != "fake-large" || specs[0].Role != "reviewer" {
		t.Fatalf("the review runs on the only model: %+v", specs)
	}
	for _, n := range e.notes.Sent() {
		if n.Kind == "model_rule" {
			t.Fatalf("nobody is asked about it: %v", n)
		}
	}
	e.svc.Runtime().MaxSessions = -1
	release()
	e.idle()
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
	release := e.holdSessions()
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
	e.svc.Runtime().MaxSessions = -1
	release()
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
	release := e.holdSessions()
	e.fill()
	if len(codex.Specs()) != 0 || len(e.prov.Specs()) != 1 || e.prov.Specs()[0].Env["BARNRAISE_ITEM"] != it.ID {
		t.Fatalf("the item goes to the provider that can run: codex %d fake %d", len(codex.Specs()), len(e.prov.Specs()))
	}
	e.svc.Runtime().MaxSessions = -1
	release()
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
	hold := make(chan struct{})
	codex2.Handle(func(ctx context.Context, _ ports.SessionSpec, _ func(ports.ProviderEvent)) (ports.SessionResult, error) {
		select {
		case <-hold:
		case <-ctx.Done():
		}
		return ports.SessionResult{}, nil
	})
	ok.fill()
	if len(codex2.Specs()) != 1 {
		t.Fatalf("codex sessions %d", len(codex2.Specs()))
	}
	ok.svc.Runtime().MaxSessions = -1
	close(hold)
	ok.idle()
}

// With container: in barnraise.yaml a session runs inside a container that
// sees the worktree, the repository's .git, the account, the socket and the
// binary — and the GPU of the machine unless the project says otherwise.
func TestRuntimeSessionInAContainer(t *testing.T) {
	old := GPUProbe
	GPUProbe = func(string) (bool, string, string) { return true, "/dev/dri", "992" }
	oldKVM := KVMProbe
	KVMProbe = func() (string, string) { return "/dev/kvm", "993" }
	defer func() { GPUProbe, KVMProbe = old, oldKVM }()
	acct := t.TempDir()
	cache := t.TempDir()
	e := newEnv(t, withGit(), withCfg(func(c *domain.ProjectConfig) {
		c.Container = domain.ContainerDef{Image: "agent:test", Network: "host", Mounts: []string{cache + ":ro"}}
	}))
	if _, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: "fake-1", Provider: "fake", CeilingPct: 100, ConfigDir: acct}); err != nil {
		t.Fatal(err)
	}
	e.svc.Runtime().Sandbox = true // a working bubblewrap on the host must not be nested in the container
	it := e.ready("simple", "in a box", "x")
	release := e.holdSessions()
	e.fill()
	specs := e.prov.Specs()
	if len(specs) != 1 || specs[0].Container == nil {
		t.Fatalf("specs %+v", specs)
	}
	c := specs[0].Container
	wt := e.svc.WorktreeDir("demo", it.ID)
	if c.Engine != "docker" || c.Image != "agent:test" || c.Network != "host" || !strings.Contains(c.User, ":") || !strings.HasPrefix(c.Home, wt) {
		t.Fatalf("container %+v", c)
	}
	for _, want := range []string{wt, filepath.Join(e.root, ".git"), acct, cache + ":ro"} {
		if !contains(c.Mounts, want) {
			t.Fatalf("mounts %v lack %s", c.Mounts, want)
		}
	}
	for _, m := range c.Mounts {
		if strings.HasPrefix(m, e.svc.d.DataDir) && !strings.HasPrefix(m, wt) && !strings.HasPrefix(m, e.svc.d.SocketPath) {
			t.Fatalf("the daemon's state must not be mounted: %s", m)
		}
	}
	if got := strings.Join(c.Extra, " "); got != "--gpus all --device /dev/dri --group-add 992 --device /dev/kvm --group-add 993" {
		t.Fatalf("gpu args %q", got)
	}
	data, _ := os.ReadFile(specs[0].SettingsFile)
	if strings.Contains(string(data), `"sandbox"`) {
		t.Fatal("the container is the sandbox: the native one is not nested inside it")
	}
	if !strings.Contains(string(data), "bypassPermissions") {
		t.Fatal("agents never wait for a permission prompt")
	}
	e.svc.Runtime().MaxSessions = -1
	release()
	e.idle()

	// gpu: none keeps the GPU out.
	off := newEnv(t, withGit(), withCfg(func(c *domain.ProjectConfig) { c.Container = domain.ContainerDef{Image: "agent:test", GPU: "none"} }))
	off.account("fake-1")
	off.ready("simple", "no gpu", "x")
	offRelease := off.holdSessions()
	off.fill()
	if s := off.prov.Specs(); len(s) != 1 || len(s[0].Container.Extra) != 0 {
		t.Fatalf("gpu none: %+v", s)
	}
	off.svc.Runtime().MaxSessions = -1
	offRelease()
	off.idle()
}

// In a container, a Gemini account that uses the CLI's own login gets only
// ~/.gemini mounted, and the CLI is told where that home is.
func TestRuntimeGeminiOwnLoginInAContainer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0o700); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, withGit(), withCfg(func(c *domain.ProjectConfig) {
		c.Container = domain.ContainerDef{Image: "agent:test", GPU: "none"}
		for class := range c.Models.Classes {
			c.Models.Classes[class] = []domain.ModelChoice{{Provider: "gemini", Model: "gemini-2.5-flash"}}
		}
	}))
	gem := fakeprovider.New("gemini", "google")
	e.svc.d.Providers["gemini"] = gem
	if _, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: "gemini-1", Provider: "gemini", CeilingPct: 100}); err != nil {
		t.Fatal(err)
	}
	e.ready("simple", "gemini in a box", "x")
	hold := make(chan struct{})
	gem.Handle(func(ctx context.Context, _ ports.SessionSpec, _ func(ports.ProviderEvent)) (ports.SessionResult, error) {
		select {
		case <-hold:
		case <-ctx.Done():
		}
		return ports.SessionResult{}, nil
	})
	e.fill()
	specs := gem.Specs()
	if len(specs) != 1 || specs[0].Container == nil {
		t.Fatalf("specs %+v", specs)
	}
	if specs[0].Env["GEMINI_CLI_HOME"] != home || !contains(specs[0].Container.Mounts, filepath.Join(home, ".gemini")) || contains(specs[0].Container.Mounts, home) {
		t.Fatalf("env %v mounts %v", specs[0].Env["GEMINI_CLI_HOME"], specs[0].Container.Mounts)
	}
	e.svc.Runtime().MaxSessions = -1
	close(hold)
	e.idle()
}

// A headless session reports the account's windows as it goes: the account
// shows its real usage without the status line, and repeats are not recorded.
func TestRuntimeRecordsAccountLimitsReportedByTheSession(t *testing.T) {
	// One implementer: a session that ends at once is not started again by a
	// second slot in the same pass, so the reports can be counted.
	e := newEnv(t, withCfg(func(c *domain.ProjectConfig) {
		r := c.Roles["implementer"]
		r.Count = 1
		c.Roles["implementer"] = r
	}))
	e.account("fake-1")
	e.ready("simple", "limits", "x")
	reset := e.clock.Now().Add(3 * time.Hour).UTC()
	e.prov.Handle(func(_ context.Context, _ ports.SessionSpec, emit func(ports.ProviderEvent)) (ports.SessionResult, error) {
		for i := 0; i < 5; i++ { // one report per request, the same numbers
			emit(ports.ProviderEvent{Kind: ports.PEUsage, Limits: &domain.Usage{FiveHourPct: 42, WeeklyPct: 17, FiveHourResets: reset, Source: "session"}})
		}
		emit(ports.ProviderEvent{Kind: ports.PEUsage, Limits: &domain.Usage{FiveHourPct: 42.4, WeeklyPct: 17}}) // under a point: noise
		emit(ports.ProviderEvent{Kind: ports.PEUsage, Limits: &domain.Usage{FiveHourPct: 45, FiveHourResets: reset}})
		return ports.SessionResult{}, nil
	})
	e.fill()
	e.idle()
	acc := e.p.Snapshot().Accounts["fake-1"]
	if acc.Usage.FiveHourPct != 45 || acc.Usage.WeeklyPct != 17 || !acc.Usage.FiveHourResets.Equal(reset) || acc.Usage.UpdatedAt.IsZero() {
		t.Fatalf("usage %+v", acc.Usage)
	}
	updates := 0
	for _, typ := range e.store.Types("fake-1") {
		if typ == domain.EvAccountUpdated {
			updates++
		}
	}
	// Added, 42%, 45%, and at most one more from the session's own bookkeeping:
	// never one per report (seven were sent).
	if updates < 3 || updates > 4 {
		t.Fatalf("%d account updates; repeated reports must not be recorded", updates)
	}
	// At the ceiling the account stops being offered.
	e.prov.Handle(func(_ context.Context, _ ports.SessionSpec, emit func(ports.ProviderEvent)) (ports.SessionResult, error) {
		emit(ports.ProviderEvent{Kind: ports.PEUsage, Limits: &domain.Usage{FiveHourPct: 100, FiveHourResets: reset}})
		return ports.SessionResult{}, nil
	})
	e.fill()
	e.idle()
	if e.p.Snapshot().Accounts["fake-1"].Available(e.clock.Now()) {
		t.Fatal("an account at 100% of the 5-hour window is not available")
	}
}

// A machine short of memory starts nothing new: the builds and emulators of
// the items in progress keep the memory instead of being killed.
func TestRuntimeHoldsNewSessionsUnderMemoryPressure(t *testing.T) {
	e := newEnv(t)
	e.account("fake-1")
	e.ready("simple", "waits for memory", "x")
	r := e.svc.Runtime()
	release := e.holdSessions()
	m := r.machine()
	m.MemGB, m.FreeMemGB = 16, 1.2
	r.fillSlots(e.ctx, e.p, m)
	if len(e.prov.Specs()) != 0 {
		t.Fatal("no session may start with 1.2 GB available")
	}
	if why := r.underPressure(m); !strings.Contains(why, "1.2 GB of memory available") {
		t.Fatalf("reason %q", why)
	}
	m.FreeMemGB = 9
	r.fillSlots(e.ctx, e.p, m)
	if len(e.prov.Specs()) != 1 {
		t.Fatalf("with memory back the item starts: %d", len(e.prov.Specs()))
	}
	r.MaxSessions = -1
	release()
	e.idle()
	r.MaxSessions = 0
	// Unknown is not pressure, and the owner may turn the floor off.
	m.FreeMemGB = 0
	if why := r.underPressure(m); why != "" {
		t.Fatalf("unknown memory: %q", why)
	}
	r.MinFreeMemGB, m.FreeMemGB = -1, 0.5
	if why := r.underPressure(m); why != "" {
		t.Fatalf("floor off: %q", why)
	}
	// A temporary folder nearly full stops new sessions; unknown does not.
	m.TmpFreeGB = 0.3
	if why := r.underPressure(m); !strings.Contains(why, "temporary folder") {
		t.Fatalf("full temporary folder: %q", why)
	}
	m.TmpFreeGB = 0
	if why := r.underPressure(m); why != "" {
		t.Fatalf("unknown temporary space: %q", why)
	}
}

// Gemini cannot change files outside a container, so an implementer step
// never goes to it there, while a reviewer step may; inside a container it
// implements too.
func TestRuntimeReadOnlyProviderOnlyReviewsOnTheHost(t *testing.T) {
	geminiFirst := withCfg(func(c *domain.ProjectConfig) {
		for class := range c.Models.Classes {
			c.Models.Classes[class] = []domain.ModelChoice{{Provider: "gemini", Model: "gemini-2.5-pro"}, {Provider: "fake", Model: "fake-large"}}
		}
	})
	e := newEnv(t, geminiFirst)
	gem := fakeprovider.New("gemini", "google")
	e.svc.d.Providers["gemini"] = gem
	if _, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: "gemini-1", Provider: "gemini", CeilingPct: 100}); err != nil {
		t.Fatal(err)
	}
	e.account("fake-1")
	release := e.holdSessions()
	it := e.ready("simple", "needs code", "x")
	e.fill()
	if len(gem.Specs()) != 0 {
		t.Fatal("an implementer step went to a provider that cannot change files on the host")
	}
	if len(e.prov.Specs()) != 1 || e.prov.Specs()[0].Env["BARNRAISE_ITEM"] != it.ID {
		t.Fatalf("the step goes to the provider that can implement: fake %d", len(e.prov.Specs()))
	}
	e.svc.Runtime().MaxSessions = -1
	release()
	e.idle()
}

// While a newer binary waits for the running sessions, no new one starts.
func TestDrainingStartsNoSession(t *testing.T) {
	e := newEnv(t)
	e.account("fake-1")
	release := e.holdSessions()
	defer release()
	e.svc.runtime.SetDraining(true)
	e.ready("simple", "Cards screen", "x")
	e.fill()
	if n := len(e.prov.Specs()); n != 0 {
		t.Fatalf("%d sessions started while draining", n)
	}
	e.svc.runtime.SetDraining(false)
	e.fill()
	eventually(t, "a session once the drain ends", func() bool { return len(e.prov.Specs()) == 1 })
}
