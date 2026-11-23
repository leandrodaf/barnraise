package tailnet

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/status.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fake(out []byte, err error) *Network {
	return &Network{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tailscale" || strings.Join(args, " ") != "status --json" {
			return nil, errors.New("unexpected command")
		}
		return out, err
	}}
}

func TestParseStatus(t *testing.T) {
	self, peers, err := ParseStatus(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if self.Name != "pc-alpha" || self.DNSName != "pc-alpha.tail0000.ts.net" || !self.Online || IPv4(self.IPs) != "100.101.1.10" {
		t.Fatalf("self %+v", self)
	}
	if len(peers) != 2 || peers[0].Name != "pc-beta" || peers[1].Name != "phone" {
		t.Fatalf("peers %+v", peers)
	}
	if !peers[0].Online || peers[1].Online || peers[1].OS != "android" || peers[0].LastSeen.IsZero() {
		t.Fatalf("peer fields %+v", peers)
	}
	br := BarnraisePeers(peers)
	if len(br) != 1 || br[0].Name != "pc-beta" || IPv4(br[0].IPs) != "100.101.1.11" {
		t.Fatalf("barnraise peers %+v", br)
	}
}

func TestStatusStopped(t *testing.T) {
	_, _, err := ParseStatus([]byte(`{"BackendState":"Stopped","Self":null}`))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if fake([]byte(`{"BackendState":"NeedsLogin"}`), nil).Available() {
		t.Fatal("not running must not be available")
	}
	if _, _, err := ParseStatus([]byte("nope")); err == nil {
		t.Fatal("junk accepted")
	}
}

func TestNetworkWithFakeCommand(t *testing.T) {
	n := fake(fixture(t), nil)
	if !n.Available() {
		t.Fatal("available")
	}
	self, peers, err := n.Status(context.Background())
	if err != nil || self.Name != "pc-alpha" || len(peers) != 2 {
		t.Fatal(self, peers, err)
	}
	if fake(nil, errors.New("not installed")).Available() {
		t.Fatal("missing command must not be available")
	}
	missing := &Network{Bin: "tailscale-definitely-missing"}
	if missing.Available() {
		t.Fatal("missing binary")
	}
	if _, _, err := missing.Status(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestListen(t *testing.T) {
	// explicit host: used as given
	ln, err := fake(nil, errors.New("unused")).Listen(context.Background(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	// empty host: the tailscale IPv4, here a loopback address from a fake status
	st := `{"BackendState":"Running","Self":{"HostName":"x","Online":true,"TailscaleIPs":["127.0.0.1","::1"]}}`
	ln, err = fake([]byte(st), nil).Listen(context.Background(), ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if !strings.HasPrefix(ln.Addr().String(), "127.0.0.1:") {
		t.Fatal(ln.Addr())
	}
	if _, err := fake([]byte(`{"BackendState":"Stopped"}`), nil).Listen(context.Background(), ":0"); err == nil {
		t.Fatal("listen without tailscale")
	}
}

func TestACLSnippet(t *testing.T) {
	s := ACLSnippet("", 4222)
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, `"tag:barnraise:4222"`) || !strings.Contains(s, "autogroup:admin") || !strings.Contains(s, `"tagOwners"`) {
		t.Fatal(s)
	}
	if !strings.Contains(ACLSnippet("owner@example.com"), "tag:barnraise:47900") {
		t.Fatal("default ports")
	}
}
