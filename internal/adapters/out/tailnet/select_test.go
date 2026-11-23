package tailnet

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func found(string) (string, error)   { return "/usr/bin/tailscale", nil }
func missing(string) (string, error) { return "", errors.New("not found") }

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{"": ModeAuto, "auto": ModeAuto, "Tailscale": ModeTailscale, " tsnet ": ModeTsnet, "lan": ModeLAN} {
		if got, err := ParseMode(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	if _, err := ParseMode("wifi"); err == nil {
		t.Fatal("unknown network accepted")
	}
}

type resolveCase struct {
	name string
	cfg  Config
	want Mode
	err  error
}

func TestResolve(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "tailscaled.state"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := func() string { return "tskey-auth-x" }
	cases := []resolveCase{
		{"disabled wins", Config{Mode: ModeTsnet, Disabled: true}, ModeLAN, nil},
		{"explicit tailscale", Config{Mode: ModeTailscale, LookPath: missing}, ModeTailscale, nil},
		{"explicit lan", Config{Mode: ModeLAN, LookPath: found}, ModeLAN, nil},
		{"auto prefers the installed command", Config{LookPath: found, AuthKey: key}, ModeTailscale, nil},
		{"auto without anything is the local network", Config{LookPath: missing, StateDir: t.TempDir()}, ModeLAN, nil},
		{"unknown mode", Config{Mode: "wifi"}, "", errors.New("")},
	}
	if Embeddable {
		cases = append(cases,
			resolveCase{"auto with an auth key embeds", Config{LookPath: missing, AuthKey: key}, ModeTsnet, nil},
			resolveCase{"auto with an earlier login embeds", Config{LookPath: missing, StateDir: stateDir}, ModeTsnet, nil},
			resolveCase{"explicit tsnet", Config{Mode: ModeTsnet, LookPath: found}, ModeTsnet, nil},
		)
	} else {
		cases = append(cases,
			resolveCase{"auto with an auth key but no tsnet", Config{LookPath: missing, AuthKey: key, StateDir: stateDir}, ModeLAN, nil},
			resolveCase{"explicit tsnet not built in", Config{Mode: ModeTsnet}, "", ErrNotBuiltIn},
		)
	}
	for _, c := range cases {
		got, err := c.cfg.Resolve()
		if c.err != nil {
			if err == nil || (c.err == ErrNotBuiltIn && !errors.Is(err, ErrNotBuiltIn)) {
				t.Errorf("%s: want error %v, got %q %v", c.name, c.err, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: got %q %v, want %q", c.name, got, err, c.want)
		}
	}
}

func TestOpenCommandAndLAN(t *testing.T) {
	tn, m, err := Open(context.Background(), Config{Mode: ModeLAN})
	if tn != nil || m != ModeLAN || err != nil {
		t.Fatal(tn, m, err)
	}
	tn, m, err = Open(context.Background(), Config{Mode: ModeTailscale, Bin: "tailscale-definitely-missing"})
	if err != nil || m != ModeTailscale || tn.Mode() != ModeTailscale || tn.Available() || tn.Close() != nil {
		t.Fatal(tn, m, err)
	}
	if _, _, err := Open(context.Background(), Config{Mode: "wifi"}); err == nil {
		t.Fatal("unknown mode opened")
	}
	if !Embeddable {
		if _, _, err := Open(context.Background(), Config{Mode: ModeTsnet}); !errors.Is(err, ErrNotBuiltIn) {
			t.Fatal(err)
		}
	}
}

func TestLookupAuthKey(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	keychain := func(k string) (string, error) {
		if k == AuthKeySecret {
			return " tskey-keychain \n", nil
		}
		return "", errors.New("missing")
	}
	if got := LookupAuthKey(getenv, nil); got != "" {
		t.Fatal(got)
	}
	if got := LookupAuthKey(getenv, keychain); got != "tskey-keychain" {
		t.Fatal(got)
	}
	env["TS_AUTHKEY"] = "tskey-ts"
	if got := LookupAuthKey(getenv, keychain); got != "tskey-ts" {
		t.Fatal(got)
	}
	env["BARNRAISE_TS_AUTHKEY"] = "tskey-barnraise"
	if got := LookupAuthKey(getenv, keychain); got != "tskey-barnraise" {
		t.Fatal(got)
	}
	if (Config{AuthKey: func() string { return " k " }}).authKey() != "k" || (Config{}).authKey() != "" {
		t.Fatal("authKey trim")
	}
	if got := (Config{}).tags(); len(got) != 1 || got[0] != Tag {
		t.Fatal(got)
	}
}

func TestRedactor(t *testing.T) {
	var got string
	logf := redactor("tskey-secret", func(f string, a ...any) { got = strings.TrimSpace(strings.ReplaceAll(f, "%s", a[0].(string))) })
	logf("logging in with %s at %s", "tskey-secret", "https://login.example")
	if strings.Contains(got, "tskey-secret") || !strings.Contains(got, "[auth key]") || !strings.Contains(got, "https://login.example") {
		t.Fatal(got)
	}
	redactor("x", nil)("nothing %d", 1) // no logger: discarded
}

func TestSelfFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadSelf(dir); err == nil {
		t.Fatal("read missing self")
	}
	want := Self{Name: "alpha", IPs: []string{"100.64.0.1"}, Online: true}
	if err := WriteSelf(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSelf(dir)
	if err != nil || got.Name != "alpha" || IPv4(got.IPs) != "100.64.0.1" {
		t.Fatal(got, err)
	}
	st, _ := os.Stat(filepath.Join(dir, selfFile))
	if st.Mode().Perm() != 0o600 {
		t.Fatal(st.Mode())
	}
	if HasState(dir) {
		t.Fatal("no state yet")
	}
}

func TestLockDir(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// flock is per open file description: a second open in the same process conflicts too.
	if _, err := lockDir(dir); !errors.Is(err, ErrBusy) {
		t.Fatalf("second lock: %v", err)
	}
	unlock()
	again, err := lockDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	again()
	if _, err := lockDir(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("lock in a missing directory")
	}
}

func TestForward(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); c.Close() }()
		}
	}()
	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Forward(ctx, front, target.Addr().String()) }()

	c, err := net.Dial("tcp", front.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("%q %v", buf, err)
	}
	c.Close()

	// nothing on the target: the relayed connection is closed
	target.Close()
	c2, err := net.Dial("tcp", front.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c2.Read(buf); err == nil {
		t.Fatal("relay to a closed target stayed open")
	}
	c2.Close()

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
