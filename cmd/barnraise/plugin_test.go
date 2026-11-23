package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/keychain"
	"github.com/leandrodaf/barnraise/internal/adapters/out/notify"
	"github.com/leandrodaf/barnraise/internal/adapters/out/plugin"
	"github.com/leandrodaf/barnraise/internal/daemoncfg"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

func pluginDaemon(t *testing.T) *daemon {
	t.Helper()
	d := &daemon{log: slog.New(slog.NewTextHandler(io.Discard, nil)), df: &daemoncfg.File{}, plugins: map[string]*plugin.Host{}, dataDir: t.TempDir()}
	d.secrets = keychain.New("barnraise-test", t.TempDir(), keychain.ForceFile())
	t.Cleanup(d.closePlugins)
	return d
}

// Plugins follow barnraise.yaml: a changed definition gets a new host, a
// removed one is stopped, and checks and notifications find the current one.
func TestPluginHostsFollowTheConfig(t *testing.T) {
	d := pluginDaemon(t)
	cfg := &domain.ProjectConfig{Name: "shop", Root: t.TempDir(), Plugins: map[string]domain.PluginDef{
		"yt":   {Command: "yt-plugin", Timeout: time.Second},
		"farm": {Command: "farm-plugin", Timeout: time.Second},
	}}
	d.syncPlugins(cfg)
	yt, err := d.pluginOf("shop", "yt")
	if err != nil {
		t.Fatal(err)
	}
	d.syncPlugins(cfg)
	if again, _ := d.pluginOf("shop", "yt"); again != yt {
		t.Fatal("an unchanged plugin must keep its host")
	}
	cfg.Plugins = map[string]domain.PluginDef{"yt": {Command: "yt-plugin", Args: []string{"-v"}, Timeout: time.Second}}
	d.syncPlugins(cfg)
	if again, _ := d.pluginOf("shop", "yt"); again == yt || again.Def().Args[0] != "-v" {
		t.Fatal("a changed plugin must get a new host")
	}
	if _, err := d.pluginOf("shop", "farm"); err == nil || !strings.Contains(err.Error(), `plugin "farm" is not declared in the plugins of project shop`) {
		t.Fatalf("err = %v", err)
	}
	// A check on a plugin goes through the project's runner.
	run := d.newRunner(cfg, nil)
	_, _, err = run.Run(context.Background(), ports.CheckSpec{Def: domain.CheckDef{Name: "devices", RunsOn: "plugin:farm"}})
	if err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("err = %v", err)
	}
	_, _, err = run.Run(context.Background(), ports.CheckSpec{Def: domain.CheckDef{Name: "devices", RunsOn: "plugin:yt"}})
	if err == nil || !strings.Contains(err.Error(), "cannot start yt-plugin") {
		t.Fatalf("err = %v", err)
	}
}

func TestNotifierRoutesPluginChannels(t *testing.T) {
	d := pluginDaemon(t)
	root := t.TempDir()
	yml := "project: shop\ntemplate: blank\nplugins:\n  ops: { command: ops-plugin }\nnotifications:\n  - type: plugin:ops\n    target: '#ops'\n"
	if err := os.WriteFile(filepath.Join(root, "barnraise.yaml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	d.df.Projects = []string{root}
	d.df.Notify = append(d.df.Notify, struct {
		Type   string `yaml:"type"`
		Target string `yaml:"target"`
		Secret string `yaml:"secret,omitempty"`
	}{Type: "plugin:ops"})
	n := d.notifier()
	m, ok := n.(notify.Multi)
	if !ok || len(m) != 1 {
		t.Fatalf("notifier = %#v", n)
	}
	if err := n.Notify(context.Background(), ports.Notification{Title: "x"}); err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("err = %v", err)
	}
	if pn, ok := m[0].(*pluginNotifier); !ok || pn.target != "#ops" || pn.project != "shop" {
		t.Fatalf("channel = %#v", m[0])
	}
}

// syncLog is a log sink safe for the plugin's reader goroutines.
type syncLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncLog) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncLog) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// A network plugin that cannot start leaves the daemon on the local
// network, as an embedded Tailscale that cannot log in does; one that
// answers gives the daemon its address.
func TestNetworkPluginInTheDaemon(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BARNRAISE_CONFIG_DIR", filepath.Join(dir, "cfg"))
	t.Setenv("BARNRAISE_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("BARNRAISE_NO_TAILSCALE", "")
	logs := &syncLog{}
	d := pluginDaemon(t)
	d.log = slog.New(slog.NewTextHandler(logs, nil))
	d.df = &daemoncfg.File{Machine: "alpha", Network: "plugin", NetworkPlugin: &daemoncfg.NetworkPlugin{Command: filepath.Join(dir, "no-such-network-plugin")}}
	d.openTailnet(context.Background())
	if d.tn != nil || !strings.Contains(logs.String(), "tailnet not available") || !strings.Contains(logs.String(), "network plugin is not available") {
		t.Fatal(d.tn, logs.String())
	}

	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	script, _ := filepath.Abs("../../docs/examples/plugins/network-static.py")
	peers := filepath.Join(dir, "peers")
	if err := os.WriteFile(peers, []byte("127.0.0.1 alpha tag:barnraise\n10.8.0.2 beta tag:barnraise\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.df.NetworkPlugin = &daemoncfg.NetworkPlugin{Command: py, Args: []string{script}, Options: map[string]any{"file": peers, "self": "alpha"}}
	d.openTailnet(context.Background())
	defer d.closeTailnet()
	if d.tn == nil || d.embedded() || !d.tn.Available() || !strings.Contains(logs.String(), "network plugin up") {
		t.Fatal(logs.String())
	}
}
