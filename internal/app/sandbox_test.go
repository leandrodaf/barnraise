package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/leandrodaf/barnraise/internal/adapters/out/fakeprovider"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// fakeConfiner records what it was asked to confine.
type fakeConfiner struct {
	backend string
	ok      bool
	mu      sync.Mutex
	confs   []domain.Confinement
}

func (f *fakeConfiner) Support() domain.SandboxSupport {
	s := domain.SandboxSupport{Backend: f.backend, Available: f.ok, ABI: 6, Network: true}
	if !f.ok {
		s.Reason = "not here"
	}
	return s
}

func (f *fakeConfiner) Wrap(c domain.Confinement, name string, args []string) (string, []string, error) {
	f.mu.Lock()
	f.confs = append(f.confs, c)
	f.mu.Unlock()
	return "confined-" + f.backend, append([]string{c.Profile, "--", name}, args...), nil
}

func (f *fakeConfiner) last(t *testing.T) domain.Confinement {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.confs) == 0 {
		t.Fatal("nothing was confined")
	}
	return f.confs[len(f.confs)-1]
}

func landlockOK() *fakeConfiner { return &fakeConfiner{backend: "landlock", ok: true} }

// worktree.setup runs inside the setup profile when Landlock works here,
// and unconfined when the project says none.
func TestWorktreeSetupRunsConfined(t *testing.T) {
	ll := landlockOK()
	e := newEnv(t, withGit(), withExec(), withIsolation(Isolation{Landlock: ll}),
		withCfg(func(c *domain.ProjectConfig) { c.WorktreeSetup = "npm ci" }))
	it := e.ready("simple", "needs its dependencies", "x")
	e.start(it.ID)
	dir := e.workspace(it.ID, map[string]string{"a.txt": "a\n"})
	var setup *ports.Cmd
	for _, c := range e.exec.Cmds() {
		if strings.Contains(strings.Join(c.Args, " "), "npm ci") {
			setup = &c
		}
	}
	if setup == nil || setup.Name != "confined-landlock" || setup.Args[0] != domain.SandboxSetup || setup.Args[2] != "sh" {
		t.Fatalf("setup must run through the confiner with the setup profile: %+v", setup)
	}
	conf := ll.last(t)
	if conf.Work[0] != dir || !domain.Contains(conf.Work, filepath.Join(e.cfg.Root, ".git")) {
		t.Fatalf("the worktree and the repository's .git are the work paths: %v", conf.Work)
	}
	if conf.Network != domain.NetRegistries || !strings.Contains(conf.Command, "npm ci") {
		t.Fatalf("setup reaches the registries: %+v", conf)
	}

	e2 := newEnv(t, withGit(), withExec(), withIsolation(Isolation{Landlock: landlockOK()}),
		withCfg(func(c *domain.ProjectConfig) { c.WorktreeSetup = "npm ci"; c.Sandbox.Setup = domain.SandboxNone }))
	it2 := e2.ready("simple", "unconfined setup", "x")
	e2.start(it2.ID)
	e2.workspace(it2.ID, nil)
	for _, c := range e2.exec.Cmds() {
		if strings.Contains(strings.Join(c.Args, " "), "npm ci") && c.Name != "sh" {
			t.Fatalf("sandbox none runs the setup as before: %+v", c)
		}
	}
}

// The project's shared stores reach a confined setup: their variables pass
// through and their directories are writable, like the worktree.
func TestConfinedSetupWritesTheStores(t *testing.T) {
	ll := landlockOK()
	e := newEnv(t, withGit(), withExec(), withIsolation(Isolation{Landlock: ll}),
		withCfg(func(c *domain.ProjectConfig) {
			c.WorktreeSetup = "pnpm install"
			c.WorktreeStores = map[string]string{"pnpm": "npm_config_store_dir"}
		}))
	it := e.ready("simple", "needs its dependencies", "x")
	e.start(it.ID)
	e.workspace(it.ID, nil)
	conf := ll.last(t)
	store := e.svc.storeDir(e.cfg.Name, "pnpm")
	if !domain.Contains(conf.Work, store) || !domain.Contains(conf.Env, "npm_config_store_dir") {
		t.Fatalf("the store must be writable and its variable passed: work %v env %v", conf.Work, conf.Env)
	}
	var setup *ports.Cmd
	for _, c := range e.exec.Cmds() {
		if strings.Contains(strings.Join(c.Args, " "), "pnpm install") {
			setup = &c
		}
	}
	if setup == nil || !domain.Contains(setup.Env, "npm_config_store_dir="+store) {
		t.Fatalf("setup gets the store's path: %+v", setup)
	}
}

// A check is confined by its profile; with the egress proxy it keeps the
// network to loopback and the proxy, and gets the proxy's variables.
func TestCheckConfine(t *testing.T) {
	ll := landlockOK()
	e := newEnv(t, withIsolation(Isolation{Landlock: ll}))
	dir := t.TempDir()
	cf, extra, err := e.svc.CheckConfine("demo", domain.CheckDef{Name: "test", Run: "go test ./..."}, dir, map[string]string{"FOO": "1"})
	if err != nil || cf == nil || cf.Backend != "landlock" || len(extra) != 0 {
		t.Fatalf("confine: %+v %v %v", cf, extra, err)
	}
	name, args, err := cf.Apply("sh", []string{"-c", "go test ./..."})
	if err != nil || name != "confined-landlock" || args[0] != domain.SandboxCheck {
		t.Fatalf("wrap: %s %v %v", name, args, err)
	}
	conf := ll.last(t)
	if conf.Network != domain.NetRegistries || !domain.Contains(conf.Env, "FOO") || !domain.Contains(conf.Env, "BARNRAISE_ITEM") {
		t.Fatalf("without the proxy a check keeps the web ports, and names its variables: %+v", conf)
	}

	e.svc.Runtime().EgressProxy = "127.0.0.1:7421"
	cf, extra, err = e.svc.CheckConfine("demo", domain.CheckDef{Name: "test", Run: "go test ./..."}, dir, nil)
	if err != nil || extra["HTTPS_PROXY"] != "http://127.0.0.1:7421" {
		t.Fatalf("the proxy goes to the check: %v %v", extra, err)
	}
	if _, _, err := cf.Apply("sh", nil); err != nil {
		t.Fatal(err)
	}
	if conf := ll.last(t); conf.Network != domain.NetLocal || len(conf.Connect) != 1 || conf.Connect[0] != 7421 || len(conf.Hosts) != 0 {
		t.Fatalf("with the proxy: loopback and the proxy only: %+v", conf)
	}

	if cf, _, err := e.svc.CheckConfine("demo", domain.CheckDef{Name: "raw", Run: "x", Sandbox: domain.SandboxNone}, dir, nil); cf != nil || err != nil {
		t.Fatalf("sandbox none: %+v %v", cf, err)
	}
	if _, _, err := e.svc.CheckConfine("nope", domain.CheckDef{}, dir, nil); err == nil {
		t.Fatal("unknown project")
	}
	if _, _, err := e.svc.CheckConfine("demo", domain.CheckDef{Sandbox: "bogus"}, dir, nil); err == nil {
		t.Fatal("unknown profile")
	}
	// Nothing to confine with: as before.
	plain := newEnv(t)
	if cf, _, err := plain.svc.CheckConfine("demo", domain.CheckDef{Run: "x"}, dir, nil); cf != nil || err != nil {
		t.Fatalf("no confiner: %+v %v", cf, err)
	}
}

// On a machine where bubblewrap cannot start, Codex works confined by
// Landlock instead of getting no work.
func TestCodexRunsUnderLandlockWhereBubblewrapCannot(t *testing.T) {
	ll := landlockOK()
	onlyCodex := withCfg(func(c *domain.ProjectConfig) {
		for class := range c.Models.Classes {
			c.Models.Classes[class] = []domain.ModelChoice{{Provider: "codex", Model: "gpt"}}
		}
	})
	e := newEnv(t, onlyCodex, withIsolation(Isolation{Landlock: ll}))
	if _, blocked := e.svc.Runtime().NoSandboxProviders["codex"]; blocked {
		t.Fatal("with Landlock, codex must not be refused")
	}
	codex := fakeprovider.New("codex", "openai")
	e.svc.d.Providers["codex"] = codex
	hold := make(chan struct{})
	defer close(hold)
	codex.Handle(func(ctx context.Context, _ ports.SessionSpec, _ func(ports.ProviderEvent)) (ports.SessionResult, error) {
		select {
		case <-hold:
		case <-ctx.Done():
		}
		return ports.SessionResult{}, nil
	})
	if _, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: "codex-1", Provider: "codex", CeilingPct: 100, ConfigDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	e.ready("simple", "codex under landlock", "x")
	e.fill()
	specs := codex.Specs()
	if len(specs) != 1 || specs[0].Confine == nil || specs[0].Confine.Backend != "landlock" {
		t.Fatalf("the codex session runs confined: %+v", specs)
	}
	name, args, err := specs[0].Confine.Apply("codex", []string{"exec"})
	if err != nil || name != "confined-landlock" || args[0] != domain.SandboxAgent {
		t.Fatalf("wrap: %s %v %v", name, args, err)
	}
	conf := ll.last(t)
	if conf.Work[0] != specs[0].Workdir || !domain.Contains(conf.Work, specs[0].Account.ConfigDir) || !domain.Contains(conf.Env, "BARNRAISE_TOKEN") {
		t.Fatalf("worktree, account and variables: %+v", conf)
	}
	pol, err := os.ReadFile(filepath.Join(specs[0].Workdir, ".barnraise-session", "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p domain.Policy
	if err := json.Unmarshal(pol, &p); err != nil || !domain.Contains(p.DenyCommands, domain.AgentDenyCommands()[0]) {
		t.Fatalf("the agent profile's command rules reach the hook: %v", err)
	}
}

// Where bubblewrap works, sessions keep the provider's own sandbox; the
// microVM, when it is on, wraps them anyway and turns Claude's sandbox off.
func TestSessionConfineChoosesTheLayer(t *testing.T) {
	ll := landlockOK()
	e := newEnv(t, withIsolation(Isolation{Landlock: ll}))
	e.svc.Runtime().Sandbox = true
	e.account("fake-1")
	e.holdSessions()
	e.ready("simple", "native sandbox", "x")
	e.fill()
	if s := e.prov.Specs(); len(s) != 1 || s[0].Confine != nil {
		t.Fatalf("bubblewrap works: no extra wrapper: %+v", s)
	}

	vm := &fakeConfiner{backend: "microvm", ok: true}
	e2 := newEnv(t, withIsolation(Isolation{Landlock: landlockOK(), MicroVM: vm, MicroVMFor: func(k string) bool { return k == ConfineSessions }}))
	e2.svc.Runtime().Sandbox = true
	e2.account("fake-1")
	e2.holdSessions()
	e2.ready("simple", "in a VM", "x")
	e2.fill()
	s := e2.prov.Specs()
	if len(s) != 1 || s[0].Confine == nil || s[0].Confine.Backend != "microvm" {
		t.Fatalf("the microVM wraps the session: %+v", s)
	}
	if _, _, err := s[0].Confine.Apply("claude", nil); err != nil {
		t.Fatal(err)
	}
	if conf := vm.last(t); !domain.Contains(conf.Hosts, "api.anthropic.com") {
		t.Fatalf("the VM may reach the provider's API: %v", conf.Hosts)
	}
	settings, _ := os.ReadFile(s[0].SettingsFile)
	if strings.Contains(string(settings), `"sandbox"`) {
		t.Fatal("no bubblewrap sandbox inside the VM")
	}
	// The VM is not wanted for checks here, and an unavailable VM falls back.
	if cf, _ := e2.svc.confiner(ConfineChecks); cf != e2.svc.d.Isolation.Landlock {
		t.Fatal("checks use Landlock")
	}
	down := &fakeConfiner{backend: "microvm"}
	e3 := newEnv(t, withIsolation(Isolation{Landlock: landlockOK(), MicroVM: down, MicroVMFor: func(string) bool { return true }}))
	if _, b := e3.svc.confiner(ConfineSessions); b != "landlock" {
		t.Fatalf("an unavailable VM falls back to Landlock: %q", b)
	}
	if got := e3.svc.SandboxSupport(); len(got) != 2 || got[1].Available {
		t.Fatalf("support: %+v", got)
	}
}

// A machine with neither bubblewrap nor Landlock says so in the status.
func TestStatusAlertsWithoutSandbox(t *testing.T) {
	e := newEnv(t, withIsolation(Isolation{Landlock: &fakeConfiner{backend: "landlock"}}))
	st, err := e.svc.GetStatus(e.ctx, "demo", owner())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(st.Alerts, "\n"), "has no sandbox") {
		t.Fatalf("alerts: %v", st.Alerts)
	}
	ok := newEnv(t, withIsolation(Isolation{Landlock: landlockOK()}))
	st, _ = ok.svc.GetStatus(ok.ctx, "demo", owner())
	if strings.Contains(strings.Join(st.Alerts, "\n"), "sandbox") {
		t.Fatalf("Landlock works: no alert: %v", st.Alerts)
	}
}

func TestHostOf(t *testing.T) {
	for in, want := range map[string]string{"https://openrouter.ai/api/v1": "openrouter.ai", "http://h:8080/x": "h", "plain": "plain"} {
		if got := hostOf(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
	r := &Runtime{EgressProxy: "bad"}
	if r.proxyPort() != 0 {
		t.Fatal("bad proxy address")
	}
}
