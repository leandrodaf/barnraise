package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
