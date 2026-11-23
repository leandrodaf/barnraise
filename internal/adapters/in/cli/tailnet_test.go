package cli

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/tailnet"
	"github.com/leandrodaf/barnraise/internal/daemoncfg"
)

func tailnetEnv(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("BARNRAISE_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("BARNRAISE_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("BARNRAISE_SOCKET", filepath.Join(dir, "none.sock"))
	t.Setenv("BARNRAISE_NO_TAILSCALE", "")
	for _, k := range tailnet.AuthKeyEnv {
		t.Setenv(k, "")
	}
}

func TestTailnetConfig(t *testing.T) {
	tailnetEnv(t)
	t.Setenv("BARNRAISE_TS_AUTHKEY", "tskey-auth-test")
	df := &daemoncfg.File{Machine: "pc-alpha", Network: "tsnet", TSNet: daemoncfg.TSNet{ControlURL: "https://headscale.example", Tags: []string{"tag:barnraise", "tag:home"}}}
	c := TailnetConfig(df, nil)
	if c.Mode != tailnet.ModeTsnet || c.Hostname != "pc-alpha" || c.ControlURL != "https://headscale.example" || len(c.Tags) != 2 || c.Disabled {
		t.Fatalf("%+v", c)
	}
	if c.StateDir != filepath.Join(os.Getenv("BARNRAISE_DATA_DIR"), "tsnet") {
		t.Fatal(c.StateDir)
	}
	if c.AuthKey() != "tskey-auth-test" {
		t.Fatal("auth key from the environment")
	}
	t.Setenv("BARNRAISE_NO_TAILSCALE", "1")
	if m, err := TailnetConfig(df, nil).Resolve(); err != nil || m != tailnet.ModeLAN {
		t.Fatal(m, err)
	}
	// an invalid network is kept, so Resolve reports it
	if _, err := TailnetConfig(&daemoncfg.File{Network: "wifi"}, nil).Resolve(); err != nil {
		t.Fatal("disabled wins over everything", err)
	}
	t.Setenv("BARNRAISE_NO_TAILSCALE", "")
	if _, err := TailnetConfig(&daemoncfg.File{Network: "wifi"}, nil).Resolve(); err == nil {
		t.Fatal("invalid network accepted")
	}
}

func TestPairingNetOnTheLocalNetwork(t *testing.T) {
	tailnetEnv(t)
	e := &env{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	pn, err := e.pairingNet(context.Background(), &daemoncfg.File{Machine: "a", Network: "lan", NATSHost: "192.168.1.5"}, true)
	if err != nil || pn.tn != nil || pn.own || pn.host != "192.168.1.5" {
		t.Fatal(pn, err)
	}
	if pn.candidates(context.Background(), "a") != nil {
		t.Fatal("candidates without a tailnet")
	}
	pn.Close()
	if !tailnet.Embeddable {
		if _, err := e.pairingNet(context.Background(), &daemoncfg.File{Machine: "a", Network: "tsnet"}, false); err == nil {
			t.Fatal("tsnet without the build tag")
		}
	}
}

type fakeTailnet struct {
	self   tailnet.Self
	peers  []tailnet.Peer
	closed bool
}

func (f *fakeTailnet) Mode() tailnet.Mode { return tailnet.ModeTsnet }
func (f *fakeTailnet) Available() bool    { return true }
func (f *fakeTailnet) Status(context.Context) (tailnet.Self, []tailnet.Peer, error) {
	return f.self, f.peers, nil
}
func (f *fakeTailnet) Listen(context.Context, string) (net.Listener, error) { return nil, nil }
func (f *fakeTailnet) Dial(context.Context, string, string) (net.Conn, error) {
	return nil, nil
}
func (f *fakeTailnet) Close() error { f.closed = true; return nil }

func TestPairingCandidates(t *testing.T) {
	tag := []string{tailnet.Tag}
	f := &fakeTailnet{peers: []tailnet.Peer{
		{Name: "beta", IPs: []string{"100.64.0.2", "fd7a::2"}, Online: true, Tags: tag},
		{Name: "gamma", IPs: []string{"100.64.0.3"}, Online: false, Tags: tag}, // offline
		{Name: "phone", IPs: []string{"100.64.0.4"}, Online: true},             // not Barnraise
		{Name: "alpha", IPs: []string{"100.64.0.1"}, Online: true, Tags: tag},  // this machine's daemon
		{Name: "alpha-pairing", IPs: []string{"100.64.0.9"}, Online: true, Tags: tag},
		{Name: "delta", IPs: []string{"100.64.0.5"}, Online: true, Tags: tag},
	}}
	cleaned := false
	pn := &pairNet{tn: f, host: "100.64.0.1", cleanup: func() { cleaned = true }}
	got := strings.Join(pn.candidates(context.Background(), "alpha"), " ")
	if got != "100.64.0.2:47900 100.64.0.5:47900" {
		t.Fatal(got)
	}
	pn.Close()
	pn.Close()
	if !f.closed || !cleaned || pn.tn != nil {
		t.Fatal("close")
	}
}

func TestNetworkCommand(t *testing.T) {
	tailnetEnv(t)
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		e := &env{out: &out, errOut: &out, in: strings.NewReader("")}
		c := e.machineCmd()
		c.SetArgs(args)
		c.SetOut(&out)
		c.SetErr(&out)
		err := c.Execute()
		return out.String(), err
	}
	out, err := run("network", "lan")
	if err != nil || !strings.Contains(out, "network: lan") {
		t.Fatal(out, err)
	}
	df, err := daemoncfg.Load()
	if err != nil || df.Network != "lan" {
		t.Fatal(df.Network, err)
	}
	if _, err := run("network", "wifi"); err == nil {
		t.Fatal("unknown network saved")
	}
	out, err = run("network", "lan", "--control-url", "https://headscale.example")
	if err != nil {
		t.Fatal(out, err)
	}
	if df, _ = daemoncfg.Load(); df.TSNet.ControlURL != "https://headscale.example" {
		t.Fatal(df.TSNet)
	}
	_, err = run("network", "tsnet")
	if tailnet.Embeddable {
		if err != nil {
			t.Fatal(err)
		}
		out, _ = run("network")
		if !strings.Contains(out, "control server: https://headscale.example") || !strings.Contains(out, "auth key: not set") {
			t.Fatal(out)
		}
	} else if err == nil {
		t.Fatal("tsnet chosen without the build tag")
	}
	if _, err := run("network-key"); err == nil {
		t.Fatal("empty auth key accepted")
	}
}

var (
	netPluginOnce sync.Once
	netPluginPath string
	netPluginErr  error
)

// netPlugin builds the plugin host's contract plugin (it implements network.status).
func netPlugin(t *testing.T) string {
	t.Helper()
	netPluginOnce.Do(func() {
		dir, err := os.MkdirTemp("", "barnraise-cli-netplugin")
		if err != nil {
			netPluginErr = err
			return
		}
		netPluginPath = filepath.Join(dir, "testplugin")
		if out, err := exec.Command("go", "build", "-o", netPluginPath, "../../out/plugin/testdata/testplugin").CombinedOutput(); err != nil {
			netPluginErr = errors.New(err.Error() + ": " + string(out))
		}
	})
	if netPluginErr != nil {
		t.Fatal(netPluginErr)
	}
	return netPluginPath
}

func TestTailnetConfigPlugin(t *testing.T) {
	tailnetEnv(t)
	df := &daemoncfg.File{Machine: "alpha", Network: "plugin", NetworkPlugin: &daemoncfg.NetworkPlugin{Command: "/opt/zt-network", Args: []string{"-n", "x"}, Secrets: map[string]string{"ZT_TOKEN": "env:ZT_TOKEN_FOR_TEST"}, Timeout: "3s"}}
	c := TailnetConfig(df, nil)
	if c.Mode != tailnet.ModePlugin || c.PluginErr != nil || c.Plugin.Name != "network" || c.Plugin.Def.Command != "/opt/zt-network" || c.Plugin.Def.Timeout != 3*time.Second || c.Plugin.Root == "" {
		t.Fatalf("%+v", c)
	}
	if m, err := c.Resolve(); err != nil || m != tailnet.ModePlugin {
		t.Fatal(m, err)
	}
	t.Setenv("ZT_TOKEN_FOR_TEST", "zt-secret")
	if v, err := c.Plugin.Secret("env:ZT_TOKEN_FOR_TEST"); err != nil || v != "zt-secret" {
		t.Fatal(v, err)
	}
	if _, err := c.Plugin.Secret("env:ZT_TOKEN_UNSET_FOR_TEST"); err == nil {
		t.Fatal("an empty variable is not a secret")
	}
	df.NetworkPlugin.Secrets["ZT_TOKEN"] = "the-value-itself"
	if _, err := TailnetConfig(df, nil).Resolve(); err == nil || !strings.Contains(err.Error(), "must be a reference") {
		t.Fatal(err)
	}
	if _, err := TailnetConfig(&daemoncfg.File{Network: "plugin"}, nil).Resolve(); !errors.Is(err, tailnet.ErrNoPlugin) {
		t.Fatal(err)
	}
}

func TestPairingNetWithANetworkPlugin(t *testing.T) {
	tailnetEnv(t)
	bin := netPlugin(t)
	e := &env{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	df := &daemoncfg.File{Machine: "alpha", Network: "plugin", NATSHost: "127.0.0.1", NetworkPlugin: &daemoncfg.NetworkPlugin{Command: bin, Args: []string{"-self=10.147.17.1"}}}
	pn, err := e.pairingNet(context.Background(), df, true)
	if err != nil || pn.tn == nil || pn.own || pn.host != "10.147.17.1" {
		t.Fatal(pn, err, e.errOut)
	}
	// beta is online and tagged; gamma is offline, the printer untagged
	if got := strings.Join(pn.candidates(context.Background(), "alpha"), " "); got != "10.147.17.2:47900" {
		t.Fatal(got)
	}
	pn.Close()

	// a plugin that fails leaves pairing on the local network
	errOut := &bytes.Buffer{}
	e = &env{out: &bytes.Buffer{}, errOut: errOut}
	df.NetworkPlugin.Args = []string{"-network=error"}
	pn, err = e.pairingNet(context.Background(), df, true)
	if err != nil || pn.tn != nil || pn.host != "127.0.0.1" || !strings.Contains(errOut.String(), "The network plugin is not available") {
		t.Fatal(pn, err, errOut.String())
	}
	pn.Close()
}

func TestNetworkCommandPlugin(t *testing.T) {
	tailnetEnv(t)
	bin := netPlugin(t)
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		e := &env{out: &out, errOut: &out, in: strings.NewReader("")}
		c := e.machineCmd()
		c.SetArgs(args)
		c.SetOut(&out)
		c.SetErr(&out)
		err := c.Execute()
		return out.String(), err
	}
	if _, err := run("network", "plugin"); !errors.Is(err, tailnet.ErrNoPlugin) {
		t.Fatal("plugin without a command:", err)
	}
	if df, _ := daemoncfg.Load(); df.Network != "" {
		t.Fatal("a refused choice was saved:", df.Network)
	}
	if _, err := run("network", "plugin", "--command", bin, "--secret", "ZT_TOKEN=abc"); err == nil || !strings.Contains(err.Error(), "must be a reference") {
		t.Fatal("a secret value accepted:", err)
	}
	if _, err := run("network", "plugin", "--command", bin, "--secret", "ZT_TOKEN"); err == nil {
		t.Fatal("a secret without a reference accepted")
	}
	out, err := run("network", "plugin", "--command", bin, "--arg", "-kinds=network", "--env", "ZT_HOME", "--secret", "ZT_TOKEN=keychain:zerotier", "--timeout", "4s")
	if err != nil || !strings.Contains(out, "network: plugin") || !strings.Contains(out, "secret: ZT_TOKEN from keychain:zerotier") {
		t.Fatal(out, err)
	}
	df, _ := daemoncfg.Load()
	if df.Network != "plugin" || df.NetworkPlugin == nil || df.NetworkPlugin.Command != bin || df.NetworkPlugin.Args[0] != "-kinds=network" || df.NetworkPlugin.Env[0] != "ZT_HOME" || df.NetworkPlugin.Timeout != "4s" {
		t.Fatalf("%+v", df.NetworkPlugin)
	}
	// the keychain entry does not exist: the plugin cannot start, and the command says so
	if !strings.Contains(out, "status: not available") || !strings.Contains(out, "secret ZT_TOKEN") {
		t.Fatal(out)
	}
	// without the secret the plugin answers
	df.NetworkPlugin.Secrets = nil
	if err := df.Save(); err != nil {
		t.Fatal(err)
	}
	out, err = run("network")
	if err != nil || !strings.Contains(out, "this machine: alpha 127.0.0.1 fd00::1") || !strings.Contains(out, "peers: 3, 1 of them online with tag:barnraise") {
		t.Fatal(out, err)
	}
	// a relative program path is stored absolute
	dir := t.TempDir()
	t.Chdir(dir)
	if out, err := run("network", "--command", "./bin/zt-network"); err != nil {
		t.Fatal(out, err)
	}
	if df, _ = daemoncfg.Load(); df.NetworkPlugin.Command != filepath.Join(dir, "bin", "zt-network") {
		t.Fatal(df.NetworkPlugin.Command)
	}
}
