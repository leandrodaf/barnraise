package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{{"0.2.0", "0.1.9", true}, {"0.1.10", "0.1.9", true}, {"0.1.0", "0.1.0-dev", true}, {"0.1.0-dev", "0.1.0", false},
		{"v1.0.0", "0.9.9", true}, {"0.1.0", "0.1.0", false}, {"0.1.0", "0.2.0", false}}
	for _, c := range cases {
		if got := newerVersion(c.a, c.b); got != c.want {
			t.Errorf("newerVersion(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

// TestClusterSingleVersion: a machine on an older version fetches the newer
// binary from its peer, verifies it and restarts on the new version.
func TestClusterSingleVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("cluster")
	}
	base := t.TempDir()
	build := func(name, v string) string {
		out := filepath.Join(base, name, "barnraise")
		os.MkdirAll(filepath.Dir(out), 0o755)
		if b, err := exec.Command("go", "build", "-ldflags", "-X main.version="+v, "-o", out, ".").CombinedOutput(); err != nil {
			t.Fatalf("build: %v\n%s", err, b)
		}
		return out
	}
	oldBin, newBin := build("alpha", "0.1.0"), build("beta", "0.2.0")
	remote := filepath.Join(base, "remote.git")
	seed := filepath.Join(base, "seed")
	git(t, base, "init", "-q", "--bare", "-b", "main", remote)
	git(t, base, "clone", "-q", remote, seed)
	git(t, seed, "checkout", "-q", "-B", "main")
	os.WriteFile(filepath.Join(seed, "README.md"), []byte("# demo\n"), 0o644)
	os.WriteFile(filepath.Join(seed, "barnraise.yaml"), []byte(demoYAML), 0o644)
	git(t, seed, "add", "-A")
	git(t, seed, "commit", "-qm", "init")
	git(t, seed, "push", "-q", "origin", "main")
	mk := func(name string) *machineEnv {
		dir := filepath.Join(base, name)
		root := filepath.Join(dir, "repo")
		os.MkdirAll(filepath.Join(dir, "cfg"), 0o700)
		git(t, base, "clone", "-q", remote, root)
		cfg := fmt.Sprintf("machine: %s\nlisten: off\nnats_host: 127.0.0.1\nnats_port: %d\nprojects: [%s]\n", name, freePort(t), root)
		os.WriteFile(filepath.Join(dir, "cfg", "daemon.yaml"), []byte(cfg), 0o600)
		env := append(os.Environ(), "BARNRAISE_CONFIG_DIR="+filepath.Join(dir, "cfg"), "BARNRAISE_DATA_DIR="+filepath.Join(dir, "data"),
			"XDG_RUNTIME_DIR="+dir, "BARNRAISE_FAKE_PROVIDER=1", "BARNRAISE_NO_TAILSCALE=1", "BARNRAISE_PROJECT=", "BARNRAISE_TOKEN=")
		return &machineEnv{name: name, root: root, env: env}
	}
	alpha, beta := mk("alpha"), mk("beta")
	// Pair by writing each other's keys through invite/join.
	pairPort := freePort(t)
	inv := exec.Command(oldBin, "machine", "invite", "--listen", fmt.Sprintf("127.0.0.1:%d", pairPort))
	inv.Dir, inv.Env = alpha.root, alpha.env
	stdout, _ := inv.StdoutPipe()
	if err := inv.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	code := ""
	deadline := time.Now().Add(10 * time.Second)
	acc := ""
	for code == "" && time.Now().Before(deadline) {
		n, _ := stdout.Read(buf)
		acc += string(buf[:n])
		if i := strings.Index(acc, "code: "); i >= 0 && len(acc) >= i+13 {
			code = strings.ReplaceAll(acc[i+6:i+13], " ", "")
		}
	}
	if code == "" {
		t.Fatalf("no code: %q", acc)
	}
	beta.run(t, newBin, "machine", "join", code, "--addr", fmt.Sprintf("127.0.0.1:%d", pairPort))
	_ = inv.Wait()

	alpha.start(t, oldBin, filepath.Join(base, "alpha.log"))
	defer func() { _ = alpha.daemon.Process.Kill() }()
	beta.start(t, newBin, filepath.Join(base, "beta.log"))
	defer func() { _ = beta.daemon.Process.Kill() }()

	deadline = time.Now().Add(90 * time.Second)
	for {
		out, _ := exec.Command(oldBin, "version").Output()
		if strings.TrimSpace(string(out)) == "0.2.0" {
			break
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(filepath.Join(base, "alpha.log"))
			t.Fatalf("alpha did not update its binary (still %s):\n%s", out, b)
		}
		time.Sleep(500 * time.Millisecond)
	}
	// Alpha restarted on the new binary and answers again.
	deadline = time.Now().Add(30 * time.Second)
	for {
		cmd := exec.Command(oldBin, "status")
		cmd.Dir, cmd.Env = alpha.root, alpha.env
		if cmd.Run() == nil {
			break
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(filepath.Join(base, "alpha.log"))
			t.Fatalf("alpha did not come back:\n%s", b)
		}
		time.Sleep(300 * time.Millisecond)
	}
}
