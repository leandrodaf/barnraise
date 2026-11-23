package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/leandrodaf/barnraise/gen/openapi"
	"github.com/leandrodaf/barnraise/internal/adapters/in/cli"
	"github.com/leandrodaf/barnraise/internal/adapters/in/httpapi"
	"github.com/leandrodaf/barnraise/internal/adapters/in/panel"
	"github.com/leandrodaf/barnraise/internal/adapters/out/checks"
	"github.com/leandrodaf/barnraise/internal/adapters/out/claude"
	"github.com/leandrodaf/barnraise/internal/adapters/out/codex"
	"github.com/leandrodaf/barnraise/internal/adapters/out/egress"
	"github.com/leandrodaf/barnraise/internal/adapters/out/execx"
	gitx "github.com/leandrodaf/barnraise/internal/adapters/out/git"
	"github.com/leandrodaf/barnraise/internal/adapters/out/github"
	"github.com/leandrodaf/barnraise/internal/adapters/out/gitlab"
	"github.com/leandrodaf/barnraise/internal/adapters/out/jira"
	"github.com/leandrodaf/barnraise/internal/adapters/out/keychain"
	"github.com/leandrodaf/barnraise/internal/adapters/out/knowledge"
	"github.com/leandrodaf/barnraise/internal/adapters/out/linear"
	natsx "github.com/leandrodaf/barnraise/internal/adapters/out/nats"
	"github.com/leandrodaf/barnraise/internal/adapters/out/notify"
	"github.com/leandrodaf/barnraise/internal/adapters/out/secretscan"
	"github.com/leandrodaf/barnraise/internal/adapters/out/sqlite"
	"github.com/leandrodaf/barnraise/internal/adapters/out/sysinfo"
	"github.com/leandrodaf/barnraise/internal/adapters/out/tailnet"
	"github.com/leandrodaf/barnraise/internal/adapters/out/webhook"
	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/config"
	"github.com/leandrodaf/barnraise/internal/daemoncfg"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/paths"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// swapBus lets the NATS layer restart (new peers) without rebuilding the service.
type swapBus struct{ v atomic.Pointer[natsx.Bus] }

func (b *swapBus) Publish(ctx context.Context, e domain.Event) error {
	if bus := b.v.Load(); bus != nil {
		return bus.Publish(ctx, e)
	}
	return nil
}

func (b *swapBus) Subscribe(ctx context.Context, project string, fn func(domain.Event)) error {
	if bus := b.v.Load(); bus != nil {
		return bus.Subscribe(ctx, project, fn)
	}
	return errors.New("bus not ready")
}

// daemon holds the wiring.
type daemon struct {
	log     *slog.Logger
	df      *daemoncfg.File
	dfMod   time.Time
	svc     *app.Service
	store   *sqlite.Store
	secrets *keychain.Store
	bus     swapBus
	natsSrv *natsx.Server
	repl    *natsx.Replicator
	blobs   *natsx.ObjectStore
	sender  *webhook.Sender
	mu      sync.Mutex
	roots   map[string]string // project -> root
	runners map[string]*checks.Runner
	machine atomic.Pointer[domain.Machine]
	dataDir string
	opts    cli.DaemonOptions
	pidFile string
	binary  string
	fake    *fakeAgent
}

func runDaemon(ctx context.Context, opts cli.DaemonOptions) error {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	df, err := daemoncfg.Load()
	if err != nil {
		return err
	}
	if opts.Machine != "" {
		df.Machine = opts.Machine
	}
	if opts.NATSPort != 0 {
		df.NATSPort = opts.NATSPort
	}
	if opts.Listen != "" {
		df.Listen = opts.Listen
	}
	d := &daemon{log: log, df: df, dfMod: df.ModTime(), roots: map[string]string{}, runners: map[string]*checks.Runner{}, dataDir: paths.DataDir(), opts: opts}
	if err := os.MkdirAll(d.dataDir, 0o700); err != nil {
		return err
	}
	pidFile := filepath.Join(d.dataDir, "daemon.pid")
	d.pidFile = pidFile
	_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600)
	defer os.Remove(pidFile)
	owner, err := ownerToken()
	if err != nil {
		return err
	}
	if df.NKeySeed == "" {
		seed, pub, err := natsx.NewMachineKey()
		if err != nil {
			return err
		}
		df.NKeySeed, df.NKeyPublic = seed, pub
		_ = df.Save()
		d.dfMod = df.ModTime()
	}

	d.store, err = sqlite.Open(filepath.Join(d.dataDir, "barnraise.db"))
	if err != nil {
		return fmt.Errorf("open state: %w", err)
	}
	defer d.store.Close()
	kn, err := knowledge.New(d.store.DB())
	if err != nil {
		return fmt.Errorf("open knowledge index: %w", err)
	}
	d.secrets = keychain.New("barnraise", filepath.Join(paths.ConfigDir(), "secrets"))

	if err := d.startNATS(ctx); err != nil {
		log.Warn("NATS unavailable; running without the event bus between machines", "err", err)
	}
	defer d.stopNATS()

	providers := map[string]ports.Provider{
		"claude": claude.New(claude.Options{ExtraArgs: []string{"--permission-mode", "bypassPermissions"}}),
		"codex":  codex.New(codex.Options{}),
	}
	if opts.Fake {
		d.fake = newFakeAgent()
		providers["fake"] = d.fake.provider
	}
	ex := execx.New()
	d.sender = webhook.NewSender(webhook.Options{})
	defer d.sender.Close()

	exe, _ := os.Executable()
	svc := app.New(app.Deps{
		Store: d.store, Bus: &d.bus, Checks: &checkRouter{d: d}, Providers: providers, Knowledge: kn, Exec: ex, Log: log,
		Machine: df.Machine, Version: version, DataDir: d.dataDir, SocketPath: paths.Socket(), BinaryPath: exe,
		Notifier: d.notifier(), Blobs: blobsOrNil(d.blobs),
	})
	d.svc = svc
	app.RoleText = config.RolePrompt
	app.SetSecretScanner(scanWorktree)
	svc.Runtime().SetProbe(d.probe)
	if opts.NoAgents {
		svc.Runtime().MaxSessions = -1
	}
	if opts.Egress != "" {
		allow := append([]string(nil), egress.DefaultAllow...)
		for _, root := range df.Projects {
			if cfg, err := config.Load(root); err == nil {
				allow = append(allow, cfg.Policy.AllowHosts...)
			}
		}
		l, err := net.Listen("tcp", opts.Egress)
		if err != nil {
			return fmt.Errorf("egress proxy: %w", err)
		}
		px := egress.New(allow, log)
		px.OnBlock = func(host string) {
			for _, name := range svc.Projects() {
				_ = svc.RecordViolation(context.Background(), name, "agent", "", "network", "egress", "outbound connection to "+host+" blocked", host)
			}
		}
		go func() { _ = px.Serve(ctx, l) }()
		svc.Runtime().EgressProxy = l.Addr().String()
		log.Info("egress allow-list proxy", "listen", l.Addr().String(), "hosts", len(allow))
	}
	for _, root := range df.Projects {
		if err := d.addProject(ctx, root); err != nil {
			log.Error("project not loaded", "root", root, "err", err)
		}
	}
	// Peers come after the projects, so mirrored events find their project.
	if d.repl != nil {
		d.connectPeers()
	}

	d.binary = d.publishBinary(ctx)
	go d.watchVersions(ctx, d.binary)
	go d.forwardWebhooks(ctx)
	go d.reindexOnIntegration(ctx)
	go d.watchDaemonFile(ctx)
	go d.serveRemoteChecks(ctx)

	srv := httpapi.New(svc, httpapi.Options{
		Version: version, OwnerToken: owner, Store: d.store, Probe: d.probe, Panel: panel.FS(), OpenAPI: openapi.Document(),
		Register: func(ctx context.Context, root string) error {
			abs, _ := filepath.Abs(root)
			if d.loaded(abs) {
				return d.reloadRoot(ctx, abs)
			}
			if err := d.addProject(ctx, abs); err != nil {
				return err
			}
			cur, err := daemoncfg.Load()
			if err == nil && cur.AddProject(abs) {
				_ = cur.Save()
			}
			return nil
		},
		Reload: d.reloadProject,
		WebhookSecret: func(project, source string) string {
			v, _ := d.secrets.Get("webhook/" + project + "/" + source)
			return v
		},
		Log: log,
	})
	unixL, err := httpapi.ListenUnix(paths.Socket())
	if err != nil {
		return fmt.Errorf("listen on %s: %w", paths.Socket(), err)
	}
	errc := make(chan error, 2)
	go func() { errc <- srv.Serve(ctx, unixL) }()
	if df.Listen != "" && df.Listen != "off" {
		host, _, _ := net.SplitHostPort(df.Listen)
		loopback := host == "127.0.0.1" || host == "localhost" || host == "::1"
		if !loopback && (opts.TLSCert == "" || opts.TLSKey == "") {
			return fmt.Errorf("listening on %s exposes the API: give --tls-cert and --tls-key (or keep 127.0.0.1)", df.Listen)
		}
		tl, err := net.Listen("tcp", df.Listen)
		if err != nil {
			log.Warn("TCP API not available", "listen", df.Listen, "err", err)
		} else if loopback {
			go func() { errc <- srv.Serve(ctx, tl) }()
		} else {
			go func() { errc <- srv.ServeTLS(ctx, tl, opts.TLSCert, opts.TLSKey) }()
		}
	}
	log.Info("barnraise daemon up", "machine", df.Machine, "socket", paths.Socket(), "listen", df.Listen, "projects", svc.Projects(), "version", version)
	go svc.Runtime().Run(ctx)
	select {
	case <-ctx.Done():
	case err := <-errc:
		if err != nil {
			return err
		}
	}
	svc.Runtime().StopAll()
	log.Info("barnraise daemon stopped")
	return nil
}

func blobsOrNil(b *natsx.ObjectStore) ports.BlobStore {
	if b == nil {
		return nil
	}
	return b
}

func ownerToken() (string, error) {
	p := paths.OwnerTokenFile()
	if b, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(b)) != "" {
		return strings.TrimSpace(string(b)), nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	tok := app.NewSecret("bro_")
	return tok, os.WriteFile(p, []byte(tok+"\n"), 0o600)
}

// startNATS starts the embedded server: other machines authenticate with
// their nkeys and may only read our stream and call our RPC subjects.
func (d *daemon) startNATS(ctx context.Context) error {
	var users []natsx.UserPerm
	for _, p := range d.df.Peers {
		if p.PublicKey != "" {
			users = append(users, natsx.PeerPermissions(p.PublicKey, d.df.Machine))
		}
	}
	host := d.df.NATSHost
	if len(d.df.Peers) > 0 && (host == "127.0.0.1" || host == "") {
		// Peers need to reach us: prefer the Tailscale address, otherwise all interfaces (nkeys required).
		host = "0.0.0.0"
		if tn := tailnet.New(); tn.Available() && os.Getenv("BARNRAISE_NO_TAILSCALE") == "" {
			if self, _, err := tn.Status(ctx); err == nil {
				if ip := tailnet.IPv4(self.IPs); ip != "" {
					host = ip
				}
			}
		}
	}
	srv, err := natsx.StartServer(natsx.ServerOptions{Name: d.df.Machine, StoreDir: filepath.Join(d.dataDir, "nats"), Host: host, Port: d.df.NATSPort, Users: users})
	if err != nil {
		srv, err = natsx.StartServer(natsx.ServerOptions{Name: d.df.Machine, StoreDir: filepath.Join(d.dataDir, "nats"), Host: "127.0.0.1", Port: -1, Users: users})
		if err != nil {
			return err
		}
		d.log.Warn("NATS port busy; using a random local port (other machines cannot reach this one)", "url", srv.ClientURL())
	}
	nc, err := srv.Connect()
	if err != nil {
		srv.Shutdown()
		return err
	}
	bus, err := natsx.NewBus(ctx, nc, natsx.BusOptions{MaxAge: 30 * 24 * time.Hour})
	if err != nil {
		nc.Close()
		srv.Shutdown()
		return err
	}
	d.natsSrv = srv
	d.bus.v.Store(bus)
	if d.blobs == nil {
		if bs, err := natsx.NewObjectStore(ctx, filepath.Join(d.dataDir, "blobs"), bus.JetStream()); err == nil {
			d.blobs = bs
		}
	}
	cursors, err := natsx.NewFileCursorStore(filepath.Join(d.dataDir, "cursors.json"))
	if err != nil {
		return err
	}
	if d.repl == nil {
		d.repl = natsx.NewReplicator(d.df.Machine, func(e domain.Event) error {
			if d.svc == nil {
				return errors.New("not ready")
			}
			return d.svc.Ingest(context.Background(), e)
		}, cursors)
		d.repl.Logf = func(format string, args ...any) {
			msg := fmt.Sprintf(format, args...)
			if strings.HasSuffix(msg, ": connected") {
				d.log.Info("replication: " + msg)
				return
			}
			d.log.Warn("replication: " + msg)
		}
	}
	return nil
}

func (d *daemon) stopNATS() {
	if d.repl != nil {
		d.repl.Close()
	}
	if b := d.bus.v.Load(); b != nil {
		b.Conn().Close()
	}
	if d.natsSrv != nil {
		d.natsSrv.Shutdown()
	}
}

func (d *daemon) connectPeers() {
	opt, err := natsx.NkeyOption(d.df.NKeySeed)
	if err != nil {
		d.log.Warn("machine key", "err", err)
		return
	}
	known := map[string]bool{}
	for _, st := range d.repl.Peers() {
		known[st.Name] = true
	}
	for _, p := range d.df.Peers {
		if known[p.Name] {
			continue
		}
		if err := d.repl.AddPeer(p.Name, p.URL, opt); err != nil {
			d.log.Warn("peer", "name", p.Name, "err", err)
		}
		if d.blobs != nil {
			d.blobs.AddPeer(p.Name, p.URL, opt)
		}
	}
}

// watchDaemonFile picks up new projects and peers written by other commands.
func (d *daemon) watchDaemonFile(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		df, err := daemoncfg.Load()
		if err != nil || !df.ModTime().After(d.dfMod) {
			continue
		}
		d.dfMod = df.ModTime()
		oldPeers := len(d.df.Peers)
		d.df.Peers = df.Peers
		d.df.Notify = df.Notify
		for _, root := range df.Projects {
			if !d.loaded(root) {
				if err := d.addProject(ctx, root); err != nil {
					d.log.Warn("project", "root", root, "err", err)
				}
			}
		}
		if len(df.Peers) != oldPeers && d.natsSrv != nil {
			// New peers need permissions on our server: restart the NATS layer.
			d.log.Info("cluster peers changed; restarting the NATS layer", "peers", len(df.Peers))
			if b := d.bus.v.Load(); b != nil {
				b.Conn().Close()
			}
			d.natsSrv.Shutdown()
			d.natsSrv = nil
			if err := d.startNATS(ctx); err != nil {
				d.log.Warn("NATS restart", "err", err)
			}
		}
		if d.repl != nil {
			d.connectPeers()
		}
	}
}

func (d *daemon) loaded(root string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.roots {
		if r == root {
			return true
		}
	}
	return false
}

// addProject builds a project's ports from its barnraise.yaml.
func (d *daemon) addProject(ctx context.Context, root string) error {
	cfg, err := config.Load(root)
	if err != nil {
		return err
	}
	pp, err := d.projectPorts(cfg)
	if err != nil {
		return err
	}
	if d.opts.Fake {
		cfg.Models = fakeModels(cfg.Models)
	}
	if _, err := d.svc.AddProject(ctx, cfg, pp); err != nil {
		return err
	}
	if d.opts.Fake {
		_, _ = d.svc.UpsertAccount(ctx, cfg.Name, app.System(), domain.Account{ID: "fake-1", Provider: "fake", Status: domain.AccountOK, CeilingPct: 100})
	}
	d.mu.Lock()
	d.roots[cfg.Name] = root
	d.mu.Unlock()
	if p, err := d.svc.Project(cfg.Name); err == nil && cfg.Goal.Name != "" && cfg.Goal.DoneWhen != "" {
		known := false
		for _, g := range p.Snapshot().Goals {
			if g.Name == cfg.Goal.Name {
				known = true // active or already finished: never recreate it
			}
		}
		if !known {
			if _, _, err := d.svc.SetGoal(ctx, cfg.Name, app.System(), cfg.Goal); err != nil {
				d.log.Warn("goal from barnraise.yaml", "err", err)
			}
		}
	}
	go func() {
		if err := d.svc.Reindex(context.Background(), cfg.Name, nil); err != nil {
			d.log.Warn("knowledge index", "project", cfg.Name, "err", err)
		}
	}()
	d.log.Info("project loaded", "project", cfg.Name, "root", root, "template", cfg.Template, "tracker", cfg.Tracker.Type)
	d.svc.Runtime().Poke()
	return nil
}

func (d *daemon) reloadProject(ctx context.Context, project string) error {
	d.mu.Lock()
	root, ok := d.roots[project]
	d.mu.Unlock()
	if !ok {
		return fmt.Errorf("project %s is not loaded", project)
	}
	return d.reloadRoot(ctx, root)
}

func (d *daemon) reloadRoot(ctx context.Context, root string) error {
	cfg, err := config.Load(root)
	if err != nil {
		return err
	}
	p, err := d.svc.Project(cfg.Name)
	if err != nil {
		return err
	}
	pp, err := d.projectPorts(cfg)
	if err != nil {
		return err
	}
	if d.opts.Fake {
		cfg.Models = fakeModels(cfg.Models)
	}
	if err := d.svc.ReloadConfig(cfg.Name, cfg); err != nil {
		return err
	}
	cur := *p.Ports()
	cur.Tracker, cur.CodeHost, cur.Triage = pp.Tracker, pp.CodeHost, pp.Triage
	p.SetPorts(cur)
	d.svc.SyncAllToTracker(cfg.Name)
	return nil
}

// projectPorts resolves git, the arbiter, the tracker and the code host.
func (d *daemon) projectPorts(cfg *domain.ProjectConfig) (app.ProjectPorts, error) {
	remote := ""
	if out, err := exec.Command("git", "-C", cfg.Root, "remote", "get-url", cfg.CodeHost.Remote).Output(); err == nil && len(strings.TrimSpace(string(out))) > 0 {
		remote = cfg.CodeHost.Remote
	}
	if _, err := os.Stat(filepath.Join(cfg.Root, ".git")); err != nil {
		return app.ProjectPorts{}, fmt.Errorf("%s is not a git repository: run `git init` (Barnraise works on branches and a main)", cfg.Root)
	}
	repo := gitx.New(cfg.Root, remote, cfg.MainBranch)
	pp := app.ProjectPorts{Git: repo, Arbiter: gitx.NewArbiter(repo, nil)}
	token := func(ref, env string) string {
		if after, ok := strings.CutPrefix(ref, "keychain:"); ok {
			v, _ := d.secrets.Get(after)
			return v
		}
		if after, ok := strings.CutPrefix(ref, "env:"); ok {
			return os.Getenv(after)
		}
		if v := os.Getenv(env); v != "" {
			return v
		}
		return ""
	}
	var err error
	switch cfg.Tracker.Type {
	case "github":
		tok := token(cfg.Tracker.Token, "GITHUB_TOKEN")
		if tok == "" {
			if out, err := exec.Command("gh", "auth", "token").Output(); err == nil {
				tok = strings.TrimSpace(string(out))
			}
		}
		proj, _ := strconv.Atoi(cfg.Tracker.Project)
		var c *github.Client
		c, err = github.New(github.Options{Repo: cfg.Tracker.Repo, Token: tok, Project: proj, Columns: cfg.Tracker.Columns})
		if err == nil {
			pp.Tracker = c.Tracker()
		}
	case "gitlab":
		var c *gitlab.Client
		c, err = gitlab.New(gitlab.Options{BaseURL: cfg.Tracker.URL, Project: cfg.Tracker.Repo, Token: token(cfg.Tracker.Token, "GITLAB_TOKEN"), Columns: cfg.Tracker.Columns})
		if err == nil {
			pp.Tracker = c.Tracker()
		}
	case "jira":
		email, key, _ := strings.Cut(cfg.Tracker.Project, "/")
		if key == "" {
			key, email = email, os.Getenv("JIRA_EMAIL")
		}
		var t *jira.Tracker
		t, err = jira.New(jira.Options{BaseURL: cfg.Tracker.URL, Email: email, Token: token(cfg.Tracker.Token, "JIRA_TOKEN"), ProjectKey: key, Columns: cfg.Tracker.Columns})
		if err == nil {
			pp.Tracker = t
		}
	case "linear":
		var t *linear.Tracker
		t, err = linear.New(linear.Options{APIKey: token(cfg.Tracker.Token, "LINEAR_API_KEY"), TeamKey: cfg.Tracker.Project, Columns: cfg.Tracker.Columns})
		if err == nil {
			pp.Tracker = t
		}
	}
	if err != nil {
		return pp, fmt.Errorf("tracker %s: %w", cfg.Tracker.Type, err)
	}
	if remote != "" {
		switch cfg.CodeHost.Type {
		case "github":
			tok := token(cfg.CodeHost.Token, "GITHUB_TOKEN")
			if tok == "" {
				tok = token(cfg.Tracker.Token, "GITHUB_TOKEN")
			}
			if tok == "" {
				if out, err := exec.Command("gh", "auth", "token").Output(); err == nil {
					tok = strings.TrimSpace(string(out))
				}
			}
			if tok != "" && cfg.CodeHost.Repo != "" {
				if c, err := github.New(github.Options{Repo: cfg.CodeHost.Repo, Token: tok}); err == nil {
					pp.CodeHost = c.CodeHost()
				}
			}
		case "gitlab":
			tok := token(cfg.CodeHost.Token, "GITLAB_TOKEN")
			if tok != "" && cfg.CodeHost.Repo != "" {
				if c, err := gitlab.New(gitlab.Options{BaseURL: cfg.CodeHost.URL, Project: cfg.CodeHost.Repo, Token: tok}); err == nil {
					pp.CodeHost = c.CodeHost()
				}
			}
		}
	}
	name := cfg.Name
	pp.Triage = &app.ProviderTriage{Provider: claude.New(claude.Options{}), Model: "haiku", Workdir: cfg.Root, Account: func() (domain.Account, bool) {
		p, err := d.svc.Project(name)
		if err != nil {
			return domain.Account{}, false
		}
		snap := p.Snapshot()
		acc := domain.PickAccount(snap.AccountList(), "claude", d.df.Machine, name, time.Now())
		if acc == nil {
			return domain.Account{}, false
		}
		return *acc, true
	}}
	if d.opts.Fake {
		pp.Triage = nil
	}
	d.mu.Lock()
	d.runners[cfg.Name] = d.newRunner(cfg, pp.CodeHost)
	d.mu.Unlock()
	return pp, nil
}

func (d *daemon) newRunner(cfg *domain.ProjectConfig, ch ports.CodeHost) *checks.Runner {
	project := cfg.Name
	o := checks.Options{Exec: execx.New(), LogDir: filepath.Join(d.dataDir, "logs", project), Blobs: blobsOrNil(d.blobs), AreaOf: cfg.AreaOfFile,
		Remote: func(ctx context.Context, label string, spec ports.CheckSpec) error {
			return d.dispatchRemote(project, label, spec)
		},
	}
	if ch != nil {
		o.Dispatch = func(ctx context.Context, workflow, branch string, inputs map[string]string) error {
			return ch.DispatchWorkflow(ctx, workflow, branch, inputs)
		}
	}
	if sec, err := d.secrets.Get("webhook/" + project + "/inbound"); err == nil {
		o.WebhookSecret = sec
		o.CallbackURL = "http://" + d.df.Listen + "/v1/webhooks/inbound/" + project
	}
	return checks.NewRunner(o)
}

// checkRouter picks the project's runner from the check's directory, and
// runs machine:<label> checks here when this machine has the label.
type checkRouter struct{ d *daemon }

func (r *checkRouter) Run(ctx context.Context, spec ports.CheckSpec) (domain.CheckResult, bool, error) {
	project := r.d.projectOfDir(spec.Dir)
	r.d.mu.Lock()
	run := r.d.runners[project]
	r.d.mu.Unlock()
	if run == nil {
		return domain.CheckResult{}, false, fmt.Errorf("no check runner for %s", spec.Dir)
	}
	if label, ok := strings.CutPrefix(spec.Def.RunsOn, "machine:"); ok {
		if m := r.d.probe(); m.HasLabel(label) {
			spec.Def.RunsOn = "local"
		}
	}
	return run.Run(ctx, spec)
}

func (d *daemon) projectOfDir(dir string) string {
	for _, sub := range []string{"worktrees", "repro"} {
		base := filepath.Join(d.dataDir, sub) + string(filepath.Separator)
		if rest, ok := strings.CutPrefix(dir, base); ok {
			p, _, _ := strings.Cut(rest, string(filepath.Separator))
			return p
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for name, root := range d.roots {
		if strings.HasPrefix(dir, root) {
			return name
		}
	}
	if len(d.roots) == 1 {
		for name := range d.roots {
			return name
		}
	}
	return ""
}

// remoteCheck is the RPC body to run a check on another machine.
type remoteCheck struct {
	Project string          `json:"project"`
	Spec    ports.CheckSpec `json:"spec"`
}

// dispatchRemote sends a machine:<label> check to a peer that has the label;
// the result comes back through ReportCheck.
func (d *daemon) dispatchRemote(project, label string, spec ports.CheckSpec) error {
	p, err := d.svc.Project(project)
	if err != nil {
		return err
	}
	var target string
	now := time.Now()
	for _, m := range p.Snapshot().Machines {
		if m.Name != d.df.Machine && m.HasLabel(label) && m.Health(now, p.Cfg().Lease.TTL) != "red" {
			target = m.Name
			break
		}
	}
	if target == "" || d.repl == nil {
		return fmt.Errorf("no machine with %s is available", label)
	}
	body, _ := json.Marshal(remoteCheck{Project: project, Spec: spec})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), spec.Def.Timeout+5*time.Minute)
		defer cancel()
		out, err := d.repl.Request(ctx, target, natsx.RPCSubject(target, "check"), body, spec.Def.Timeout+2*time.Minute, 2)
		res := domain.CheckResult{ItemID: spec.ItemID, Check: spec.Def.Name, Commit: spec.Commit, RunsOn: "machine:" + target}
		if err != nil {
			res.Summary = "remote check on " + target + " failed: " + err.Error()
		} else if err := json.Unmarshal(out, &res); err != nil {
			res.Summary = "remote check answer unreadable: " + err.Error()
		}
		res.ItemID, res.Check = spec.ItemID, spec.Def.Name
		if _, err := d.svc.ReportCheck(context.Background(), project, app.System(), res); err != nil {
			d.log.Warn("report remote check", "err", err)
		}
	}()
	return nil
}

// serveRemoteChecks runs checks other machines send here, at the item's commit.
func (d *daemon) serveRemoteChecks(ctx context.Context) {
	b := d.bus.v.Load()
	if b == nil {
		return
	}
	_ = natsx.Serve(ctx, b.Conn(), natsx.RPCSubject(d.df.Machine, "check"), func(ctx context.Context, data []byte) ([]byte, error) {
		var rc remoteCheck
		if err := json.Unmarshal(data, &rc); err != nil {
			return nil, err
		}
		p, err := d.svc.Project(rc.Project)
		if err != nil {
			return nil, err
		}
		g := p.Ports().Git
		if err := g.Fetch(ctx); err != nil {
			d.log.Warn("fetch for remote check", "err", err)
		}
		if rc.Spec.Branch != "" {
			_, _ = g.FetchBranch(ctx, rc.Spec.Branch)
		}
		dir := filepath.Join(d.dataDir, "worktrees", rc.Project, "_remote-"+rc.Spec.ItemID+"-"+strconv.FormatInt(time.Now().UnixNano(), 36))
		if err := g.AddDetachedWorktree(ctx, dir, rc.Spec.Commit); err != nil {
			return nil, fmt.Errorf("checkout %s: %w", rc.Spec.Commit, err)
		}
		defer func() { _ = g.RemoveWorktree(context.Background(), dir) }()
		rc.Spec.Dir = dir
		rc.Spec.Def.RunsOn = "local"
		res, _, err := (&checkRouter{d: d}).Run(ctx, rc.Spec)
		if err != nil {
			return nil, err
		}
		return json.Marshal(res)
	})
}

// probe returns this machine's capabilities, cached for 30 seconds.
func (d *daemon) probe() domain.Machine {
	if m := d.machine.Load(); m != nil && time.Since(m.LastSeen) < 30*time.Second {
		return *m
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m := sysinfo.Probe(ctx)
	m.Name = d.df.Machine
	m.Slots = sysinfo.Slots(m, 4, m.Emulators)
	m.Version = version
	m.Platform = platform()
	m.Binary = d.binary
	m.LastSeen = time.Now()
	if d.natsSrv != nil {
		m.Address = d.natsSrv.ClientURL()
	}
	d.machine.Store(&m)
	return m
}

// notifier fans out to the daemon's and the projects' channels.
func (d *daemon) notifier() ports.Notifier {
	var defs []domain.NotifierDef
	for _, n := range d.df.Notify {
		defs = append(defs, domain.NotifierDef{Type: n.Type, Target: n.Target, Secret: n.Secret})
	}
	for _, root := range d.df.Projects {
		if cfg, err := config.Load(root); err == nil {
			defs = append(defs, cfg.Notifications...)
		}
	}
	if len(defs) == 0 {
		return nil
	}
	n, err := notify.FromDefs(defs, d.secrets)
	if err != nil {
		d.log.Warn("notifications", "err", err)
		return nil
	}
	return n
}

// forwardWebhooks sends events to each project's outgoing webhooks, signed.
func (d *daemon) forwardWebhooks(ctx context.Context) {
	ch, cancel := d.svc.Hub().Subscribe("", nil, 2048)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			if e.Type == domain.EvAgentSession || e.Type == domain.EvMachineUpdated {
				continue // live-only noise; SSE carries them
			}
			p, err := d.svc.Project(e.Project)
			if err != nil {
				continue
			}
			for _, w := range p.Cfg().Webhooks {
				if !webhook.Matches(w.Events, e.Type) {
					continue
				}
				secret := ""
				if w.SecretRef != "" {
					secret, _ = notify.SecretRef(d.secrets, w.SecretRef)
				}
				if err := d.sender.Send(w, secret, webhook.ToCloudEvent(e, webhook.Source(e.Machine, e.Project))); err != nil {
					d.log.Warn("webhook", "url", w.URL, "err", err)
				}
			}
		}
	}
}

// reindexOnIntegration refreshes the knowledge index when main moves.
func (d *daemon) reindexOnIntegration(ctx context.Context) {
	ch, cancel := d.svc.Hub().Subscribe("", []string{domain.EvItemIntegrated}, 64)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			if err := d.svc.Reindex(context.Background(), e.Project, nil); err != nil {
				d.log.Warn("reindex", "err", err)
			}
		}
	}
}

// scanWorktree scans pending changes of a worktree before a commit and push.
func scanWorktree(ctx context.Context, dir string) (string, error) {
	diff, _ := exec.CommandContext(ctx, "git", "-C", dir, "diff", "HEAD").Output()
	findings := secretscan.ScanDiff(string(diff))
	out, _ := exec.CommandContext(ctx, "git", "-C", dir, "ls-files", "--others", "--exclude-standard").Output()
	files := map[string][]byte{}
	for f := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if f == "" {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(dir, f)); err == nil && len(b) < 2<<20 {
			files[f] = b
		} else if err == nil {
			files[f] = nil
		}
	}
	findings = append(findings, secretscan.Scan(files)...)
	if len(findings) == 0 {
		return "", nil
	}
	var parts []string
	for _, f := range findings {
		parts = append(parts, fmt.Sprintf("%s:%d %s %s", f.File, f.Line, f.Rule, f.Redacted))
	}
	return strings.Join(parts, "; "), nil
}

var _ = nats.Option(nil)
