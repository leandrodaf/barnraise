// Package provcommon holds what the provider adapters share: running a CLI
// in its own process group, turning its stdout lines into normalized events,
// building a minimal environment and reading usage-limit messages.
package provcommon

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// ErrStopped is returned by Wait when Stop ended the session.
var ErrStopped = errors.New("session stopped")

// ErrTimeout is returned (wrapped) by Wait when the session outlived its timeout.
var ErrTimeout = errors.New("session timed out")

// DefaultGrace is how long Stop waits after SIGINT before SIGKILL.
const DefaultGrace = 5 * time.Second

// stderrTail is how much of stderr is kept for error messages.
const stderrTail = 16 << 10

// Parser turns the stdout lines of one session into normalized events.
// A parser is used by one goroutine only.
type Parser interface {
	// Parse handles one non-empty stdout line (without the newline).
	Parse(line []byte) []ports.ProviderEvent
	// Finish is called once stdout ended; it may emit closing events
	// (for example a synthetic done when the CLI never sent one).
	Finish(exitCode int, stderr string) []ports.ProviderEvent
	// Result is what the parser gathered; ExitCode and Duration are set by the session.
	Result() ports.SessionResult
}

// StartOptions tune a session.
type StartOptions struct {
	Name    string        // used in error messages, e.g. "claude"
	Timeout time.Duration // 0: none
	Grace   time.Duration // SIGINT to SIGKILL delay; 0: DefaultGrace
	Stdin   io.Reader     // nil: empty stdin
	OnExit  func()        // runs once after the process ended (cleanup of temp files)
}

// Session runs one CLI process and implements ports.Session.
type Session struct {
	name   string
	cmd    *exec.Cmd
	parser Parser
	grace  time.Duration
	start  time.Time

	events chan ports.ProviderEvent
	queue  *EventQueue
	stderr *tailBuffer

	stopOnce sync.Once
	mu       sync.Mutex
	reason   error // why the session was ended from our side
	onExit   func()

	done   chan struct{}
	result ports.SessionResult
	err    error
}

// Start launches cmd (its Stdout, Stderr and Stdin must be unset) and parses
// its stdout with p. The process gets its own process group. Cancelling ctx
// or reaching the timeout stops it gracefully.
func Start(ctx context.Context, cmd *exec.Cmd, p Parser, o StartOptions) (*Session, error) {
	if o.Grace <= 0 {
		o.Grace = DefaultGrace
	}
	if o.Name == "" {
		o.Name = cmd.Path
	}
	setGroup(cmd)
	if o.Stdin != nil {
		cmd.Stdin = o.Stdin
	} else {
		cmd.Stdin = strings.NewReader("")
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = pw
	tb := &tailBuffer{max: stderrTail}
	cmd.Stderr = tb
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = 2 * time.Second
	}

	s := &Session{
		name:   o.Name,
		cmd:    cmd,
		parser: p,
		grace:  o.Grace,
		events: make(chan ports.ProviderEvent),
		queue:  NewEventQueue(),
		stderr: tb,
		done:   make(chan struct{}),
		onExit: o.OnExit,
	}
	s.start = time.Now()
	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		if o.OnExit != nil {
			o.OnExit()
		}
		return nil, fmt.Errorf("%s: start: %w", o.Name, err)
	}
	_ = pw.Close()

	go s.queue.Forward(s.events)
	readDone := make(chan struct{})
	go s.read(pr, readDone)
	go s.run(ctx, pr, readDone, o.Timeout)
	return s, nil
}

// Events delivers normalized events; it closes after the process ended.
// Events are buffered without bound, so not reading them never blocks the process.
func (s *Session) Events() <-chan ports.ProviderEvent { return s.events }

// Wait blocks until the process ended and returns what it reported.
func (s *Session) Wait() (ports.SessionResult, error) {
	<-s.done
	return s.result, s.err
}

// Stop ends the session: SIGINT to the process group, SIGKILL after the grace period.
func (s *Session) Stop() error {
	s.end(ErrStopped)
	return nil
}

// Pid of the CLI process (for diagnostics).
func (s *Session) Pid() int {
	if s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

// Stderr returns the tail of what the CLI wrote to stderr so far.
func (s *Session) Stderr() string { return s.stderr.String() }

func (s *Session) end(reason error) {
	s.mu.Lock()
	if s.reason == nil {
		s.reason = reason
	}
	s.mu.Unlock()
	s.stopOnce.Do(func() {
		go func() {
			_ = signalGroup(s.cmd, interruptSignal)
			t := time.NewTimer(s.grace)
			defer t.Stop()
			select {
			case <-s.done:
			case <-t.C:
				_ = signalGroup(s.cmd, killSignal)
			}
		}()
	})
}

func (s *Session) read(r io.Reader, readDone chan<- struct{}) {
	defer close(readDone)
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			for _, ev := range s.safeParse(line) {
				s.queue.Push(ev)
			}
		}
		if err != nil {
			return
		}
	}
}

// safeParse never lets a parser bug crash the daemon.
func (s *Session) safeParse(line []byte) (evs []ports.ProviderEvent) {
	defer func() {
		if r := recover(); r != nil {
			evs = []ports.ProviderEvent{{Kind: ports.PEError, Text: fmt.Sprintf("%s: parser panic: %v", s.name, r)}}
		}
	}()
	return s.parser.Parse(line)
}

func (s *Session) run(ctx context.Context, pr *os.File, readDone <-chan struct{}, timeout time.Duration) {
	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}
	waitErr := make(chan error, 1)
	go func() { waitErr <- s.cmd.Wait() }()

	var werr error
	select {
	case werr = <-waitErr:
	case <-ctx.Done():
		s.end(ctx.Err())
		werr = <-waitErr
	case <-timer:
		s.end(fmt.Errorf("%w after %s", ErrTimeout, timeout))
		werr = <-waitErr
	}
	// Children left behind may keep stdout open; give the reader a moment,
	// then kill the rest of the group and close our end.
	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
		_ = signalGroup(s.cmd, killSignal)
		_ = pr.Close()
		<-readDone
	}
	_ = pr.Close()
	if s.onExit != nil {
		s.onExit()
	}

	code := exitCode(s.cmd, werr)
	stderr := s.stderr.String()
	for _, ev := range s.parser.Finish(code, stderr) {
		s.queue.Push(ev)
	}
	res := s.parser.Result()
	res.ExitCode = code
	res.Duration = time.Since(s.start)
	if res.Usage.Duration == 0 {
		res.Usage.Duration = res.Duration
	}

	s.mu.Lock()
	reason := s.reason
	s.mu.Unlock()
	var err error
	switch {
	case reason != nil:
		err = fmt.Errorf("%s: %w", s.name, reason)
	case code != 0 && !res.Limited:
		err = fmt.Errorf("%s: exit code %d: %s", s.name, code, lastLines(stderr, 5))
	}
	s.result, s.err = res, err
	s.queue.Close()
	close(s.done)
}

// exitCode reports the exit code; -1 when killed by a signal or not started.
func exitCode(cmd *exec.Cmd, err error) int {
	if cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}

func lastLines(s string, n int) string {
	s = strings.TrimSpace(s)
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// EventQueue is an unbounded FIFO of events, so producers never block on a
// consumer that does not read.
type EventQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  []ports.ProviderEvent
	closed bool
}

// NewEventQueue returns an empty queue.
func NewEventQueue() *EventQueue {
	q := &EventQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// Push appends an event; pushes after Close are dropped.
func (q *EventQueue) Push(ev ports.ProviderEvent) {
	q.mu.Lock()
	if !q.closed {
		q.items = append(q.items, ev)
	}
	q.mu.Unlock()
	q.cond.Signal()
}

// Close ends the queue; Pop drains what is left, then reports false.
func (q *EventQueue) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

// Pop blocks for the next event.
func (q *EventQueue) Pop() (ports.ProviderEvent, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.items) == 0 {
		return ports.ProviderEvent{}, false
	}
	ev := q.items[0]
	q.items[0] = ports.ProviderEvent{}
	q.items = q.items[1:]
	return ev, true
}

// Forward sends every event of q to ch and closes ch when q is closed and drained.
func (q *EventQueue) Forward(ch chan<- ports.ProviderEvent) {
	defer close(ch)
	for {
		ev, ok := q.Pop()
		if !ok {
			return
		}
		ch <- ev
	}
}

// tailBuffer keeps the last max bytes written.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

var _ ports.Session = (*Session)(nil)
