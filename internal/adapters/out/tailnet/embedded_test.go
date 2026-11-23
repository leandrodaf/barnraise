//go:build tsnet

package tailnet

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tailscale.com/net/netns"
	"tailscale.com/tailcfg"
	"tailscale.com/tstest/integration"
	"tailscale.com/tstest/integration/testcontrol"
	"tailscale.com/types/logger"

	"github.com/leandrodaf/barnraise/internal/adapters/out/nats"
	"github.com/leandrodaf/barnraise/internal/adapters/out/pairing"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// startControl runs Tailscale's test control server and a DERP relay on
// loopback: the whole tailnet lives inside the test, no internet needed.
func startControl(t *testing.T) string {
	t.Helper()
	netns.SetEnabled(false)
	t.Cleanup(func() { netns.SetEnabled(true) })
	derp := integration.RunDERPAndSTUN(t, logger.Discard, "127.0.0.1")
	control := &testcontrol.Server{
		DERPMap:        derp,
		DNSConfig:      &tailcfg.DNSConfig{Proxied: true},
		MagicDNSDomain: "tail-scale.ts.net",
		Logf:           logger.Discard,
	}
	control.HTTPTestServer = httptest.NewUnstartedServer(control)
	control.HTTPTestServer.Start()
	t.Cleanup(control.HTTPTestServer.Close)
	return control.HTTPTestServer.URL
}

// Two machines join a test tailnet as embedded devices; one serves NATS
// and a pairing invite on its device, the other replicates its events and
// joins its cluster through its own device.
func TestEmbeddedTwoMachines(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a local Tailscale control server and two embedded devices")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	url := startControl(t)
	root := t.TempDir()
	open := func(name string) *Embedded {
		t.Helper()
		e, err := OpenEmbedded(ctx, Config{Hostname: name, StateDir: filepath.Join(root, name), ControlURL: url, Logf: t.Logf})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { e.Close() })
		return e
	}
	alpha, beta := open("alpha"), open("beta")

	// state dir is private, recorded and locked
	st, err := os.Stat(filepath.Join(root, "alpha"))
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("state dir %v %v", st.Mode(), err)
	}
	if !HasState(filepath.Join(root, "alpha")) {
		t.Fatal("no state written")
	}
	self, err := ReadSelf(filepath.Join(root, "alpha"))
	if err != nil || IPv4(self.IPs) == "" {
		t.Fatalf("self.json %+v %v", self, err)
	}
	if _, err := OpenEmbedded(ctx, Config{Hostname: "alpha", StateDir: filepath.Join(root, "alpha"), ControlURL: url}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second process on the same state: %v", err)
	}
	if !alpha.Available() || alpha.Mode() != ModeTsnet {
		t.Fatal("alpha not available")
	}
	alphaIP := IPv4(self.IPs)

	// beta sees alpha
	for {
		_, peers, err := beta.Status(ctx)
		if err == nil && len(peers) == 1 && IPv4(peers[0].IPs) == alphaIP {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("beta never saw alpha: %+v %v", peers, err)
		case <-time.After(100 * time.Millisecond):
		}
	}

	// NATS on alpha's device, replicated by beta through its device
	seed, pub, err := nats.NewMachineKey()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := nats.StartServer(nats.ServerOptions{Name: "alpha", StoreDir: t.TempDir(), Port: -1, Users: []nats.UserPerm{nats.PeerPermissions(pub, "alpha")}})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown()
	nc, err := srv.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	bus, err := nats.NewBus(ctx, nc, nats.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish(ctx, domain.Event{ID: "e1", Machine: "alpha", Project: "p", Subject: "I-1", Type: "item.created", Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	ln, err := alpha.Listen(ctx, ":4222")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.ServeListener(ln) }()
	defer ln.Close()
	auth, err := nats.NkeyOption(seed)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan domain.Event, 1)
	rep := nats.NewReplicator("beta", func(e domain.Event) error { got <- e; return nil }, nil)
	defer rep.Close()
	if err := rep.AddPeer("alpha", "nats://"+net.JoinHostPort(alphaIP, "4222"), auth, nats.DialerOption(beta.Dial)); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-got:
		if e.ID != "e1" {
			t.Fatal(e)
		}
	case <-ctx.Done():
		t.Fatal("no event replicated over the tailnet")
	}

	// pairing: alpha invites on its device, beta finds it among the candidates
	pl, err := alpha.Listen(ctx, ":47900")
	if err != nil {
		t.Fatal(err)
	}
	code, _, wait, err := pairing.Invite(ctx, pairing.InviteOptions{ListenAddr: "127.0.0.1:0", Payload: []byte("alpha-info"), Listeners: []net.Listener{pl}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := pairing.JoinWith(ctx, pairing.JoinOptions{Code: code, Info: []byte("beta-info"), Candidates: []string{net.JoinHostPort(alphaIP, "47900")}, Dial: beta.Dial})
	if err != nil || string(out) != "alpha-info" {
		t.Fatalf("join over the tailnet: %q %v", out, err)
	}
	if info, err := wait(); err != nil || string(info) != "beta-info" {
		t.Fatalf("invite: %q %v", info, err)
	}

	if err := alpha.Close(); err != nil {
		t.Fatal(err)
	}
	if alpha.Available() || alpha.Close() != nil {
		t.Fatal("closed device still available")
	}
	if _, _, err := alpha.Status(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
