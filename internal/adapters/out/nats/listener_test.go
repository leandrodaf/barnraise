package nats

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// A listener the server does not own (here plain TCP, in the daemon the
// embedded Tailscale device) carries peers to the server with the same
// authentication and permissions, and DialerOption routes clients through
// a custom dialer.
func TestServeListenerAndCustomDialer(t *testing.T) {
	seed, pub, err := NewMachineKey()
	if err != nil {
		t.Fatal(err)
	}
	a := startMachine(t, "alpha", t.TempDir(), -1, PeerPermissions(pub, "alpha"))
	defer a.stop()
	if err := a.bus.Publish(context.Background(), ev("e1", "alpha", "I-1", 1)); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.srv.ServeListener(ln) }()

	var dials atomic.Int32
	dial := DialerOption(func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials.Add(1)
		var d net.Dialer
		return d.DialContext(ctx, network, ln.Addr().String()) // the name in the URL is not routable
	})
	url := "nats://alpha.tailnet.invalid:4222"

	if nc, err := nats.Connect(url, dial, nats.Timeout(2*time.Second)); err == nil {
		nc.Close()
		t.Fatal("anonymous connection accepted through the listener")
	}
	auth, err := NkeyOption(seed)
	if err != nil {
		t.Fatal(err)
	}
	rec := newRecorder()
	rep := NewReplicator("beta", rec.apply, nil)
	defer rep.Close()
	if err := rep.AddPeer("alpha", url, auth, dial); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "replicated through the listener", 10*time.Second, func() bool { return rec.len() == 1 })
	if dials.Load() < 2 {
		t.Fatalf("custom dialer used %d times", dials.Load())
	}

	ln.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeListener did not return after the listener closed")
	}
}
