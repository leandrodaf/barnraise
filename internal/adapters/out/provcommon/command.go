package provcommon

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// CommandFunc builds the command for a CLI call. Tests swap it to run a fake
// binary. Env set by the func is kept; the adapter appends its own.
type CommandFunc func(ctx context.Context, name string, args ...string) *exec.Cmd

// DefaultCommand builds a plain exec.Cmd; the session handles ctx itself so
// it can stop the process group gracefully.
func DefaultCommand(_ context.Context, name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

// Output runs a short command (like --version) and returns stdout and stderr
// together. The whole process group is killed on timeout.
func Output(ctx context.Context, fn CommandFunc, timeout time.Duration, name string, args ...string) (string, error) {
	if fn == nil {
		fn = DefaultCommand
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := fn(ctx, name, args...)
	setGroup(cmd)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return buf.String(), fmt.Errorf("%s %v: %w", name, args, err)
		}
		return buf.String(), nil
	case <-ctx.Done():
		_ = signalGroup(cmd, killSignal)
		<-done
		return buf.String(), fmt.Errorf("%s %v: %w", name, args, ctx.Err())
	}
}
