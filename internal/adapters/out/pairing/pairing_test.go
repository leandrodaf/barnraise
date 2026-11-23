package pairing

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"filippo.io/cpace"
)

func invite(t *testing.T, opts InviteOptions) (string, string, func() ([]byte, error)) {
	t.Helper()
	if opts.ListenAddr == "" {
		opts.ListenAddr = "127.0.0.1:0"
	}
	code, addr, wait, err := Invite(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return code, addr, wait
}

func TestCodes(t *testing.T) {
	for range 100 {
		c, err := NewCode()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NormalizeCode(c); err != nil {
			t.Fatalf("generated %q: %v", c, err)
		}
	}
	if c, _ := NormalizeCode("482 913"); c != "482913" {
		t.Fatal(c)
	}
	if FormatCode("482913") != "482 913" {
		t.Fatal(FormatCode("482913"))
	}
	for _, bad := range []string{"", "12345", "1234567", "12a456"} {
		if _, err := NormalizeCode(bad); !errors.Is(err, ErrBadCode) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPairingSuccess(t *testing.T) {
	var joined []byte
	code, addr, wait := invite(t, InviteOptions{Payload: []byte("cluster-info"), OnJoined: func(b []byte) { joined = b }})
	if len(code) != 6 {
		t.Fatalf("code %q", code)
	}
	got, err := Join(context.Background(), addr, FormatCode(code), []byte("machine-info"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "cluster-info" {
		t.Fatalf("joiner got %q", got)
	}
	info, err := wait()
	if err != nil {
		t.Fatal(err)
	}
	if string(info) != "machine-info" || string(joined) != "machine-info" {
		t.Fatalf("inviter got %q / %q", info, joined)
	}
	// single use
	if _, err := Join(context.Background(), addr, code, nil); err == nil {
		t.Fatal("invite reused")
	}
}

func TestWrongCodeFailsBothSides(t *testing.T) {
	var fails atomic.Int32
	_, addr, wait := invite(t, InviteOptions{Code: "111111", Payload: []byte("secret"), OnFailed: func(err error) {
		if errors.Is(err, ErrWrongCode) {
			fails.Add(1)
		}
	}})
	if _, err := Join(context.Background(), addr, "222222", []byte("x")); !errors.Is(err, ErrWrongCode) {
		t.Fatalf("joiner: %v", err)
	}
	if fails.Load() != 1 {
		t.Fatalf("inviter failures %d", fails.Load())
	}
	// the right code still works afterwards
	got, err := Join(context.Background(), addr, "111111", []byte("x"))
	if err != nil || string(got) != "secret" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := wait(); err != nil {
		t.Fatal(err)
	}
}

func TestLockoutAfterThreeFailures(t *testing.T) {
	_, addr, wait := invite(t, InviteOptions{Code: "123456"})
	for i := range MaxFailedAttempts {
		if _, err := Join(context.Background(), addr, "654321", nil); !errors.Is(err, ErrWrongCode) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := wait(); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("wait: %v", err)
	}
	if _, err := Join(context.Background(), addr, "123456", nil); err == nil {
		t.Fatal("right code accepted after lockout")
	}
}

func TestExpiry(t *testing.T) {
	_, addr, wait := invite(t, InviteOptions{Code: "123456", Timeout: 200 * time.Millisecond})
	if _, err := wait(); !errors.Is(err, ErrExpired) {
		t.Fatalf("wait: %v", err)
	}
	if _, err := Join(context.Background(), addr, "123456", nil); err == nil {
		t.Fatal("expired code accepted")
	}
}

func TestCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, _, wait, err := Invite(ctx, InviteOptions{ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait: %v", err)
	}
}

// A silent connection does not count as an attempt.
func TestSilentConnection(t *testing.T) {
	_, addr, wait := invite(t, InviteOptions{Code: "123456"})
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	got, err := Join(context.Background(), addr, "123456", []byte("ok"))
	if err != nil || len(got) != 0 {
		t.Fatalf("%q %v", got, err)
	}
	if info, err := wait(); err != nil || string(info) != "ok" {
		t.Fatalf("%q %v", info, err)
	}
}

func TestConcurrentJoins(t *testing.T) {
	_, addr, wait := invite(t, InviteOptions{Code: "424242", Payload: []byte("p")})
	var wg sync.WaitGroup
	var ok, wrong atomic.Int32
	for i := range 12 {
		wg.Go(func() {
			code := "000000"
			if i%4 == 0 {
				code = "424242"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := Join(ctx, addr, code, []byte("m"))
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ErrWrongCode):
				wrong.Add(1)
			}
		})
	}
	wg.Wait()
	_, err := wait()
	if ok.Load() > 1 {
		t.Fatalf("%d joins succeeded", ok.Load())
	}
	if wrong.Load() > MaxFailedAttempts {
		t.Fatalf("%d wrong guesses were evaluated", wrong.Load())
	}
	switch {
	case ok.Load() == 1 && err != nil:
		t.Fatalf("one join succeeded but wait says %v", err)
	case ok.Load() == 0 && !errors.Is(err, ErrTooManyAttempts):
		t.Fatalf("no join succeeded and wait says %v", err)
	}
}

func TestLANDiscovery(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	pc.Close()
	_, _, wait := invite(t, InviteOptions{
		ListenAddr: "0.0.0.0:0", Code: "135790", Payload: []byte("lan"),
		BeaconPort: port, BeaconAddr: "127.0.0.1", BeaconEvery: 50 * time.Millisecond,
	})
	got, err := JoinWith(context.Background(), JoinOptions{Code: "135790", Info: []byte("me"), BeaconPort: port, DiscoverFor: 3 * time.Second})
	if err != nil || string(got) != "lan" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := wait(); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverNothing(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	pc.Close()
	if _, err := Discover(context.Background(), port, 100*time.Millisecond); !errors.Is(err, ErrNoInviteFound) {
		t.Fatal(err)
	}
}

func TestParseBeacon(t *testing.T) {
	from := &net.UDPAddr{IP: net.ParseIP("192.168.1.7"), Port: 999}
	if a, ok := parseBeacon("barnraise-invite [::]:4000", from); !ok || a != "192.168.1.7:4000" {
		t.Fatal(a)
	}
	if a, ok := parseBeacon("barnraise-invite 100.64.0.2:4000", from); !ok || a != "100.64.0.2:4000" {
		t.Fatal(a)
	}
	if _, ok := parseBeacon("hello", from); ok {
		t.Fatal("junk accepted")
	}
}

func TestFrames(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() { _ = writeFrame(a, 7, []byte("abc")) }()
	typ, body, err := readFrame(b)
	if err != nil || typ != 7 || string(body) != "abc" {
		t.Fatal(typ, body, err)
	}
	go func() { _, _ = a.Write([]byte{0xff, 0xff, 0xff, 0xff}) }()
	if _, _, err := readFrame(b); !errors.Is(err, ErrProtocol) {
		t.Fatal(err)
	}
}

func TestAbandonedAfterReplyCounts(t *testing.T) {
	_, addr, wait := invite(t, InviteOptions{Code: "123456"})
	for range MaxFailedAttempts {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		msgA, _, err := cpace.Start("999999", cpace.NewContextInfo(idJoiner, idInviter, []byte(Context)))
		if err != nil {
			t.Fatal(err)
		}
		if err := writeFrame(c, frameHello, append([]byte{version}, msgA...)); err != nil {
			t.Fatal(err)
		}
		if typ, _, err := readFrame(c); err != nil || typ != frameReply {
			t.Fatal(typ, err)
		}
		c.Close()
	}
	if _, err := wait(); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("wait: %v", err)
	}
}
