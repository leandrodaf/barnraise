package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// standby runs `barnraise standby ...` on m with the given answer on stdin.
func (m *machineEnv) standby(t *testing.T, bin, answer string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"standby"}, args...)...)
	cmd.Dir, cmd.Env = m.root, m.env
	cmd.Stdin = strings.NewReader(answer)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s standby %v: %v\n%s", m.name, args, err, out)
	}
	return string(out)
}

// inStandby reads the switch from the machine's own configuration file.
func (m *machineEnv) inStandby(t *testing.T) bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(filepath.Dir(m.root), "cfg", "daemon.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(b), "standby: true")
}

func (m *machineEnv) waitStandby(t *testing.T, bin string, want bool) {
	t.Helper()
	word := "Standby is off"
	if want {
		word = "Standby is on"
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		out := m.standby(t, bin, "")
		if strings.Contains(out, word) && m.inStandby(t) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: want %q and the same in daemon.yaml, got %q (file says %v)", m.name, word, out, m.inStandby(t))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestStandby: the machine's switch, end to end. Set with the daemon
// stopped, a daemon starts and starts nothing; off, the same item is worked
// to the end. One machine switches the other, with the owner's yes. The
// switch outlives a restart.
func TestStandby(t *testing.T) {
	if testing.Short() {
		t.Skip("cluster")
	}
	bin, base, alpha, beta := pairedMachines(t)
	alphaLog, betaLog := filepath.Join(base, "alpha.log"), filepath.Join(base, "beta.log")

	// With the daemon stopped: the switch is the file the daemon reads.
	for _, m := range []*machineEnv{alpha, beta} {
		out := m.standby(t, bin, "", "on", "not", "yet")
		if !strings.Contains(out, "Standby is on") || !strings.Contains(out, "daemon is not running") || !m.inStandby(t) {
			t.Fatalf("%s standby on without a daemon: %s", m.name, out)
		}
		if out := m.standby(t, bin, ""); !strings.Contains(out, "Standby is on") || !strings.Contains(out, "not yet") {
			t.Fatalf("%s standby (state) without a daemon: %s", m.name, out)
		}
	}

	// A daemon that starts in standby starts nothing, and says so.
	alpha.start(t, bin, alphaLog)
	beta.start(t, bin, betaLog)
	alpha.waitConnected(t, "beta")
	beta.waitConnected(t, "alpha")
	if out := alpha.run(t, bin, "status"); !strings.Contains(out, "STANDBY") || !strings.Contains(out, "not yet") {
		t.Fatalf("status does not say standby: %s", out)
	}
	alpha.run(t, bin, "item", "add", "Cards screen", "--area", "cards", "-c", "WHEN the user opens cards, THE SYSTEM SHALL list the cards | main",
		"--body", "fake: write app/cards.txt cards")
	time.Sleep(6 * time.Second) // several passes of both loops
	for _, m := range []*machineEnv{alpha, beta} {
		it := m.itemJSON(t, bin, "I-1")
		if it == nil {
			if m == alpha {
				t.Fatal("the item is missing on alpha")
			}
			continue // not mirrored yet
		}
		if it["status"] != "ready" || it["claim"] != nil {
			t.Fatalf("%s worked on the item in standby: status %v, step %v, claim %v", m.name, it["status"], it["step"], it["claim"])
		}
	}

	// "n": only the machine that asked leaves standby, and works.
	out := alpha.standby(t, bin, "n\n", "off")
	if !strings.Contains(out, "also connected to: beta") || !strings.Contains(out, "out of standby") || strings.Contains(out, "beta is") {
		t.Fatalf("standby off answered n: %s", out)
	}
	alpha.waitStandby(t, bin, false)
	if !beta.inStandby(t) {
		t.Fatal("beta left standby without being asked")
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		it := alpha.itemJSON(t, bin, "I-1")
		if it != nil && it["status"] == "integrated" {
			if claim, _ := it["claim"].(map[string]any); claim != nil {
				t.Fatalf("integrated item still claimed: %v", claim)
			}
			break
		}
		if time.Now().After(deadline) {
			a, _ := os.ReadFile(alphaLog)
			t.Fatalf("alpha never integrated the item after standby off: %v\n%s", it, a)
		}
		time.Sleep(300 * time.Millisecond)
	}
	if out := alpha.run(t, bin, "status"); strings.Contains(out, "STANDBY") {
		t.Fatalf("status still says standby: %s", out)
	}

	// "y": the other machine is switched too, and keeps it in its own file.
	out = alpha.standby(t, bin, "y\n", "off")
	if !strings.Contains(out, "✔ beta is out of standby") {
		t.Fatalf("standby off answered y: %s", out)
	}
	beta.waitStandby(t, bin, false)
	out = beta.standby(t, bin, "", "on", "--all", "maintenance")
	if strings.Contains(out, "connected to") || !strings.Contains(out, "✔ alpha is in standby") || !strings.Contains(out, "This machine is in standby") {
		t.Fatalf("standby on --all: %s", out)
	}
	alpha.waitStandby(t, bin, true)
	beta.waitStandby(t, bin, true)
	if out := alpha.run(t, bin, "status"); !strings.Contains(out, "STANDBY") || !strings.Contains(out, "maintenance") {
		t.Fatalf("alpha's status after beta switched it: %s", out)
	}

	// --here asks nothing and reaches nobody.
	if out := beta.standby(t, bin, "y\n", "off", "--here"); strings.Contains(out, "connected to") || strings.Contains(out, "alpha") {
		t.Fatalf("standby off --here: %s", out)
	}
	if !alpha.inStandby(t) {
		t.Fatal("--here reached the other machine")
	}

	// The switch outlives the daemon: stopped in standby, it starts in standby.
	alpha.down(t, bin, "", "--here")
	alpha.exited(t, alphaLog)
	alpha.start(t, bin, alphaLog)
	alpha.waitStandby(t, bin, true)
	if out := alpha.run(t, bin, "status"); !strings.Contains(out, "STANDBY") {
		t.Fatalf("after a restart the status does not say standby: %s", out)
	}
	// And a switch written while the daemon runs (by hand, by another tool) is picked up.
	cfg := filepath.Join(filepath.Dir(alpha.root), "cfg", "daemon.yaml")
	b, _ := os.ReadFile(cfg)
	cleared := strings.ReplaceAll(strings.ReplaceAll(string(b), "standby: true\n", ""), "standby_why: maintenance\n", "")
	if err := os.WriteFile(cfg, []byte(cleared), 0o600); err != nil {
		t.Fatal(err)
	}
	alpha.waitStandby(t, bin, false)

	if out, err := func() ([]byte, error) {
		cmd := exec.Command(bin, "standby", "maybe")
		cmd.Dir, cmd.Env = alpha.root, alpha.env
		return cmd.CombinedOutput()
	}(); err == nil || !strings.Contains(string(out), "on or off") {
		t.Fatalf("a wrong word must be refused: %v %s", err, out)
	}
}
