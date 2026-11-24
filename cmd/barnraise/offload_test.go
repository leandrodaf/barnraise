package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestTwoMachinesOffloadChecks: a loaded machine works on an item and an
// idle machine of the cluster runs its check. The idle machine checks the
// item's commit out by itself, runs the command and leaves nothing behind;
// the result comes back and the item is integrated as usual.
func TestTwoMachinesOffloadChecks(t *testing.T) {
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
	marker := filepath.Join(base, "check-ran-on")
	git(t, base, "init", "-q", "--bare", "-b", "main", remote)
	git(t, base, "clone", "-q", remote, seed)
	git(t, seed, "checkout", "-q", "-B", "main")
	os.WriteFile(filepath.Join(seed, "README.md"), []byte("# demo\n"), 0o644)
	// The check records which machine ran it (the daemon's own name is in its environment).
	yml := "project: demo\ntemplate: blank\nlanguage: en\napproves: analyst\nchecks:\n  main: \"echo $BARNRAISE_FAKE_MACHINE >> " + marker + "; test -f README.md\"\nlease: { renew: 1s, ttl: 6s }\n"
	os.WriteFile(filepath.Join(seed, "barnraise.yaml"), []byte(yml), 0o644)
	git(t, seed, "add", "-A")
	git(t, seed, "commit", "-qm", "init")
	git(t, seed, "push", "-q", "origin", "main")

	mk := func(name string, extra ...string) *machineEnv {
		dir := filepath.Join(base, name)
		root := filepath.Join(dir, "repo")
		os.MkdirAll(filepath.Join(dir, "cfg"), 0o700)
		git(t, base, "clone", "-q", remote, root)
		cfg := fmt.Sprintf("machine: %s\nlisten: off\nnats_host: 127.0.0.1\nnats_port: %d\nprojects: [%s]\n", name, freePort(t), root)
		os.WriteFile(filepath.Join(dir, "cfg", "daemon.yaml"), []byte(cfg), 0o600)
		env := append(os.Environ(), "BARNRAISE_CONFIG_DIR="+filepath.Join(dir, "cfg"), "BARNRAISE_DATA_DIR="+filepath.Join(dir, "data"),
			"XDG_RUNTIME_DIR="+dir, "BARNRAISE_FAKE_PROVIDER=1", "BARNRAISE_FAKE_MACHINE="+name, "BARNRAISE_NO_TAILSCALE=1", "BARNRAISE_NO_AUTOSTART=1",
			"BARNRAISE_PROJECT=", "BARNRAISE_TOKEN=", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		return &machineEnv{name: name, root: root, env: append(env, extra...)}
	}
	// alpha reports itself loaded; beta is idle and starts no agent sessions:
	// the item can only be worked on by alpha, and its check should go to beta.
	alpha := mk("alpha", "BARNRAISE_TEST_LOAD_PCT=96")
	beta := mk("beta", "BARNRAISE_TEST_LOAD_PCT=4")

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
	beta.run(t, bin, "machine", "join", code, "--addr", fmt.Sprintf("127.0.0.1:%d", pairPort))
	if err := inv.Wait(); err != nil {
		t.Fatalf("invite: %v", err)
	}

	stop := func(m *machineEnv) {
		if m.daemon == nil || m.daemon.ProcessState != nil {
			return
		}
		_ = m.daemon.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = m.daemon.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = m.daemon.Process.Kill()
		}
	}
	startWith := func(m *machineEnv, args ...string) {
		t.Helper()
		logPath := filepath.Join(base, m.name+".log")
		logf, _ := os.Create(logPath)
		m.daemon = exec.Command(bin, append([]string{"daemon"}, args...)...)
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
	startWith(beta, "--no-agents")
	defer stop(beta)
	startWith(alpha)
	defer stop(alpha)

	// Each machine must know the other's readings before a check can move.
	deadline := time.Now().Add(30 * time.Second)
	for {
		out := alpha.run(t, bin, "cluster", "status")
		if strings.Contains(out, "beta") && strings.Contains(out, "load ") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("alpha never saw beta's readings:\n%s", out)
		}
		time.Sleep(300 * time.Millisecond)
	}

	// Each one also learns what the other keeps on its own disk.
	alpha.waitOverviewOfPeer(t, bin, "beta")
	beta.waitOverviewOfPeer(t, bin, "alpha")

	alpha.run(t, bin, "item", "add", "Report", "--area", "reports", "-c", "WHEN the user opens reports, THE SYSTEM SHALL list them | main",
		"--body", "fake: write app/report.txt report")
	deadline = time.Now().Add(90 * time.Second)
	for {
		it := alpha.itemJSON(t, bin, "I-1")
		if it != nil && it["status"] == "integrated" {
			break
		}
		if time.Now().After(deadline) {
			a, _ := os.ReadFile(filepath.Join(base, "alpha.log"))
			b, _ := os.ReadFile(filepath.Join(base, "beta.log"))
			t.Fatalf("not integrated: %v\n--- alpha\n%s\n--- beta\n%s", it, a, b)
		}
		time.Sleep(300 * time.Millisecond)
	}

	// The machine that only ran the check answers the whole story of the item:
	// who worked on it, what it cost there and where its check ran.
	deadline = time.Now().Add(30 * time.Second)
	for {
		o := beta.overview(t, bin, "--item", "I-1")
		if o != nil && len(o.Projects) == 1 && len(o.Projects[0].Items) == 1 {
			it := o.Projects[0].Items[0]
			onBeta := false
			for _, c := range it.Checks {
				onBeta = onBeta || (c.Check == "main" && c.Passed && strings.HasSuffix(c.RunsOn, "beta"))
			}
			if o.Machine == "beta" && it.Activity == "integrated" && it.UsageByMachine["alpha"].Sessions > 0 && it.Usage.Tokens != "" && it.Usage.Tokens != "0" && onBeta {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("beta's overview of I-1: %+v", o)
		}
		time.Sleep(300 * time.Millisecond)
	}

	ran, _ := os.ReadFile(marker)
	if !strings.Contains(string(ran), "beta") {
		a, _ := os.ReadFile(filepath.Join(base, "alpha.log"))
		t.Fatalf("the check must have run on the idle machine; it ran on %q\n--- alpha\n%s", ran, a)
	}
	tl := alpha.run(t, bin, "item", "timeline", "I-1")
	if !strings.Contains(tl, "ran on beta") || !strings.Contains(tl, "alpha has 96% of its CPUs busy") {
		t.Fatalf("the timeline must say where the check ran and why:\n%s", tl)
	}
	// The idle machine leaves nothing behind.
	left, _ := filepath.Glob(filepath.Join(base, "beta", "data", "worktrees", "demo", "_remote-*"))
	if len(left) != 0 {
		t.Fatalf("beta kept the checkout of the check: %v", left)
	}
	log := git(t, base, "--git-dir", remote, "log", "--format=%s", "main")
	if !strings.Contains(log, "feat(reports): report") {
		t.Fatalf("main: %s", log)
	}
}
