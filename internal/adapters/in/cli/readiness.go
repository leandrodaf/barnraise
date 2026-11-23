package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/keychain"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/paths"
)

// readiness is what `barnraise up` checks on this machine before it lets
// agents work here: a project can be valid and still not runnable on a
// machine that cannot push to the arbiter, has no token for the code host,
// or runs its checks with a PATH that lacks their tools.
type readiness struct {
	// run executes a command in dir and returns its combined output.
	run func(ctx context.Context, dir, name string, args ...string) (string, error)
	// lookPath finds a program on the PATH the daemon will have.
	lookPath func(string) (string, error)
	// secret reads a keychain entry.
	secret func(key string) string
	// env reads a variable.
	env func(string) string
}

func defaultReadiness() readiness {
	return readiness{
		run: func(ctx context.Context, dir, name string, args ...string) (string, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=10")
			out, err := cmd.CombinedOutput()
			return string(out), err
		},
		lookPath: exec.LookPath,
		secret: func(key string) string {
			v, _ := keychain.New("barnraise", filepath.Join(paths.ConfigDir(), "secrets")).Get(key)
			return v
		},
		env: os.Getenv,
	}
}

// problems returns one plain sentence per thing that would stop agents on
// this machine. An empty list means the machine is ready.
func (r readiness) problems(ctx context.Context, cfg *domain.ProjectConfig) []string {
	var out []string
	remote := cfg.CodeHost.Remote
	if remote == "" {
		remote = "origin"
	}
	if _, err := r.run(ctx, cfg.Root, "git", "remote", "get-url", remote); err != nil {
		out = append(out, fmt.Sprintf("the git remote %q does not exist in %s: it is the arbiter of the cluster and where every step is pushed; add it (git remote add %s <url>) or set code_host.remote", remote, cfg.Root, remote))
	} else {
		pctx, cancel := context.WithTimeout(ctx, 40*time.Second)
		// A dry-run push asks the server for write access without sending anything.
		msg, err := r.run(pctx, cfg.Root, "git", "push", "--dry-run", "--porcelain", remote, "HEAD:refs/barnraise/readiness")
		cancel()
		if err != nil {
			out = append(out, fmt.Sprintf("this machine cannot push to the git remote %q (%s): items cannot be claimed or saved here; give this machine its own credential for that repository", remote, firstLine(msg)))
		}
	}
	if typ := cfg.CodeHost.Type; typ == "github" || cfg.Tracker.Type == "github" {
		if !r.hasGitHubToken(ctx, cfg) {
			out = append(out, "no GitHub token on this machine: issues, pull requests and statuses need one; run barnraise tracker connect github, set GITHUB_TOKEN, or gh auth login")
		}
	}
	missing := map[string][]string{}
	for name, ch := range cfg.Checks {
		if ch.Run == "" || (ch.RunsOn != "" && ch.RunsOn != "local" && !strings.HasPrefix(ch.RunsOn, "machine:")) {
			continue
		}
		prog := commandProgram(ch.Run)
		if prog == "" || strings.Contains(prog, "/") {
			continue
		}
		if _, err := r.lookPath(prog); err != nil {
			missing[prog] = append(missing[prog], name)
		}
	}
	progs := make([]string, 0, len(missing))
	for p := range missing {
		progs = append(progs, p)
	}
	sort.Strings(progs)
	for _, p := range progs {
		sort.Strings(missing[p])
		out = append(out, fmt.Sprintf("%s is not on the PATH (check %s): the daemon runs checks with the PATH of the shell that starts it", p, strings.Join(missing[p], ", ")))
	}
	return out
}

func (r readiness) hasGitHubToken(ctx context.Context, cfg *domain.ProjectConfig) bool {
	for _, ref := range []string{cfg.CodeHost.Token, cfg.Tracker.Token} {
		if after, ok := strings.CutPrefix(ref, "keychain:"); ok && r.secret(after) != "" {
			return true
		}
		if after, ok := strings.CutPrefix(ref, "env:"); ok && r.env(after) != "" {
			return true
		}
	}
	if r.env("GITHUB_TOKEN") != "" {
		return true
	}
	out, err := r.run(ctx, cfg.Root, "gh", "auth", "token")
	return err == nil && strings.TrimSpace(out) != ""
}

// commandProgram is the program a shell command line starts: the first word
// after any leading VAR=value assignments.
func commandProgram(line string) string {
	for _, w := range strings.Fields(line) {
		if i := strings.IndexByte(w, '='); i > 0 && !strings.ContainsAny(w[:i], "\"'$/") {
			continue
		}
		return strings.Trim(w, "\"'")
	}
	return ""
}

func firstLine(s string) string {
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return "no answer"
}
