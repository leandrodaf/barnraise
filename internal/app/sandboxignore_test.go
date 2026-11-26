package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

// Inside Claude Code's sandbox the working folder shows placeholder files
// (.bashrc, .mcp.json, .vscode…); the agent's git ignores them, and a file
// the project tracks, or a new one of its own, still shows.
func TestSandboxedGitIgnoresThePlaceholders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	_ = os.MkdirAll(filepath.Join(home, ".config", "git"), 0o755)
	_ = os.WriteFile(filepath.Join(home, ".config", "git", "ignore"), []byte("*.swp\n"), 0o644)

	repo := t.TempDir()
	testkit.RunGit(t, repo, "init", "-q", "-b", "main")
	testkit.WriteFile(t, repo, ".gitmodules", "[submodule]\n")
	testkit.RunGit(t, repo, "add", ".")
	testkit.RunGit(t, repo, "-c", "user.email=a@b", "-c", "user.name=A", "commit", "-qm", "init")
	for _, f := range []string{".bashrc", ".profile", ".mcp.json", ".ripgreprc", ".vscode/x", ".claude/settings.json", "notes.swp", "src/new.go"} {
		testkit.WriteFile(t, repo, f, "x")
	}
	testkit.WriteFile(t, repo, ".gitmodules", "changed\n")

	ex, err := sandboxExcludes(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "status", "--porcelain", "-uall")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.excludesFile", "GIT_CONFIG_VALUE_0="+ex)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimRight(string(out), "\n")
	if got != " M .gitmodules\n?? src/new.go" {
		t.Fatalf("status:\n%s", got)
	}
}

// The agent's git does not sign (no passphrase can be typed in a session,
// and the sandbox keeps ~/.gnupg read-only), for every provider through
// the environment; with the sandbox it also ignores the placeholders.
func TestSessionGitSettings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	e := newEnv(t)
	for _, sandbox := range []bool{false, true} {
		e.svc.Runtime().Sandbox = sandbox
		sessDir := filepath.Join(t.TempDir(), ".barnraise-session")
		env := map[string]string{}
		_, settingsPath, err := e.svc.Runtime().writeSessionFiles(e.p, sessDir, e.root, env, Actor{Kind: ActorAgent, ItemID: "I-1"})
		if err != nil {
			t.Fatal(err)
		}
		cfg := map[string]string{}
		for i := 0; i < 5; i++ {
			if k := env[fmt.Sprintf("GIT_CONFIG_KEY_%d", i)]; k != "" {
				cfg[k] = env[fmt.Sprintf("GIT_CONFIG_VALUE_%d", i)]
			}
		}
		if cfg["commit.gpgsign"] != "false" || cfg["tag.gpgsign"] != "false" || env["GIT_CONFIG_COUNT"] != fmt.Sprint(len(cfg)) {
			t.Fatalf("sandbox=%v env %v", sandbox, env)
		}
		if _, ok := cfg["core.excludesFile"]; ok != sandbox {
			t.Fatalf("sandbox=%v: excludes %v", sandbox, cfg)
		}
		b, _ := os.ReadFile(settingsPath)
		if !strings.Contains(string(b), `"GIT_CONFIG_KEY_0": "commit.gpgsign"`) || !strings.Contains(string(b), `"DISABLE_AUTOUPDATER": "1"`) {
			t.Fatalf("settings %s", b)
		}
	}
}

// A delivery the item cannot take now says why and what to do.
func TestLateMessage(t *testing.T) {
	st := &domain.State{Questions: map[string]*domain.Question{"Q-9": {ID: "Q-9", ItemID: "I-1"}, "Q-8": {ID: "Q-8", ItemID: "I-1", Answer: "yes"}}}
	waiting := &domain.Item{ID: "I-1", Step: "implement", Status: domain.StatusWaitingOwner}
	if m := lateMessage(st, waiting, "implement", "delivery"); !strings.Contains(m, "waits for the owner's answer (Q-9) at step implement") || !strings.Contains(m, "Stop here without delivering") {
		t.Fatal(m)
	}
	moved := &domain.Item{ID: "I-1", Step: "check", Status: domain.StatusInProgress}
	if m := lateMessage(st, moved, "implement", "review"); !strings.Contains(m, "moved on to step check") {
		t.Fatal(m)
	}
}
