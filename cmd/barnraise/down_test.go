package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/client"
	"github.com/leandrodaf/barnraise/internal/paths"
)

// connected reports whether m's daemon holds a live connection to peer.
func (m *machineEnv) connected(t *testing.T, peer string) bool {
	t.Helper()
	dir := filepath.Dir(m.root)
	t.Setenv("BARNRAISE_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("BARNRAISE_SOCKET", "")
	tok, _ := os.ReadFile(filepath.Join(dir, "cfg", "owner.token"))
	cl := client.New(client.Options{Socket: paths.Socket(), Token: strings.TrimSpace(string(tok)), Timeout: 5 * time.Second})
	res, err := cl.ListPeers(context.Background(), connect.NewRequest(&pb.Empty{}))
	if err != nil {
		return false
	}
	for _, p := range res.Msg.Peers {
		if p.Name == peer && p.Connected {
			return true
		}
	}
	return false
}

func (m *machineEnv) waitConnected(t *testing.T, peer string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !m.connected(t, peer) {
		if time.Now().After(deadline) {
			t.Fatalf("%s never connected to %s", m.name, peer)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// down runs `barnraise down` on m with the given answer on stdin.
func (m *machineEnv) down(t *testing.T, bin, answer string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"down"}, args...)...)
	cmd.Dir, cmd.Env = m.root, m.env
	cmd.Stdin = strings.NewReader(answer)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s down: %v\n%s", m.name, err, out)
	}
	return string(out)
}

// exited waits for m's daemon to end and fails when it does not, or ends badly.
func (m *machineEnv) exited(t *testing.T, logPath string) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- m.daemon.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			b, _ := os.ReadFile(logPath)
			t.Fatalf("%s daemon ended badly: %v\n%s", m.name, err, b)
		}
	case <-time.After(30 * time.Second):
		_ = m.daemon.Process.Kill()
		b, _ := os.ReadFile(logPath)
		t.Fatalf("%s daemon did not stop:\n%s", m.name, b)
	}
}

// pairedMachines builds the binary and two machines, alpha and beta, on the
// same repository, paired with the 6-digit code. Neither daemon is started.
func pairedMachines(t *testing.T) (bin, base string, alpha, beta *machineEnv) {
	t.Helper()
	base = t.TempDir()
	bin = filepath.Join(base, "barnraise")
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
			"XDG_RUNTIME_DIR="+dir, "BARNRAISE_SOCKET=", "BARNRAISE_FAKE_PROVIDER=1", "BARNRAISE_FAKE_MACHINE="+name, "BARNRAISE_NO_TAILSCALE=1", "BARNRAISE_NO_AUTOSTART=1",
			"BARNRAISE_PROJECT=", "BARNRAISE_TOKEN=", "BARNRAISE_URL=", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		return &machineEnv{name: name, root: root, env: env}
	}
	alpha, beta = mk("alpha"), mk("beta")

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
	if out := beta.run(t, bin, "machine", "join", code, "--addr", fmt.Sprintf("127.0.0.1:%d", pairPort)); !strings.Contains(out, "Joined alpha") {
		t.Fatalf("join: %s", out)
	}
	if err := inv.Wait(); err != nil {
		t.Fatalf("invite: %v", err)
	}

	t.Cleanup(func() {
		for _, m := range []*machineEnv{alpha, beta} {
			if m.daemon != nil && m.daemon.ProcessState == nil {
				_ = m.daemon.Process.Kill()
			}
		}
	})
	return bin, base, alpha, beta
}

// TestTwoMachinesDown: `barnraise down` on one machine asks whether the
// connected ones stop too. "n" stops only the machine that asked; "y" stops
// both, each with a clean exit, so a service manager does not start it again.
func TestTwoMachinesDown(t *testing.T) {
	if testing.Short() {
		t.Skip("cluster")
	}
	bin, base, alpha, beta := pairedMachines(t)
	alphaLog, betaLog := filepath.Join(base, "alpha.log"), filepath.Join(base, "beta.log")
	alpha.start(t, bin, alphaLog)
	beta.start(t, bin, betaLog)
	alpha.waitConnected(t, "beta")
	beta.waitConnected(t, "alpha")

	// "n": only the machine that asked stops.
	out := beta.down(t, bin, "n\n")
	if !strings.Contains(out, "also connected to: alpha") || strings.Contains(out, "alpha is stopping") {
		t.Fatalf("down answered n: %s", out)
	}
	beta.exited(t, betaLog)
	if got := alpha.run(t, bin, "status"); !strings.Contains(got, "machine alpha") {
		t.Fatalf("alpha should still run: %s", got)
	}

	// No answer at all (no terminal): the same as "n".
	beta.start(t, bin, betaLog)
	beta.waitConnected(t, "alpha")
	if out := beta.down(t, bin, ""); strings.Contains(out, "is stopping") {
		t.Fatalf("down without an answer: %s", out)
	}
	beta.exited(t, betaLog)
	alpha.run(t, bin, "status")

	// --here: no question.
	beta.start(t, bin, betaLog)
	beta.waitConnected(t, "alpha")
	if out := beta.down(t, bin, "y\n", "--here"); strings.Contains(out, "connected to") {
		t.Fatalf("down --here asked: %s", out)
	}
	beta.exited(t, betaLog)
	alpha.run(t, bin, "status")

	// "y": both stop, each with a clean exit.
	beta.start(t, bin, betaLog)
	alpha.waitConnected(t, "beta")
	out = alpha.down(t, bin, "y\n")
	if !strings.Contains(out, "also connected to: beta") || !strings.Contains(out, "✔ beta is stopping") {
		t.Fatalf("down answered y: %s", out)
	}
	alpha.exited(t, alphaLog)
	beta.exited(t, betaLog)
}
