package acp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// The terminals of an ACP agent are Barnraise's to run, so Barnraise puts
// them in a bubblewrap sandbox when the machine can start one: the whole
// file system read-only, the item's worktree (and the repository's .git,
// where a worktree's commits go) writable, /tmp private, the credential
// folders of deny_read hidden, and the process tree in its own PID
// namespace that dies with the session. The network stays as the session
// has it (the egress proxy of the daemon, when on).

// probeSandbox reports whether bubblewrap can really start here; AppArmor
// or a container may forbid unprivileged namespaces even when it exists.
func probeSandbox() bool {
	if _, err := exec.LookPath("bwrap"); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "bwrap", "--die-with-parent", "--unshare-pid", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "true").Run() == nil
}

// BubblewrapWorks reports whether bubblewrap can start a sandbox on this
// machine, the way ACP terminals are confined (for the capability probe).
func BubblewrapWorks() bool { return probeSandbox() }

// sandboxArgs wraps a command in bubblewrap.
func sandboxArgs(pol domain.Policy, worktree, socket, cwd, name string, args []string) (string, []string) {
	out := []string{"--die-with-parent", "--new-session", "--unshare-pid", "--unshare-ipc",
		"--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp"}
	for _, m := range hiddenPaths(pol) {
		if st, err := os.Stat(m); err == nil {
			if st.IsDir() {
				out = append(out, "--tmpfs", m)
			} else {
				out = append(out, "--ro-bind", "/dev/null", m)
			}
		}
	}
	// The tools Barnraise installed live in its hidden data folder too:
	// they come back read-only, so the PATH finds them (ADR-0046).
	if tools := os.Getenv(domain.EnvToolsDir); filepath.IsAbs(tools) {
		if st, err := os.Stat(tools); err == nil && st.IsDir() {
			out = append(out, "--ro-bind", tools, tools)
		}
	}
	// Writable binds come after the hidden folders: the worktree lives
	// under Barnraise's data folder, which is itself hidden.
	out = append(out, "--bind", worktree, worktree)
	if g := gitCommonDir(worktree); g != "" && !within(g, worktree) {
		out = append(out, "--bind", g, g)
	}
	if socket != "" {
		if _, err := os.Stat(socket); err == nil {
			out = append(out, "--bind", socket, socket)
		}
	}
	out = append(out, "--chdir", cwd, "--", name)
	return "bwrap", append(out, args...)
}

// hiddenPaths are the deny_read rules that name a folder or a file without
// wildcards (~/.ssh/**, ~/.netrc): those can be hidden by a mount. The
// rules with wildcards stay with the policy check of the command line.
func hiddenPaths(pol domain.Policy) []string {
	var out []string
	for _, g := range pol.DenyRead {
		if rest, ok := strings.CutPrefix(g, "~/"); ok {
			if pol.Home == "" {
				continue
			}
			g = filepath.Join(pol.Home, rest)
		}
		if !filepath.IsAbs(g) {
			continue
		}
		g = strings.TrimSuffix(g, "/**")
		if strings.ContainsAny(g, "*?[{") {
			continue
		}
		out = append(out, filepath.Clean(g))
	}
	return out
}

// gitCommonDir is the repository folder of a git worktree (its .git file
// points into <repo>/.git/worktrees/<name>); empty for a plain checkout.
func gitCommonDir(worktree string) string {
	data, err := os.ReadFile(filepath.Join(worktree, ".git"))
	if err != nil {
		return ""
	}
	dir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return ""
	}
	dir = strings.TrimSpace(dir)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(worktree, dir)
	}
	if parent := filepath.Dir(dir); filepath.Base(parent) == "worktrees" {
		return filepath.Dir(parent)
	}
	return dir
}

func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}
