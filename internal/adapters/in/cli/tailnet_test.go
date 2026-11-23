package cli

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
