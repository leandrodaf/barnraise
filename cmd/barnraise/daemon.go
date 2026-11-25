package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/gen/openapi"
	"github.com/leandrodaf/barnraise/internal/adapters/in/cli"
	"github.com/leandrodaf/barnraise/internal/adapters/in/httpapi"
	"github.com/leandrodaf/barnraise/internal/adapters/in/panel"
	"github.com/leandrodaf/barnraise/internal/adapters/out/acp"
	"github.com/leandrodaf/barnraise/internal/adapters/out/capprobe"
	"github.com/leandrodaf/barnraise/internal/adapters/out/checks"
	"github.com/leandrodaf/barnraise/internal/adapters/out/claude"
	"github.com/leandrodaf/barnraise/internal/adapters/out/codex"
	"github.com/leandrodaf/barnraise/internal/adapters/out/execx"
	"github.com/leandrodaf/barnraise/internal/adapters/out/gemini"
	gitx "github.com/leandrodaf/barnraise/internal/adapters/out/git"
	"github.com/leandrodaf/barnraise/internal/adapters/out/github"
	"github.com/leandrodaf/barnraise/internal/adapters/out/gitlab"
	"github.com/leandrodaf/barnraise/internal/adapters/out/jira"
	"github.com/leandrodaf/barnraise/internal/adapters/out/keychain"
	"github.com/leandrodaf/barnraise/internal/adapters/out/knowledge"
	"github.com/leandrodaf/barnraise/internal/adapters/out/linear"
	natsx "github.com/leandrodaf/barnraise/internal/adapters/out/nats"
	"github.com/leandrodaf/barnraise/internal/adapters/out/notify"
	"github.com/leandrodaf/barnraise/internal/adapters/out/plugin"
	"github.com/leandrodaf/barnraise/internal/adapters/out/providerlogin"
	"github.com/leandrodaf/barnraise/internal/adapters/out/reflink"
	"github.com/leandrodaf/barnraise/internal/adapters/out/secretscan"
	"github.com/leandrodaf/barnraise/internal/adapters/out/sqlite"
	"github.com/leandrodaf/barnraise/internal/adapters/out/sysinfo"
	"github.com/leandrodaf/barnraise/internal/adapters/out/tailnet"
	"github.com/leandrodaf/barnraise/internal/adapters/out/toolinstall"
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
	natsLn  net.Listener    // NATS on the embedded Tailscale device
	tn      tailnet.Tailnet // nil: no tailnet
	repl    *natsx.Replicator
	blobs   *natsx.ObjectStore
	sender  *webhook.Sender
	mu      sync.Mutex
	roots   map[string]string // project -> root
	runners map[string]*checks.Runner
	plugins map[string]*plugin.Host // project/name -> the running plugin
	machine atomic.Pointer[domain.Machine]
	dataDir string
	opts    cli.DaemonOptions
	pidFile string
	binary  string
	fake    *fakeAgent
	// stopSelf ends the daemon as a signal would; a paired machine can ask for it.
	stopSelf context.CancelFunc
	iso      app.Isolation // Barnraise's own confinement layers
}

func runDaemon(ctx context.Context, opts cli.DaemonOptions) error {
	// A peer can ask this daemon to stop (barnraise down, answered "yes" there).
	ctx, stopSelf := context.WithCancel(ctx)
	defer stopSelf()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	df, err := daemoncfg.Load()
	if err != nil {
		return err
	}
	maxAgentsV.Store(int64(df.MaxAgents))
	if opts.Machine != "" {
		df.Machine = opts.Machine
	}
	if opts.NATSPort != 0 {
		df.NATSPort = opts.NATSPort
	}
	if opts.Listen != "" {
		df.Listen = opts.Listen
	}
	d := &daemon{log: log, df: df, dfMod: df.ModTime(), roots: map[string]string{}, runners: map[string]*checks.Runner{}, plugins: map[string]*plugin.Host{}, dataDir: paths.DataDir(), opts: opts}
	// Sessions write their temporary files on the data disk, never on a
	// small /tmp that, once full, breaks every command (see tempdir.go).
	tempBefore, restoreTemp := useDataTemp(d.dataDir)
	defer restoreTemp()
	go keepTempTrimmed(ctx, log, tempBefore)
	if err := os.MkdirAll(d.dataDir, 0o700); err != nil {
		return err
	}
	if err := paths.MarkNotIndexed(d.dataDir); err != nil {
		log.Warn("the data directory is not marked for file indexers to skip", "dir", d.dataDir, "err", err)
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
	defer d.closePlugins()
	kn, err := knowledge.New(d.store.DB(), knowledge.WithWriteLock(d.store.WriteLock()))
	if err != nil {
		return fmt.Errorf("open knowledge index: %w", err)
	}
	d.secrets = keychain.New("barnraise", filepath.Join(paths.ConfigDir(), "secrets"))

	d.openTailnet(ctx)
	defer d.closeTailnet()
	if err := d.startNATS(ctx); err != nil {
		log.Warn("NATS unavailable; running without the event bus between machines", "err", err)
	}
	defer d.stopNATS()

	providers := map[string]ports.Provider{
		"claude": claude.New(claude.Options{ExtraArgs: []string{"--permission-mode", "bypassPermissions"}}),
		"codex":  codex.New(codex.Options{}),
		"gemini": gemini.New(gemini.Options{}),
	}
	if opts.Fake {
		d.fake = newFakeAgent()
		providers["fake"] = d.fake.provider
	}
	ex := execx.New()
	kn.UseAstGrep(ex) // symbols by syntax tree when ast-grep is installed
	d.sender = webhook.NewSender(webhook.Options{})
	defer d.sender.Close()

	exe, _ := os.Executable()
	svc := app.New(app.Deps{
		Store: d.store, Bus: &d.bus, Checks: &checkRouter{d: d}, Providers: providers, Knowledge: kn, Exec: ex, Log: log,
		ACP:     acp.New(acp.Options{Version: version}),
		Machine: df.Machine, Version: version, BuildTime: selfBuild().Time, SelfUpdate: os.Getenv("BARNRAISE_NO_SELF_UPDATE") == "", DataDir: d.dataDir, SocketPath: paths.Socket(), BinaryPath: exe,
		Notifier: d.notifier(), Blobs: blobsOrNil(d.blobs), Host: sysinfo.NewHost(),
		ResolveSecret: func(ref string) (string, error) { return notify.SecretRef(d.secrets, ref) },
		Isolation:     d.isolation(exe),
		Cloner:        reflink.New(),
		Prober:        capprobe.New(capprobe.Options{Exec: ex, DataDir: d.dataDir, ToolsBin: toolinstall.BinDir(toolinstall.Root(d.dataDir))}),
		Installer:     toolinstall.New(toolinstall.Options{Root: toolinstall.Root(d.dataDir)}),
		Login:         providerlogin.New(),
	})
	d.svc = svc
	app.RoleText = config.RolePrompt
	app.SetSecretScanner(scanWorktree)
	svc.Runtime().SetProbe(d.probe)
	svc.SetMachineSettings(d.machineSettings)
	if opts.NoAgents {
		svc.Runtime().MaxSessions = -1
	}
	if opts.Egress != "" {
		if err := d.startEgress(ctx, svc, df.Projects, opts.Egress); err != nil {
			return err
		}
	}
	for _, root := range df.Projects {
		if err := d.addProject(ctx, root, false); err != nil {
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
	go d.forwardHooks(ctx)
	go d.reindexOnIntegration(ctx)
	go d.watchDaemonFile(ctx)
	go d.serveRemoteChecks(ctx)
	d.stopSelf = stopSelf
	go d.serveRemoteStop(ctx)
	go d.serveRemoteStandby(ctx)
	go d.serveRemoteCapabilities(ctx)
	go d.serveRemoteLogin(ctx)
	go d.serveRemoteAgents(ctx)
	go d.serveRemoteProject(ctx)
	go d.watchCapabilities(ctx)
	go newChainWatch(d).run(ctx)

	srv := httpapi.New(svc, httpapi.Options{
		Version: version, OwnerToken: owner, Store: d.store, Probe: d.probe, Panel: panel.FS(), OpenAPI: openapi.Document(),
		Register: d.register,
		Reload:   d.reloadProject,
		Peers:    d.peers, StopPeers: d.stopPeers, SetStandby: d.setStandby,
		Capabilities: d.capabilities, InstallTools: d.installTools, AccountLogin: d.accountLogin, SetMaxAgents: d.setMachineAgents,
		ShareProject: d.shareProject,
		VerifyAnchor: natsx.AnchorVerifier(df.NKeyPublic),
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
	if df.Standby {
		// Before the first pass: a machine left in standby starts nothing.
		svc.Runtime().SetStandby(true, df.StandbyWhy)
		log.Info("this machine is in standby: no agent, check or merge starts here until `barnraise standby off`", "why", df.StandbyWhy)
	}
	go svc.Runtime().Run(ctx)
	select {
	case <-ctx.Done():
	case err := <-errc:
		if err != nil {
			return err
		}
	}
	svc.Runtime().StopAll()
	svc.StopLogins()
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

// openTailnet joins the tailnet daemon.yaml asks for (`network:`). With the
// embedded Tailscale this process becomes a device of the tailnet: NATS and
// the pairing port are served on it, and peers are dialed through it.
func (d *daemon) openTailnet(ctx context.Context) {
	cfg := cli.TailnetConfig(d.df, func(format string, args ...any) { d.log.Info("tailscale: " + fmt.Sprintf(format, args...)) })
	cfg.Plugin.Version, cfg.Plugin.Log = version, d.log
	cfg.Plugin.Secret = func(ref string) (string, error) { return notify.SecretRef(d.secrets, ref) }
	tn, mode, err := tailnet.Open(ctx, cfg)
	if err != nil {
		d.log.Warn("tailnet not available; other machines reach this one over the local network", "network", mode, "err", err)
		return
	}
	if tn == nil {
		return
	}
	d.tn = tn
	if mode == tailnet.ModePlugin {
		self, peers, _ := tn.Status(ctx)
		d.log.Info("network plugin up", "name", self.Name, "ip", tailnet.IPv4(self.IPs), "peers", len(peers), "barnraise_peers", len(tailnet.BarnraisePeers(peers)))
	}
	if !d.embedded() {
		return
	}
	self, _, _ := tn.Status(ctx)
	d.log.Info("joined the tailnet as its own device", "name", self.DNSName, "ip", tailnet.IPv4(self.IPs), "state", cfg.StateDir)
	// `machine invite` listens on loopback while the daemon holds the device:
	// relay the device's pairing port to it.
	if ln, err := tn.Listen(ctx, fmt.Sprintf(":%d", tailnet.PairingPort)); err != nil {
		d.log.Warn("pairing port on the tailnet", "err", err)
	} else {
		go func() { _ = tailnet.Forward(ctx, ln, fmt.Sprintf("127.0.0.1:%d", tailnet.PairingPort)) }()
	}
}

// embedded reports whether this process is its own tailnet device.
func (d *daemon) embedded() bool { return d.tn != nil && d.tn.Mode() == tailnet.ModeTsnet }

func (d *daemon) closeTailnet() {
	if d.tn != nil {
		_ = d.tn.Close()
		d.tn = nil
	}
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
	if len(d.df.Peers) > 0 && (host == "127.0.0.1" || host == "") && !d.embedded() {
		// Peers need to reach us: prefer the Tailscale address, otherwise all interfaces (nkeys required).
		// The embedded device needs neither: peers arrive through its own listener below.
		host = "0.0.0.0"
		if d.tn != nil && d.tn.Available() {
			if self, _, err := d.tn.Status(ctx); err == nil {
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
	if d.embedded() {
		if ln, err := d.tn.Listen(ctx, fmt.Sprintf(":%d", d.df.NATSPort)); err != nil {
			d.log.Warn("NATS on the tailnet", "err", err)
		} else {
			d.natsLn = ln
			go func() { _ = srv.ServeListener(ln) }()
		}
	}
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

func (d *daemon) closeNATSListener() {
	if d.natsLn != nil {
		d.natsLn.Close()
		d.natsLn = nil
	}
}

func (d *daemon) stopNATS() {
	d.closeNATSListener()
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
	opts := []nats.Option{opt}
	if d.embedded() {
		// Tailnet addresses exist only inside the embedded device.
		opts = append(opts, natsx.DialerOption(d.tn.Dial))
	}
	known := map[string]bool{}
	for _, st := range d.repl.Peers() {
		known[st.Name] = true
	}
	for _, p := range d.df.Peers {
		if known[p.Name] {
			continue
		}
		if err := d.repl.AddPeer(p.Name, p.URL, opts...); err != nil {
			d.log.Warn("peer", "name", p.Name, "err", err)
		}
		if d.blobs != nil {
			d.blobs.AddPeer(p.Name, p.URL, opts...)
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
		if d.setMaxAgents(df.MaxAgents) {
			d.machine.Store(nil) // the next heartbeat carries the new limit
		}
		// `barnraise standby` may have written the file itself.
		if on, why := d.svc.Runtime().Standby(); on != df.Standby || (on && why != df.StandbyWhy) {
			d.svc.Runtime().SetStandby(df.Standby, df.StandbyWhy)
		}
		for _, root := range df.Projects {
			if !d.loaded(root) {
				if err := d.addProject(ctx, root, false); err != nil {
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
			d.closeNATSListener()
			d.natsSrv.Shutdown()
			d.natsSrv = nil
			if d.tn == nil {
				// The embedded device may have been busy (a pairing command held it) at start.
				d.openTailnet(ctx)
			}
			if err := d.startNATS(ctx); err != nil {
				d.log.Warn("NATS restart", "err", err)
			} else {
				// The subscriptions went with the old connection.
				go d.serveRemoteStop(ctx)
				go d.serveRemoteStandby(ctx)
				go d.serveRemoteCapabilities(ctx)
				go d.serveRemoteLogin(ctx)
				go d.serveRemoteAgents(ctx)
				go d.serveRemoteProject(ctx)
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
// pausedNote is why a project registered with `barnraise up --paused` waits.
const pausedNote = "started paused (barnraise up --paused): adopt the existing work, then barnraise resume"

// pauseRoot pauses the project already loaded from root.
func (d *daemon) pauseRoot(ctx context.Context, root string) error {
	cfg, err := config.Load(root)
	if err != nil {
		return err
	}
	if p, err := d.svc.Project(cfg.Name); err == nil && p.Snapshot().Paused {
		return nil
	}
	return d.svc.Pause(ctx, cfg.Name, app.System(), pausedNote)
}

// register loads the project of root, or reloads it when it is loaded,
// and keeps it in daemon.yaml.
func (d *daemon) register(ctx context.Context, root string, paused bool) error {
	abs, _ := filepath.Abs(root)
	if d.loaded(abs) {
		if paused {
			if err := d.pauseRoot(ctx, abs); err != nil {
				return err
			}
		}
		return d.reloadRoot(ctx, abs)
	}
	if err := d.addProject(ctx, abs, paused); err != nil {
		return err
	}
	cur, err := daemoncfg.Load()
	if err == nil && cur.AddProject(abs) {
		_ = cur.Save()
	}
	return nil
}

// addProject loads the project of root. With paused, the pause is recorded
// before the runtime is woken, so no agent of it can start.
func (d *daemon) addProject(ctx context.Context, root string, paused bool) error {
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
	if err := d.svc.TrustOnLoad(ctx, cfg.Name); err != nil {
		d.log.Warn("record the trusted files", "project", cfg.Name, "err", err)
	}
	if paused {
		if p, err := d.svc.Project(cfg.Name); err == nil && !p.Snapshot().Paused {
			if err := d.svc.Pause(ctx, cfg.Name, app.System(), pausedNote); err != nil {
				return err
			}
		}
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
	// The owner reloads (barnraise reload, barnraise up): the files as they are now are trusted.
	if err := d.svc.RecordTrust(ctx, cfg.Name, "reloaded by the owner"); err != nil {
		d.log.Warn("record the trusted files", "project", cfg.Name, "err", err)
	}
	cur := *p.Ports()
	cur.Tracker, cur.CodeHost, cur.Triage = pp.Tracker, pp.CodeHost, pp.Triage
	p.SetPorts(cur)
	d.svc.SyncAllToTracker(cfg.Name)
	return nil
}

// ghAuthToken is `gh auth token` (a variable for the tests).
var ghAuthToken = func() string {
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// githubToken picks the token for a GitHub repository: the one barnraise.yaml
// names, then the one `barnraise machine share --token-stdin` kept for that
// repository (git already pushes with it here, so a machine that can push can
// also write the repository's issues and pull requests), then GITHUB_TOKEN,
// then `gh auth token`.
func (d *daemon) githubToken(resolve func(ref, env string) string, ref, repo string) string {
	if ref != "" {
		if v := resolve(ref, "GITHUB_TOKEN"); v != "" {
			return v
		}
	}
	if key := domain.GitTokenSecret("github.com", repo); key != "" && d.secrets != nil {
		if v, _ := d.secrets.Get(key); v != "" {
			return v
		}
	}
	if v := resolve("", "GITHUB_TOKEN"); v != "" {
		return v
	}
	return ghAuthToken()
}

// projectPorts resolves git, the arbiter, the tracker and the code host.
func (d *daemon) projectPorts(cfg *domain.ProjectConfig) (app.ProjectPorts, error) {
	remote, remoteURL := "", ""
	if out, err := exec.Command("git", "-C", cfg.Root, "remote", "get-url", cfg.CodeHost.Remote).Output(); err == nil && len(strings.TrimSpace(string(out))) > 0 {
		remote, remoteURL = cfg.CodeHost.Remote, strings.TrimSpace(string(out))
	}
	if _, err := os.Stat(filepath.Join(cfg.Root, ".git")); err != nil {
		return app.ProjectPorts{}, fmt.Errorf("%s is not a git repository: run `git init` (Barnraise works on branches and a main)", cfg.Root)
	}
	repo := gitx.New(cfg.Root, remote, cfg.MainBranch)
	d.setupMerge(cfg, repo)
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
	d.syncPlugins(cfg)
	var err error
	if name, ok := domain.PluginRef(cfg.Tracker.Type); ok {
		h, perr := d.pluginOf(cfg.Name, name)
		if perr != nil {
			return pp, fmt.Errorf("tracker %s: %w", cfg.Tracker.Type, perr)
		}
		pp.Tracker = h.Tracker(cfg.Tracker.Columns)
	}
	switch cfg.Tracker.Type {
	case "github":
		tok := d.githubToken(token, cfg.Tracker.Token, cfg.Tracker.Repo)
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
	// The repository's web page, for the panel's links: the code host's
	// type when there is one, else the tracker's, else the host's name.
	hostKind := cfg.CodeHost.Type
	if hostKind == "" {
		hostKind = cfg.Tracker.Type
	}
	if web := gitx.ParseRepoWeb(remoteURL, hostKind); web.Base != "" {
		pp.Web = web
	}
	if remote != "" {
		switch cfg.CodeHost.Type {
		case "github":
			ref := cfg.CodeHost.Token
			if ref == "" {
				ref = cfg.Tracker.Token
			}
			tok := d.githubToken(token, ref, cfg.CodeHost.Repo)
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
		return *acc.On(d.df.Machine, d.dataDir), true
	}}
	// The advisor talks a decision over with the owner, with the strongest
	// model of the project's Claude ladder, reading only.
	// When the strongest is at its limit (or every account's window is),
	// the next model of the ladder answers.
	ladder := cfg.Models.Ladders["claude"]
	models := []string{"opus"}
	if len(ladder) > 0 {
		models = make([]string, 0, len(ladder))
		for i := len(ladder) - 1; i >= 0; i-- {
			models = append(models, ladder[i])
		}
	}
	pp.Advisor = &app.ProviderAdvisor{Provider: claude.New(claude.Options{}), Models: models, TempDir: os.TempDir(),
		Accounts: func(model string) []domain.Account { return d.svc.AdvisorAccounts(name, "claude", model) },
		OnLimit: func(account, model string, resets time.Time, modelOnly bool) {
			d.svc.AdvisorLimit(name, account, model, resets, modelOnly)
		}}
	if d.opts.Fake {
		pp.Triage, pp.Advisor = nil, nil
	}
	d.mu.Lock()
	d.runners[cfg.Name] = d.newRunner(cfg, pp.CodeHost)
	d.mu.Unlock()
	return pp, nil
}

func (d *daemon) newRunner(cfg *domain.ProjectConfig, ch ports.CodeHost) *checks.Runner {
	project := cfg.Name
	o := checks.Options{Exec: execx.New(), LogDir: filepath.Join(d.dataDir, "logs", project), Blobs: blobsOrNil(d.blobs), AreaOf: cfg.AreaOfFile,
		Dependencies: cfg.Dependencies, MainRefs: mainRefs(cfg), Machine: d.df.Machine,
		Remote: func(ctx context.Context, label string, spec ports.CheckSpec) error {
			return d.dispatchRemote(project, label, spec)
		},
		Plugin: func(ctx context.Context, name string, spec ports.CheckSpec) (domain.CheckResult, bool, error) {
			h, err := d.pluginOf(project, name)
			if err != nil {
				return domain.CheckResult{}, false, err
			}
			return h.RunCheck(ctx, spec)
		},
		Confine: func(spec ports.CheckSpec, dir string, env map[string]string) (*ports.Confine, map[string]string, error) {
			return d.svc.CheckConfine(project, spec.Def, dir, env)
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
	if target, why := r.d.offloadTarget(project, spec); target != "" {
		r.d.offload(project, target, why, spec, run)
		return domain.CheckResult{}, true, nil // the result comes through ReportCheck
	}
	return run.Run(ctx, spec)
}

// offloadTarget picks an idle machine for a local check when this one is
// loaded ("" = run it here). Only a check of an item's pushed commit can
// move: the other machine checks that commit out by itself.
func (d *daemon) offloadTarget(project string, spec ports.CheckSpec) (target, why string) {
	def := spec.Def
	if (def.RunsOn != "local" && def.RunsOn != "") || def.NoOffload || spec.Main || spec.Commit == "" || spec.Branch == "" || d.repl == nil {
		return "", ""
	}
	p, err := d.svc.Project(project)
	if err != nil {
		return "", ""
	}
	self := d.probe()
	snap := p.Snapshot()
	peers := make([]*domain.Machine, 0, len(snap.Machines))
	for _, m := range snap.Machines {
		if _, connected := d.repl.Conn(m.Name); connected {
			peers = append(peers, m)
		}
	}
	return domain.ChooseOffload(self, peers, time.Now(), p.Cfg().Lease.TTL)
}

// offload runs a check on another machine and reports its result. If that
// machine cannot do it (unreachable, the commit is not there, it is leaving),
// the check runs here after all: moving work is an optimization, never a
// reason for a check to fail.
func (d *daemon) offload(project, target, why string, spec ports.CheckSpec, local *checks.Runner) {
	d.log.Info("check moved to an idle machine", "check", spec.Def.Name, "item", spec.ItemID, "to", target, "why", why)
	remote := spec
	remote.Def.NoOffload = true // the other machine runs it; it does not pass it on
	body, _ := json.Marshal(remoteCheck{Project: project, Spec: remote})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), spec.Def.Timeout+5*time.Minute)
		defer cancel()
		var res domain.CheckResult
		out, err := d.repl.Request(ctx, target, natsx.RPCSubject(target, "check"), body, spec.Def.Timeout+2*time.Minute, 0)
		if err == nil {
			err = json.Unmarshal(out, &res)
		}
		if err == nil {
			res.RunsOn = "machine:" + target
			res.Summary = strings.TrimSpace("ran on " + target + " (" + why + "). " + res.Summary)
		} else {
			d.log.Warn("the idle machine could not run the check; running it here", "check", spec.Def.Name, "item", spec.ItemID, "machine", target, "err", err)
			here := spec
			here.Def.NoOffload = true
			var pending bool
			res, pending, err = local.Run(ctx, here)
			if err != nil {
				res = domain.CheckResult{Passed: false, Summary: "check could not run: " + err.Error()}
			} else if pending {
				return
			}
		}
		res.ItemID, res.Check, res.Commit = spec.ItemID, spec.Def.Name, spec.Commit
		if _, err := d.svc.ReportCheck(context.Background(), project, app.System(), res); err != nil {
			d.log.Warn("report the moved check", "err", err)
		}
	}()
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
	// Leftovers of a daemon that died in the middle of a check for another machine.
	d.mu.Lock()
	roots := make(map[string]string, len(d.roots))
	for name, root := range d.roots {
		roots[name] = root
	}
	d.mu.Unlock()
	for name := range roots {
		stale, _ := filepath.Glob(filepath.Join(d.dataDir, "worktrees", name, "_remote-*"))
		if p, err := d.svc.Project(name); err == nil && p.Ports().Git != nil {
			for _, dir := range stale {
				_ = p.Ports().Git.RemoveWorktree(ctx, dir)
			}
		}
	}
	_ = natsx.Serve(ctx, b.Conn(), natsx.RPCSubject(d.df.Machine, "check"), func(ctx context.Context, data []byte) ([]byte, error) {
		if on, _ := d.svc.Runtime().Standby(); on {
			return nil, errors.New("this machine is in standby") // the asking machine runs the check itself
		}
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
		defer func() {
			// What the check started (its compose project) goes with the checkout.
			d.svc.ReleaseServices(context.Background(), p, rc.Spec.ItemID, dir)
			_ = g.RemoveWorktree(context.Background(), dir)
		}()
		if err := d.svc.SetupWorktreeFor(ctx, p, dir, rc.Spec.ItemID); err != nil {
			return nil, err // the asking machine runs the check itself
		}
		rc.Spec.Dir = dir
		// The shared stores are this machine's: the paths that came are the sender's.
		if stores := d.svc.StoreEnv(p); len(stores) > 0 {
			if rc.Spec.Env == nil {
				rc.Spec.Env = map[string]string{}
			}
			maps.Copy(rc.Spec.Env, stores)
		}
		rc.Spec.Def.RunsOn = "local"
		rc.Spec.Def.NoOffload = true // asked to run it here: never passed on again
		res, _, err := (&checkRouter{d: d}).Run(ctx, rc.Spec)
		if err != nil {
			return nil, err
		}
		return json.Marshal(res)
	})
}

// serveRemoteStop stops this daemon when a paired machine asks. Only paired
// machines reach the subject: the NATS server admits their keys and no other.
func (d *daemon) serveRemoteStop(ctx context.Context) {
	b := d.bus.v.Load()
	if b == nil {
		return
	}
	_ = natsx.Serve(ctx, b.Conn(), natsx.RPCSubject(d.df.Machine, "down"), func(_ context.Context, data []byte) ([]byte, error) {
		d.log.Info("a paired machine asked this daemon to stop", "from", string(data))
		// Answer first: the reply leaves before the connection closes.
		time.AfterFunc(500*time.Millisecond, d.stopSelf)
		return []byte("ok"), nil
	})
}

// remoteStandby is what one daemon asks another on the "standby" subject.
type remoteStandby struct {
	On   bool   `json:"on"`
	Why  string `json:"why,omitempty"`
	From string `json:"from,omitempty"`
}

// serveRemoteStandby switches this machine's standby when a paired machine asks.
func (d *daemon) serveRemoteStandby(ctx context.Context) {
	b := d.bus.v.Load()
	if b == nil {
		return
	}
	_ = natsx.Serve(ctx, b.Conn(), natsx.RPCSubject(d.df.Machine, "standby"), func(_ context.Context, data []byte) ([]byte, error) {
		var rs remoteStandby
		if err := json.Unmarshal(data, &rs); err != nil {
			return nil, err
		}
		d.log.Info("a paired machine switched this machine's standby", "from", rs.From, "on", rs.On)
		if _, err := d.applyStandby(rs.On, rs.Why); err != nil {
			return nil, err
		}
		return []byte("ok"), nil
	})
}

// applyStandby writes the switch to daemon.yaml first, so a daemon that
// stops now starts the same way, and then applies it to the running loop.
// It returns the sessions stopped.
func (d *daemon) applyStandby(on bool, why string) (int, error) {
	cur, err := daemoncfg.Load()
	if err != nil {
		return 0, err
	}
	if !on {
		why = ""
	}
	if cur.Standby != on || cur.StandbyWhy != why {
		cur.Standby, cur.StandbyWhy = on, why
		if err := cur.Save(); err != nil {
			return 0, fmt.Errorf("standby not kept in %s: %w", paths.DaemonConfig(), err)
		}
	}
	return d.svc.Runtime().SetStandby(on, why), nil
}

// setStandby switches this machine and, through NATS, the named peers.
// setStandby switches this machine (unless peersOnly) and the named peers.
func (d *daemon) setStandby(ctx context.Context, on bool, why string, peers []string, peersOnly bool) (*pb.StandbyResponse, error) {
	stopped := 0
	if !peersOnly {
		var err error
		if stopped, err = d.applyStandby(on, why); err != nil {
			return nil, err
		}
	}
	out := &pb.StandbyResponse{On: on, Stopped: int32(stopped), Peers: make([]*pb.PeerResult, len(peers))}
	body, _ := json.Marshal(remoteStandby{On: on, Why: why, From: d.df.Machine})
	var wg sync.WaitGroup
	for i, name := range peers {
		wg.Go(func() {
			res := &pb.PeerResult{Name: name}
			out.Peers[i] = res
			if d.repl == nil {
				res.Error = "this daemon has no connection to other machines"
				return
			}
			_, err := d.repl.Request(ctx, name, natsx.RPCSubject(name, "standby"), body, 10*time.Second, 0)
			switch {
			case err == nil:
				res.Ok = true
			case errors.Is(err, nats.ErrNoResponders):
				res.Error = "it runs an older Barnraise without standby; run `barnraise standby` there after updating it"
			default:
				res.Error = err.Error()
			}
		})
	}
	wg.Wait()
	return out, nil
}

// peers lists the machines this daemon is paired with.
func (d *daemon) peers() []*pb.Peer {
	if d.repl == nil {
		return nil
	}
	var out []*pb.Peer
	for _, st := range d.repl.Peers() {
		out = append(out, &pb.Peer{Name: st.Name, Connected: st.Connected})
	}
	return out
}

// stopPeers asks the named machines (empty: every connected one) to stop.
func (d *daemon) stopPeers(ctx context.Context, names []string) []*pb.PeerStop {
	if len(names) == 0 {
		for _, p := range d.peers() {
			if p.Connected {
				names = append(names, p.Name)
			}
		}
	}
	out := make([]*pb.PeerStop, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Go(func() {
			res := &pb.PeerStop{Name: name}
			out[i] = res
			if d.repl == nil {
				res.Error = "this daemon has no connection to other machines"
				return
			}
			_, err := d.repl.Request(ctx, name, natsx.RPCSubject(name, "down"), []byte(d.df.Machine), 10*time.Second, 0)
			switch {
			case err == nil:
				res.Stopped = true
			case errors.Is(err, nats.ErrNoResponders):
				res.Error = "it runs an older Barnraise that cannot be stopped from here; run `barnraise down` there"
			default:
				res.Error = err.Error()
			}
		})
	}
	wg.Wait()
	return out
}

// probe returns this machine's capabilities, cached for 30 seconds.
func (d *daemon) probe() domain.Machine {
	if m := d.machine.Load(); m != nil && time.Since(m.LastSeen) < 30*time.Second {
		return *m
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m := sysinfo.Probe(ctx)
	// For tests of what a loaded machine does: BARNRAISE_TEST_LOAD_PCT=95
	// makes this machine report that share of its CPUs busy.
	if v, err := strconv.ParseFloat(os.Getenv("BARNRAISE_TEST_LOAD_PCT"), 64); err == nil && v > 0 {
		m.Load1 = float64(m.CPUs) * v / 100
	}
	m.Name = d.df.Machine
	if free, _ := sysinfo.NewHost().Disk(os.TempDir()); free > 0 {
		m.TmpFreeGB = float64(free) / (1 << 30)
	}
	m.AutoSlots, m.SlotsBy = sysinfo.SlotsWhy(m, 4, m.Emulators)
	m.Slots = m.AutoSlots
	if n := d.maxAgents(); n > 0 {
		m.Slots, m.SlotsBy = n, "owner"
	}
	m.Version = version
	m.Platform = platform()
	m.Labels = append(m.Labels, d.sandboxLabels()...)
	m.Binary = d.binary
	if d.svc != nil {
		m.CapabilityCounts = d.svc.CapabilityCounts()
	}
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
	var plugins notify.Multi
	for _, n := range d.df.Notify {
		if _, ok := domain.PluginRef(n.Type); ok {
			d.log.Warn("notifications: plugins are declared in a project's barnraise.yaml; daemon.yaml cannot use one", "type", n.Type)
			continue
		}
		defs = append(defs, domain.NotifierDef{Type: n.Type, Target: n.Target, Secret: n.Secret})
	}
	for _, root := range d.df.Projects {
		cfg, err := config.Load(root)
		if err != nil {
			continue
		}
		for _, n := range cfg.Notifications {
			if name, ok := domain.PluginRef(n.Type); ok {
				plugins = append(plugins, &pluginNotifier{d: d, project: cfg.Name, name: name, target: n.Target, extra: n.Extra})
				continue
			}
			defs = append(defs, n)
		}
	}
	if len(defs) == 0 && len(plugins) == 0 {
		return nil
	}
	var out notify.Multi
	if len(defs) > 0 {
		n, err := notify.FromDefs(defs, d.secrets)
		if err != nil {
			d.log.Warn("notifications", "err", err)
		} else {
			out = append(out, n)
		}
	}
	out = append(out, plugins...)
	if len(out) == 0 {
		return nil
	}
	return out
}

// pluginNotifier finds the project's plugin when it sends, so a reloaded
// barnraise.yaml takes effect without restarting the daemon.
type pluginNotifier struct {
	d             *daemon
	project, name string
	target        string
	extra         map[string]string
}

func (n *pluginNotifier) Notify(ctx context.Context, x ports.Notification) error {
	h, err := n.d.pluginOf(n.project, n.name)
	if err != nil {
		return err
	}
	return h.Notifier(n.target, n.extra).Notify(ctx, x)
}

// syncPlugins starts hosts for a project's plugins (lazily: the program
// runs with the first call), replaces those whose definition changed and
// stops those that are gone.
func (d *daemon) syncPlugins(cfg *domain.ProjectConfig) {
	d.mu.Lock()
	defer d.mu.Unlock()
	prefix := cfg.Name + "/"
	for key, h := range d.plugins {
		name, ok := strings.CutPrefix(key, prefix)
		if !ok {
			continue
		}
		if def, keep := cfg.Plugins[name]; !keep || !reflect.DeepEqual(def, h.Def()) {
			go func() { _ = h.Close() }()
			delete(d.plugins, key)
		}
	}
	for name, def := range cfg.Plugins {
		if _, ok := d.plugins[prefix+name]; ok {
			continue
		}
		d.plugins[prefix+name] = plugin.New(plugin.Options{
			Name: name, Def: def, Project: cfg.Name, Root: cfg.Root, Version: version,
			LogDir: filepath.Join(d.dataDir, "logs", cfg.Name),
			Secret: func(ref string) (string, error) { return notify.SecretRef(d.secrets, ref) },
			Log:    d.log.With("project", cfg.Name),
		})
	}
}

// pluginOf returns a project's plugin.
func (d *daemon) pluginOf(project, name string) (*plugin.Host, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if h := d.plugins[project+"/"+name]; h != nil {
		return h, nil
	}
	return nil, fmt.Errorf("plugin %q is not declared in the plugins of project %s", name, project)
}

func (d *daemon) closePlugins() {
	d.mu.Lock()
	hosts := make([]*plugin.Host, 0, len(d.plugins))
	for _, h := range d.plugins {
		hosts = append(hosts, h)
	}
	d.plugins = map[string]*plugin.Host{}
	d.mu.Unlock()
	var wg sync.WaitGroup
	for _, h := range hosts {
		wg.Go(func() { _ = h.Close() })
	}
	wg.Wait()
}

// forwardHooks hands each event to the project's hook plugins that asked
// for its type. Delivery never blocks the flow: a slow plugin's queue
// drops events instead.
func (d *daemon) forwardHooks(ctx context.Context) {
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
			if e.LiveOnly() {
				continue // live-only noise
			}
			prefix := e.Project + "/"
			d.mu.Lock()
			var hosts []*plugin.Host
			for key, h := range d.plugins {
				if strings.HasPrefix(key, prefix) && h.Wants(e.Type) {
					hosts = append(hosts, h)
				}
			}
			d.mu.Unlock()
			for _, h := range hosts {
				h.Deliver(e)
			}
		}
	}
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
			if e.LiveOnly() {
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
