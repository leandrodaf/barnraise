package tailnet

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/plugin"
	"github.com/leandrodaf/barnraise/internal/domain"
)

var (
	testPluginOnce sync.Once
	testPluginPath string
	testPluginErr  error
)

// testPlugin builds the plugin host's contract plugin, which implements
// network.status.
func testPlugin(t *testing.T) string {
	t.Helper()
	testPluginOnce.Do(func() {
		dir, err := os.MkdirTemp("", "barnraise-netplugin")
		if err != nil {
			testPluginErr = err
			return
		}
		testPluginPath = filepath.Join(dir, "testplugin")
		if runtime.GOOS == "windows" {
			testPluginPath += ".exe"
		}
		out, err := exec.Command("go", "build", "-o", testPluginPath, "../plugin/testdata/testplugin").CombinedOutput()
		if err != nil {
			testPluginErr = errors.New(err.Error() + ": " + string(out))
		}
	})
	if testPluginErr != nil {
		t.Fatal(testPluginErr)
	}
	return testPluginPath
}

func pluginConfig(t *testing.T, command string, args ...string) Config {
	t.Helper()
	env := map[string]string{"PATH": os.Getenv("PATH"), "HOME": t.TempDir()}
	return Config{Mode: ModePlugin, LookPath: found, Plugin: plugin.Options{
		Def:    domain.PluginDef{Command: command, Args: args, Timeout: 5 * time.Second},
		Root:   t.TempDir(),
		Getenv: func(k string) string { return env[k] },
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Grace:  200 * time.Millisecond, Backoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
	}}
}

func TestParseModePlugin(t *testing.T) {
	if m, err := ParseMode(" Plugin "); err != nil || m != ModePlugin {
		t.Fatal(m, err)
	}
}

func TestResolvePlugin(t *testing.T) {
	withPlugin := Config{Mode: ModePlugin, LookPath: found, Plugin: plugin.Options{Def: domain.PluginDef{Command: "zt-network"}}}
	if m, err := withPlugin.Resolve(); err != nil || m != ModePlugin {
		t.Fatal("explicit plugin wins over the installed tailscale:", m, err)
	}
	if _, err := (Config{Mode: ModePlugin}).Resolve(); !errors.Is(err, ErrNoPlugin) {
		t.Fatal("plugin without a command:", err)
	}
	bad := withPlugin
	bad.PluginErr = errors.New("network_plugin: secret TOKEN must be a reference")
	if _, err := bad.Resolve(); err == nil || !strings.Contains(err.Error(), "must be a reference") {
		t.Fatal(err)
	}
	auto := withPlugin
	auto.Mode = ModeAuto
	if m, _ := auto.Resolve(); m != ModeTailscale {
		t.Fatal("auto never picks the plugin:", m)
	}
	auto.LookPath = missing
	auto.StateDir = t.TempDir()
	if m, _ := auto.Resolve(); m != ModeLAN {
		t.Fatal("auto without tailscale is the local network even with a plugin:", m)
	}
	disabled := withPlugin
	disabled.Disabled = true
	if m, _ := disabled.Resolve(); m != ModeLAN {
		t.Fatal("BARNRAISE_NO_TAILSCALE turns the plugin off too:", m)
	}
}

func TestFromPlugin(t *testing.T) {
	self, peers := FromPlugin(plugin.NetworkStatus{
		Self: plugin.NetworkSelf{Name: "alpha", Addresses: []string{"fd00::1", "10.0.0.1"}, Tags: []string{Tag}},
		Peers: []plugin.NetworkPeer{
			{Name: "gamma", Addresses: []string{"10.0.0.3"}, Online: true, Tags: []string{Tag}},
			{Name: "beta", Addresses: []string{"10.0.0.2"}, Online: false, Tags: []string{Tag}},
			{Name: "printer", Addresses: []string{"10.0.0.9"}, Online: true},
		},
	})
	if self.Name != "alpha" || !self.Online || IPv4(self.IPs) != "10.0.0.1" || len(self.Tags) != 1 {
		t.Fatalf("self %+v", self)
	}
	if len(peers) != 3 || peers[0].Name != "beta" || peers[0].Online || peers[2].Name != "printer" {
		t.Fatalf("peers %+v", peers)
	}
	br := BarnraisePeers(peers)
	if len(br) != 2 || br[0].Name != "beta" || br[1].Name != "gamma" {
		t.Fatalf("barnraise peers %+v", br)
	}
	if _, none := FromPlugin(plugin.NetworkStatus{Self: plugin.NetworkSelf{Name: "a"}}); none == nil || len(none) != 0 {
		t.Fatal("no peers is an empty list")
	}
}

func TestPluginNetwork(t *testing.T) {
	tn, m, err := Open(context.Background(), pluginConfig(t, testPlugin(t)))
	if err != nil || m != ModePlugin || tn.Mode() != ModePlugin {
		t.Fatal(tn, m, err)
	}
	defer tn.Close()
	if !tn.Available() {
		t.Fatal("available")
	}
	self, peers, err := tn.Status(context.Background())
	if err != nil || self.Name != "alpha" || IPv4(self.IPs) != "127.0.0.1" {
		t.Fatal(self, err)
	}
	if len(peers) != 3 || peers[0].Name != "beta" || IPv4(peers[0].IPs) != "10.147.17.2" {
		t.Fatalf("peers %+v", peers)
	}
	br := BarnraisePeers(peers)
	if len(br) != 2 || br[0].Name != "beta" || !br[0].Online || br[1].Online {
		t.Fatalf("barnraise peers %+v", br)
	}

	// ":port" listens on the plugin's address for this machine, not on every interface.
	ln, err := tn.Listen(context.Background(), ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if !strings.HasPrefix(ln.Addr().String(), "127.0.0.1:") {
		t.Fatal(ln.Addr())
	}
	go func() {
		if c, err := ln.Accept(); err == nil {
			_, _ = c.Write([]byte("ok"))
			c.Close()
		}
	}()
	c, err := tn.Dial(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ok" {
		t.Fatal(string(buf), err)
	}
	c.Close()

	if err := tn.Close(); err != nil {
		t.Fatal(err)
	}
	if tn.Close() != nil || tn.Available() {
		t.Fatal("closed twice, or available after close")
	}
	if _, _, err := tn.Status(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := tn.Listen(context.Background(), ":0"); err == nil {
		t.Fatal("listen on a stopped plugin")
	}
}

// A plugin that is missing, answers badly or too late leaves the machine
// on the local network: Open fails with the mode, as the daemon expects.
func TestPluginNetworkFailures(t *testing.T) {
	bin := testPlugin(t)
	for name, c := range map[string]struct {
		cfg  Config
		want string
	}{
		"missing program":   {pluginConfig(t, filepath.Join(t.TempDir(), "no-such-plugin")), "cannot start"},
		"not a network one": {pluginConfig(t, bin, "-kinds=tracker"), "does not implement network"},
		"no address":        {pluginConfig(t, bin, "-network=noself"), "no address for this machine"},
		"bad address":       {pluginConfig(t, bin, "-network=badaddr"), "not an IP address"},
		"an error":          {pluginConfig(t, bin, "-network=error"), "the overlay is down"},
		"timeout": {func() Config {
			c := pluginConfig(t, bin, "-network=hang")
			c.Plugin.Def.Timeout = 200 * time.Millisecond
			return c
		}(), "did not answer network.status"},
	} {
		t.Run(name, func(t *testing.T) {
			tn, m, err := Open(context.Background(), c.cfg)
			if tn != nil || m != ModePlugin || err == nil || !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v %q %v, want an error with %q", tn, m, err, c.want)
			}
		})
	}
}

// The static example of docs/examples/plugins runs against the real host.
func TestStaticNetworkExample(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	script, err := filepath.Abs("../../../../docs/examples/plugins/network-static.py")
	if err != nil {
		t.Fatal(err)
	}
	peers := filepath.Join(t.TempDir(), "peers.json")
	data := `{
  "self": "alpha",
  "machines": [
    {"name": "alpha", "addresses": ["127.0.0.1"], "tags": ["tag:barnraise"]},
    {"name": "beta", "addresses": ["127.0.0.1"], "tags": ["tag:barnraise"], "probe": 1},
    {"name": "printer", "addresses": ["10.9.0.9"]}
  ]
}`
	if err := os.WriteFile(peers, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := pluginConfig(t, py, script)
	cfg.Plugin.Def.Options = map[string]any{"file": peers}
	tn, _, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tn.Close()
	self, list, err := tn.Status(context.Background())
	if err != nil || self.Name != "alpha" || IPv4(self.IPs) != "127.0.0.1" || len(list) != 2 {
		t.Fatalf("%+v %+v %v", self, list, err)
	}
	br := BarnraisePeers(list)
	if len(br) != 1 || br[0].Name != "beta" {
		t.Fatalf("%+v", br)
	}
	// beta's probe port (1) is closed here: the example reports it offline.
	if br[0].Online {
		t.Fatal("beta should be offline")
	}
	if list[1].Name != "printer" || !list[1].Online {
		t.Fatalf("a machine without a probe port is online: %+v", list[1])
	}
	ln, err := tn.Listen(context.Background(), ":0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()

	// hosts-like lines; the file is read on every call
	hosts := "# wireguard\n10.8.0.2 beta tag:barnraise\n127.0.0.1 alpha tag:barnraise\n"
	if err := os.WriteFile(peers, []byte(hosts), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Plugin.Def.Options["self"] = "alpha"
	tn2, _, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tn2.Close()
	self, list, err = tn2.Status(context.Background())
	if err != nil || IPv4(self.IPs) != "127.0.0.1" || len(list) != 1 || list[0].Name != "beta" || !list[0].Online || !list[0].HasTag(Tag) {
		t.Fatalf("%+v %+v %v", self, list, err)
	}
	// a file without this machine is a bad answer: the network is unavailable
	if err := os.WriteFile(peers, []byte("10.8.0.2 beta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tn2.Status(context.Background()); err == nil || !strings.Contains(err.Error(), "not in the file") {
		t.Fatal(err)
	}
}
