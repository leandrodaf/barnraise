package pairing

import (
	"context"
	"errors"
	"net"
	"testing"
)

// An extra listener (the embedded Tailscale device in the daemon) serves
// the same single-use invite, and a join through a custom dialer tries the
// tailnet candidates in order, skipping the ones nobody answers on.
func TestExtraListenerAndCandidates(t *testing.T) {
	extra, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	code, _, wait := invite(t, InviteOptions{Payload: []byte("cluster"), Listeners: []net.Listener{extra}})

	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := dead.Addr().String()
	dead.Close() // nothing listens here any more

	var dialed []string
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialed = append(dialed, addr)
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	got, err := JoinWith(context.Background(), JoinOptions{Code: code, Info: []byte("me"), Candidates: []string{deadAddr, extra.Addr().String()}, Dial: dial})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "cluster" || len(dialed) != 2 {
		t.Fatalf("got %q, dialed %v", got, dialed)
	}
	if info, err := wait(); err != nil || string(info) != "me" {
		t.Fatalf("%q %v", info, err)
	}
	// single use across listeners: the extra one is closed too
	if _, err := JoinWith(context.Background(), JoinOptions{Addr: extra.Addr().String(), Code: code}); err == nil {
		t.Fatal("invite reused on the extra listener")
	}
}

func TestCandidateWithWrongCodeStops(t *testing.T) {
	_, addr, _ := invite(t, InviteOptions{Code: "111111"})
	_, err := JoinWith(context.Background(), JoinOptions{Code: "222222", Candidates: []string{addr, "127.0.0.1:1"}})
	if !errors.Is(err, ErrWrongCode) {
		t.Fatalf("want the wrong code from the first machine that answered, got %v", err)
	}
}

// A machine that accepts and hangs up without answering (a daemon relaying
// the pairing port while no invite runs) is skipped like one that refuses.
func TestCandidateThatHangsUpIsSkipped(t *testing.T) {
	hang, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hang.Close()
	go func() {
		for {
			c, err := hang.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	code, addr, _ := invite(t, InviteOptions{Payload: []byte("ok")})
	got, err := JoinWith(context.Background(), JoinOptions{Code: code, Candidates: []string{hang.Addr().String(), addr}})
	if err != nil || string(got) != "ok" {
		t.Fatalf("%q %v", got, err)
	}
}
