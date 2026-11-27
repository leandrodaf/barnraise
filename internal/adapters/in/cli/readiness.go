package cli

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/keychain"
	"github.com/leandrodaf/barnraise/internal/config"
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
	// stat reads a file's information; nil means os.Stat.
	stat func(string) (os.FileInfo, error)
	// tries and sleep pace the push check on a network error (0/nil: 3, time.Sleep).
	tries int
	sleep func(time.Duration)
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
		env:  os.Getenv,
		stat: os.Stat,
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
		// A dry-run push asks the server for write access without sending
		// anything. A network that drops for a moment is tried again, and a
		// network that stays down is not a missing credential: it does not
		// pause the project (that paused one each time the daemon restarted
		// during a blip).
		var msg string
		var err error
		for try := 0; try < r.pushTries(); try++ {
			if try > 0 {
				r.wait(3 * time.Second)
			}
			pctx, cancel := context.WithTimeout(ctx, 40*time.Second)
			msg, err = r.run(pctx, cfg.Root, "git", "push", "--dry-run", "--porcelain", remote, "HEAD:refs/barnraise/readiness")
			cancel()
			if err == nil || !networkDown(msg) {
				break
			}
		}
		if err != nil && !networkDown(msg) {
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
	stat := r.stat
	if stat == nil {
		stat = os.Stat
	}
	for _, name := range slices.Sorted(maps.Keys(cfg.Plugins)) {
		if msg := config.PluginProgramProblem(cfg.Root, name, cfg.Plugins[name].Command, r.lookPath, stat); msg != "" {
			out = append(out, msg+": the daemon starts plugins with the PATH of the shell that starts it, and every tracker, check or notification call through it would fail")
		}
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

// networkDown reports an error that says the network is not there now, not
// that access is refused.
func networkDown(msg string) bool {
	m := strings.ToLower(msg)
	for _, w := range []string{"network is unreachable", "could not resolve host", "could not resolve hostname", "temporary failure in name resolution",
		"connection timed out", "operation timed out", "no route to host", "connection refused", "connection reset", "network is down"} {
		if strings.Contains(m, w) {
			return true
		}
	}
	return false
}

func (r readiness) pushTries() int {
	if r.tries > 0 {
		return r.tries
	}
	return 3
}

func (r readiness) wait(d time.Duration) {
	if r.sleep != nil {
		r.sleep(d)
		return
	}
	time.Sleep(d)
}
