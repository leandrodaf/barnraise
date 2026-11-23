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

// run executes git in dir and returns stdout.
func run(ctx context.Context, dir string, stdin io.Reader, args ...string) (string, error) {
	return runEnv(ctx, dir, stdin, nil, args...)
}

// runEnv is run with extra environment variables.
func runEnv(ctx context.Context, dir string, stdin io.Reader, extra []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
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
