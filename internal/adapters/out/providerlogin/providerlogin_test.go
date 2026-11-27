package providerlogin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// script writes a stand-in for a provider's CLI.
func script(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	path := filepath.Join(t.TempDir(), "cli")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func collect(p ports.LoginProcess, until string) string {
	var b strings.Builder
	for out := range p.Output() {
		b.WriteString(out)
		if until != "" && strings.Contains(b.String(), until) {
			break
		}
	}
	return b.String()
}

func TestClaudeLoginTakesTheCode(t *testing.T) {
	dir := t.TempDir()
	cli := script(t, `if [ "$1 $2" = "auth status" ]; then
  if [ -f "$CLAUDE_CONFIG_DIR/ok" ]; then echo 'note'; echo '{"loggedIn": true}'; else echo '{"loggedIn": false}'; fi
  exit 0
fi
[ "$1 $2" = "auth login" ] || exit 9
echo "browser=$BROWSER display=${DISPLAY:-none}"
echo "visit: https://claude.example/authorize?x=1" >&2
printf 'Paste code here if prompted > '
read code
[ "$code" = "abc#def" ] || { echo "bad code"; exit 3; }
touch "$CLAUDE_CONFIG_DIR/ok"
echo "Login successful"
`)
	r := &Runner{Bin: map[string]string{"claude": cli}, Env: []string{"PATH=" + os.Getenv("PATH"), "DISPLAY=:0", "BROWSER=firefox", "CLAUDE_CONFIG_DIR=/elsewhere"}}
	spec := ports.LoginSpec{Provider: "claude", Dir: dir}
	ctx := context.Background()
	if ok, err := r.LoggedIn(ctx, spec); err != nil || ok {
		t.Fatalf("before: %v, %v", ok, err)
	}
	p, err := r.Start(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	out := collect(p, "Paste code")
	// No browser opens on the machine: the owner may be at another one.
	if !strings.Contains(out, "browser=true display=none") || !strings.Contains(out, "https://claude.example/authorize?x=1") {
		t.Errorf("printed: %q", out)
	}
	if err := p.Input("abc#def"); err != nil {
		t.Fatal(err)
	}
	if rest := collect(p, ""); !strings.Contains(rest, "Login successful") {
		t.Errorf("after the code: %q", rest)
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if ok, err := r.LoggedIn(ctx, spec); err != nil || !ok {
		t.Fatalf("after: %v, %v", ok, err)
	}
}

func TestCodexLoginAndStop(t *testing.T) {
	dir := t.TempDir()
	cli := script(t, `if [ "$1 $2" = "login status" ]; then
  if [ -f "$CODEX_HOME/auth.json" ]; then echo "Logged in using ChatGPT"; exit 0; fi
  echo "Not logged in"; exit 1
fi
[ "$1 $2" = "login --device-auth" ] || exit 9
echo "https://auth.example/device"
echo "ABCD-12345"
sleep 30
`)
	r := &Runner{Bin: map[string]string{"codex": cli}}
	spec := ports.LoginSpec{Provider: "codex", Dir: dir}
	ctx := context.Background()
	if ok, err := r.LoggedIn(ctx, spec); err != nil || ok {
		t.Fatalf("before: %v, %v", ok, err)
	}
	p, err := r.Start(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if out := collect(p, "ABCD-12345"); !strings.Contains(out, "https://auth.example/device") {
		t.Errorf("printed: %q", out)
	}
	start := time.Now()
	p.Stop()
	p.Stop() // twice is once
	go collect(p, "")
	if err := p.Wait(); err == nil {
		t.Error("a stopped login ended well")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("stop took %s", d)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.LoggedIn(ctx, spec); err != nil || !ok {
		t.Fatalf("after: %v, %v", ok, err)
	}
}

func TestLoginsThatCannotRun(t *testing.T) {
	ctx := context.Background()
	r := New()
	if _, err := r.Start(ctx, ports.LoginSpec{Provider: "gemini"}); err == nil || !strings.Contains(err.Error(), "without a terminal") {
		t.Errorf("gemini: %v", err)
	}
	if _, err := r.LoggedIn(ctx, ports.LoginSpec{Provider: "gemini"}); err == nil {
		t.Error("gemini's login was checked")
	}
	missing := &Runner{Bin: map[string]string{"claude": filepath.Join(t.TempDir(), "nope")}}
	if _, err := missing.Start(ctx, ports.LoginSpec{Provider: "claude"}); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("no CLI: %v", err)
	}
	if _, err := missing.LoggedIn(ctx, ports.LoginSpec{Provider: "claude"}); err == nil {
		t.Error("a CLI that is not there answered")
	}
	// Words that are not the CLI's status mean no login.
	junk := &Runner{Bin: map[string]string{"claude": script(t, "echo hello\n")}}
	if ok, err := junk.LoggedIn(ctx, ports.LoginSpec{Provider: "claude"}); err != nil || ok {
		t.Errorf("junk: %v, %v", ok, err)
	}
	// The CLI's own login keeps the CLI's own folder.
	env := strings.Join(r.env(ports.LoginSpec{Provider: "claude"}), "\n")
	if strings.Contains(env, "CLAUDE_CONFIG_DIR=") {
		t.Error("an account without a folder got one")
	}
}

func TestInstallThenLogin(t *testing.T) {
	if installThenLogin("codex", false) != "" || installThenLogin("claude", true) != "" {
		t.Fatal("only claude has an installer, and a test's program is never installed")
	}
	s := installThenLogin("claude", false)
	if s == "" {
		t.Skip("no curl or bash here")
	}
	if out, err := exec.Command("sh", "-n", "-c", s).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
}
