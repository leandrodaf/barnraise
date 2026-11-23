// Package execx runs processes for Barnraise: timeouts kill the whole process
// group, output is captured with a size cap that keeps the tail, and a
// non-zero exit code is a result, not an error.
package execx

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// DefaultMaxOutput is the default cap per stream (stdout and stderr each).
const DefaultMaxOutput = 10 << 20

// ErrTimeout is returned (wrapped in *TimeoutError) when a process outlives
// its Cmd.Timeout. The partial result is returned alongside it.
var ErrTimeout = errors.New("process timed out")

// TimeoutError carries the timeout that expired.
type TimeoutError struct {
	Name    string
	Timeout time.Duration
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("%s: %v after %s", e.Name, ErrTimeout, e.Timeout)
}

// Is makes errors.Is(err, ErrTimeout) work.
func (e *TimeoutError) Is(target error) bool { return target == ErrTimeout }

// Option configures the runner.
type Option func(*Runner)

// WithMaxOutput caps each captured stream at n bytes, keeping the tail.
func WithMaxOutput(n int) Option {
	return func(r *Runner) {
		if n > 0 {
			r.max = n
		}
	}
}

// WithKillGrace sets how long to wait for pipes to close after the process
// group was killed (default 2s).
func WithKillGrace(d time.Duration) Option {
	return func(r *Runner) {
		if d > 0 {
			r.grace = d
		}
	}
}

// Runner implements ports.Exec.
type Runner struct {
	max   int
	grace time.Duration
}

var _ ports.Exec = (*Runner)(nil)

// New returns a process runner.
func New(opts ...Option) *Runner {
	r := &Runner{max: DefaultMaxOutput, grace: 2 * time.Second}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Run starts c and waits for it. Errors are only start failures, timeouts
// (ErrTimeout) and context cancellation; exit codes go in the result.
func (r *Runner) Run(ctx context.Context, c ports.Cmd) (ports.ExecResult, error) {
	if c.Name == "" {
		return ports.ExecResult{ExitCode: -1}, errors.New("execx: empty command name")
	}
	runCtx := ctx
	cancel := func() {}
	if c.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, c.Timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	if len(c.Env) > 0 {
		cmd.Env = c.Env
	}
	cmd.Stdin = c.Stdin
	stdout := NewTailBuffer(r.max)
	stderr := NewTailBuffer(r.max)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	KillGroupOnCancel(cmd, r.grace)

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return ports.ExecResult{ExitCode: -1}, fmt.Errorf("start %s: %w", c.Name, err)
	}
	waitErr := cmd.Wait()
	res := ports.ExecResult{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		ExitCode: exitCode(cmd, waitErr),
		Duration: time.Since(start),
	}
	if runCtx.Err() != nil {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		return res, &TimeoutError{Name: c.Name, Timeout: c.Timeout}
	}
	var ee *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &ee) {
		return res, fmt.Errorf("wait %s: %w", c.Name, waitErr)
	}
	return res, nil
}

func exitCode(cmd *exec.Cmd, err error) int {
	if cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}

// TailBuffer is an io.Writer that keeps at most Max bytes, dropping the
// oldest ones. It is safe for concurrent writers.
type TailBuffer struct {
	mu        sync.Mutex
	max       int
	buf       []byte
	truncated bool
}

// NewTailBuffer returns a buffer that keeps the last max bytes.
func NewTailBuffer(max int) *TailBuffer {
	if max <= 0 {
		max = DefaultMaxOutput
	}
	return &TailBuffer{max: max}
}

// Write appends p, discarding the oldest bytes beyond the cap.
func (t *TailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	if len(p) >= t.max {
		t.buf = append(t.buf[:0], p[len(p)-t.max:]...)
		t.truncated = true
		return n, nil
	}
	if over := len(t.buf) + len(p) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
		t.truncated = true
	}
	t.buf = append(t.buf, p...)
	return n, nil
}

// Bytes returns a copy of the kept bytes.
func (t *TailBuffer) Bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.buf...)
}

// Truncated reports whether older bytes were dropped.
func (t *TailBuffer) Truncated() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.truncated
}
