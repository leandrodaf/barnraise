package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type machineEnv struct {
	name, root string
	env        []string
	daemon     *exec.Cmd
}

func (m *machineEnv) run(t *testing.T, bin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir, cmd.Env = m.root, m.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", m.name, args, err, out)
	}
	return string(out)
}

func (m *machineEnv) start(t *testing.T, bin, logPath string) {
	t.Helper()
	logf, _ := os.Create(logPath)
	m.daemon = exec.Command(bin, "daemon")
	m.daemon.Dir, m.daemon.Env = m.root, m.env
	m.daemon.Stdout, m.daemon.Stderr = logf, logf
	if err := m.daemon.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		cmd := exec.Command(bin, "status")
		cmd.Dir, cmd.Env = m.root, m.env
		if cmd.Run() == nil {
			return
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(logPath)
			t.Fatalf("%s daemon did not start:\n%s", m.name, b)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func (m *machineEnv) itemJSON(t *testing.T, bin, id string) map[string]any {
	cmd := exec.Command(bin, "item", "show", id, "--json")
	cmd.Dir, cmd.Env = m.root, m.env
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var v struct {
		Item map[string]any `json:"item"`
	}
	_ = json.Unmarshal(out, &v)
	return v.Item
}

// TestTwoMachinesFailover: two machines pair with a 6-digit code, one takes
// an item and dies mid-step; the other takes the item over through the git
// arbiter after the lease ttl and integrates it. Nothing is lost.
func TestTwoMachinesFailover(t *testing.T) {
	if testing.Short() {
		t.Skip("cluster")
	}
	base := t.TempDir()
	bin := filepath.Join(base, "barnraise")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
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
			"XDG_RUNTIME_DIR="+dir, "BARNRAISE_FAKE_PROVIDER=1", "BARNRAISE_FAKE_MACHINE="+name, "BARNRAISE_NO_TAILSCALE=1", "BARNRAISE_NO_AUTOSTART=1",
			"BARNRAISE_PROJECT=", "BARNRAISE_TOKEN=", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		return &machineEnv{name: name, root: root, env: env}
	}
	alpha, beta := mk("alpha"), mk("beta")

	// Pair with the 6-digit code.
	pairPort := freePort(t)
	inv := exec.Command(bin, "machine", "invite", "--listen", fmt.Sprintf("127.0.0.1:%d", pairPort))
	inv.Dir, inv.Env = alpha.root, alpha.env
	stdout, _ := inv.StdoutPipe()
	inv.Stderr = os.Stderr
	if err := inv.Start(); err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(stdout)
	code := ""
	re := regexp.MustCompile(`code: (\d{3}) (\d{3})`)
	for sc.Scan() {
		if m := re.FindStringSubmatch(sc.Text()); m != nil {
			code = m[1] + m[2]
			break
		}
	}
	if code == "" {
		t.Fatal("no pairing code printed")
	}
	go func() {
		for sc.Scan() {
		}
	}()
	out := beta.run(t, bin, "machine", "join", code, "--addr", fmt.Sprintf("127.0.0.1:%d", pairPort))
	if !strings.Contains(out, "Joined alpha") {
		t.Fatalf("join: %s", out)
	}
	if err := inv.Wait(); err != nil {
		t.Fatalf("invite: %v", err)
	}

	alpha.start(t, bin, filepath.Join(base, "alpha.log"))
	defer func() {
		if alpha.daemon.ProcessState == nil {
			_ = alpha.daemon.Process.Kill()
		}
	}()
	alpha.run(t, bin, "item", "add", "Cards screen", "--area", "cards", "-c", "WHEN the user opens cards, THE SYSTEM SHALL list the cards | main",
		"--body", "fake: hang-on alpha\nfake: write app/cards.txt cards")
	deadline := time.Now().Add(60 * time.Second)
	for {
		it := alpha.itemJSON(t, bin, "I-1")
		if it != nil && it["step"] == "implement" && it["status"] == "in_progress" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("alpha never reached implement: %v", it)
		}
		time.Sleep(200 * time.Millisecond)
	}

	beta.start(t, bin, filepath.Join(base, "beta.log"))
	defer func() {
		_ = beta.daemon.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = beta.daemon.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = beta.daemon.Process.Kill()
		}
	}()
	// Beta sees alpha's item through the mirrored events.
	deadline = time.Now().Add(30 * time.Second)
	for {
		it := beta.itemJSON(t, bin, "I-1")
		if it != nil && it["step"] == "implement" {
			claim, _ := it["claim"].(map[string]any)
			if claim["machine"] != "alpha" {
				t.Fatalf("beta sees the item held by %v", claim)
			}
			break
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(filepath.Join(base, "beta.log"))
			a, _ := os.ReadFile(filepath.Join(base, "alpha.log"))
			cfgA, _ := os.ReadFile(filepath.Join(base, "alpha", "cfg", "daemon.yaml"))
			cfgB, _ := os.ReadFile(filepath.Join(base, "beta", "cfg", "daemon.yaml"))
			t.Fatalf("beta never saw alpha's item:\n%s\n--- alpha\n%s\n--- cfg alpha\n%s\n--- cfg beta\n%s", b, a, cfgA, cfgB)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Alpha dies in the middle of the step.
	_ = alpha.daemon.Process.Kill()
	_ = alpha.daemon.Wait()

	deadline = time.Now().Add(90 * time.Second)
	for {
		it := beta.itemJSON(t, bin, "I-1")
		if it != nil && it["status"] == "integrated" {
			break
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(filepath.Join(base, "beta.log"))
			t.Fatalf("beta did not take over and integrate: %v\n%s", it, b)
		}
		time.Sleep(300 * time.Millisecond)
	}
	log := git(t, base, "--git-dir", remote, "log", "--format=%s", "main")
	if !strings.Contains(log, "feat(cards): cards screen") {
		t.Fatalf("main: %s", log)
	}
	tl := beta.run(t, bin, "item", "timeline", "I-1")
	if !strings.Contains(tl, "taken over from machine alpha") {
		t.Fatalf("timeline must show the takeover:\n%s", tl)
	}
	_ = context.Background
}
