// Package git implements ports.Git and ports.Arbiter by shelling out to the
// git binary. The arbiter keeps leases, counters and main integration in git
// refs, so the git server the project already uses (or the local disk when
// there is no remote) decides the few things only one machine may do.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Identity used when the repository has no user.name/user.email configured.
const (
	fallbackName  = "Barnraise"
	fallbackEmail = "barnraise@localhost"
)

// CmdError is a failed git invocation.
type CmdError struct {
	Args     []string
	Stdout   string
	Stderr   string
	ExitCode int // -1 when the process did not run or was killed
	Err      error
}

func (e *CmdError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(e.Stdout)
	}
	if msg == "" && e.Err != nil {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("git %s: %s (exit %d)", strings.Join(e.Args, " "), msg, e.ExitCode)
}

func (e *CmdError) Unwrap() error { return e.Err }

// exitCode returns the process exit code of err, or -1.
func exitCode(err error) int {
	if ce, ok := errors.AsType[*CmdError](err); ok {
		return ce.ExitCode
	}
	return -1
}

var gitEnv = []string{
	"GIT_TERMINAL_PROMPT=0",
	"LC_ALL=C",
	"LANG=C",
	"GIT_EDITOR=true",
	"GIT_SEQUENCE_EDITOR=true",
	"GIT_MERGE_AUTOEDIT=no",
}

// networkTimeout bounds a git command that talks to the remote. Without it,
// a connection that dies without a reset (a network change, a sleeping
// router) leaves ssh waiting forever, and whatever called git (the merge
// queue holds a lock while it fetches and pushes) stops for good.
var networkTimeout = 10 * time.Minute

// waitDelay is how long git's children (ssh, credential helpers) may keep
// its pipes open after git itself was killed.
const waitDelay = 10 * time.Second

// networkCmds are the git subcommands that reach the remote.
var networkCmds = map[string]bool{"fetch": true, "push": true, "ls-remote": true, "clone": true, "pull": true}

// subcommand returns git's subcommand in args, skipping global options.
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-c" || a == "-C" || a == "--git-dir" || a == "--work-tree":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return ""
}

// run executes git in dir and returns stdout.
func run(ctx context.Context, dir string, stdin io.Reader, args ...string) (string, error) {
	return runEnv(ctx, dir, stdin, nil, args...)
}

// runEnv is run with extra environment variables.
func runEnv(ctx context.Context, dir string, stdin io.Reader, extra []string, args ...string) (string, error) {
	if networkCmds[subcommand(args)] {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, networkTimeout)
		defer cancel()
		// Over https, give up on a transfer that stalls for a minute.
		extra = append([]string{"GIT_HTTP_LOW_SPEED_LIMIT=1000", "GIT_HTTP_LOW_SPEED_TIME=60"}, extra...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.WaitDelay = waitDelay
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), gitEnv...), extra...)
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		code := -1
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			code = ee.ExitCode()
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return out.String(), &CmdError{Args: args, Stdout: out.String(), Stderr: errb.String(), ExitCode: code, Err: err}
	}
	return out.String(), nil
}
