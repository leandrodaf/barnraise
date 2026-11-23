// Package plugin runs external plugins: programs the owner configured in
// barnraise.yaml that talk to the daemon over JSON-RPC 2.0, one message per
// line on their standard input and output (docs/plugins.md). A Host starts
// the program, does the initialize handshake, restarts it with backoff when
// it crashes, stops it when a call outlives its timeout, and exposes it as
// the ports the daemon already uses: ports.Tracker, ports.Notifier, a check
// runner and an event hook.
package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// ProtocolVersion is the version of docs/plugins.md this host speaks.
const ProtocolVersion = 1

// MaxMessage is the longest line a plugin may write (16 MiB).
const MaxMessage = 16 << 20

// JSON-RPC error codes, the standard ones and Barnraise's.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
	CodeCancelled      = -32800
	CodeNotFound       = -32001
	CodeUnavailable    = -32002
)

// passEnv are the variables every plugin gets from the daemon, besides the
// ones its definition lists.
var passEnv = []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "TZ", "TMPDIR", "SYSTEMROOT"}

// Options configures a Host.
type Options struct {
	Name    string           // the plugin's name in barnraise.yaml
	Def     domain.PluginDef // what to run
	Project string
	Root    string // the project's repository: the working directory, and the base of a relative command
	Version string // Barnraise's version, sent in initialize
	// LogDir receives the logs that check plugins return.
	LogDir string
	// Getenv reads the daemon's environment (default os.Getenv).
	Getenv func(string) string
	// Secret resolves a secret reference (keychain:…, env:…).
	Secret func(ref string) (string, error)
	Log    *slog.Logger
	// StartTimeout bounds the start and handshake (default 10s).
	StartTimeout time.Duration
	// Grace is how long a plugin may take to answer a cancellation or a
	// shutdown before it is killed (default 2s).
	Grace time.Duration
	// Backoff is the first restart delay after a crash, doubled per crash
	// in a row up to MaxBackoff (defaults 500ms and 30s).
	Backoff, MaxBackoff time.Duration
	// Healthy is how long a run must last to reset the backoff (default 1m).
	Healthy time.Duration
}

// Info is what a plugin answers to initialize.
type Info struct {
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	ProtocolVersion int      `json:"protocol_version"`
	Kinds           []string `json:"kinds"`
}

// Implements reports whether the plugin declared kind.
func (i Info) Implements(kind string) bool { return slices.Contains(i.Kinds, kind) }

// Error is a JSON-RPC error object.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// message is any JSON-RPC message on the wire.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type reply struct {
	result json.RawMessage
	err    *Error
}

// Host is one configured plugin. It is safe for concurrent use; calls are
// multiplexed on the one process by id.
type Host struct {
	o Options

	mu        sync.Mutex
	proc      *proc
	crashes   int       // crashes in a row
	notBefore time.Time // no restart before this
	closed    bool

	// hmu guards the hook queue apart from mu, which a start holds for
	// seconds: queueing an event never waits for a plugin to start.
	hmu      sync.Mutex
	hclosed  bool
	hookOnce sync.Once
	hooks    chan domain.Event
	stop     chan struct{}
	hookDone chan struct{}
}

// New returns a Host; the program starts with the first call.
func New(o Options) *Host {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	o.Log = o.Log.With("plugin", o.Name)
	if o.StartTimeout <= 0 {
		o.StartTimeout = 10 * time.Second
	}
	if o.Grace <= 0 {
		o.Grace = 2 * time.Second
	}
	if o.Backoff <= 0 {
		o.Backoff = 500 * time.Millisecond
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = 30 * time.Second
	}
	if o.Healthy <= 0 {
		o.Healthy = time.Minute
	}
	if o.Def.Timeout <= 0 {
		o.Def.Timeout = 30 * time.Second
	}
	return &Host{o: o, stop: make(chan struct{})}
}

// Name is the plugin's name in barnraise.yaml.
func (h *Host) Name() string { return h.o.Name }

// Def is the definition the host runs.
func (h *Host) Def() domain.PluginDef { return h.o.Def }

// Info starts the plugin if needed and returns its handshake answer.
func (h *Host) Info(ctx context.Context) (Info, error) {
	p, err := h.ensure(ctx)
	if err != nil {
		return Info{}, err
	}
	return p.info, nil
}

// Running reports whether the program is up (for tests and status).
func (h *Host) Running() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.proc != nil && !h.proc.dead()
}

// Call sends a request and decodes its result into out (nil: ignored).
// timeout <= 0 uses the plugin's own timeout.
func (h *Host) Call(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	p, err := h.ensure(ctx)
	if err != nil {
		return err
	}
	if timeout <= 0 {
		timeout = h.o.Def.Timeout
	}
	raw, err := p.call(ctx, method, params, timeout, h.o.Grace)
	if err != nil {
		if errors.Is(err, errTimeout) {
			h.o.Log.Warn("plugin did not answer in time; stopping it", "method", method, "timeout", timeout)
			p.kill()
			return fmt.Errorf("plugin %s did not answer %s within %s; it was stopped and starts again on the next call", h.o.Name, method, timeout)
		}
		var re *Error
		if errors.As(err, &re) {
			return fmt.Errorf("plugin %s: %s: %w", h.o.Name, method, re)
		}
		return err
	}
	if out == nil || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("plugin %s answered %s with a result Barnraise cannot read: %w", h.o.Name, method, err)
	}
	return nil
}

// require starts the plugin and checks it implements kind.
func (h *Host) require(ctx context.Context, kind string) error {
	info, err := h.Info(ctx)
	if err != nil {
		return err
	}
	if !info.Implements(kind) {
		return fmt.Errorf("plugin %s does not implement %s (it declares: %s)", h.o.Name, kind, strings.Join(info.Kinds, ", "))
	}
	return nil
}

// Close stops the hook worker and the program: shutdown, then the end of
// its input, then a kill after the grace period.
func (h *Host) Close() error {
	h.hmu.Lock()
	h.hclosed = true
	done := h.hookDone
	h.hmu.Unlock()
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	p := h.proc
	h.proc = nil
	close(h.stop)
	h.mu.Unlock()
	if done != nil {
		<-done
	}
	if p != nil {
		p.shutdown(h.o.Grace)
	}
	return nil
}

// ensure returns a running, initialized process, starting one if needed.
func (h *Host) ensure(ctx context.Context) (*proc, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, fmt.Errorf("plugin %s is stopped", h.o.Name)
	}
	if h.proc != nil && !h.proc.dead() {
		return h.proc, nil
	}
	if h.proc != nil {
		select { // a stopped process ends at once; wait for it to be gone
		case <-h.proc.done:
		case <-time.After(h.o.Grace):
		}
		h.noteExit(h.proc)
		h.proc = nil
	}
	if wait := time.Until(h.notBefore); wait > 0 {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, fmt.Errorf("plugin %s is waiting %s before starting again after a crash: %w", h.o.Name, wait.Round(time.Millisecond), ctx.Err())
		case <-t.C:
		}
	}
	p, err := h.start(ctx)
	if err != nil {
		h.crashes++
		h.notBefore = time.Now().Add(h.backoff())
		return nil, err
	}
	h.proc = p
	return p, nil
}

// noteExit records how a process ended, for the backoff.
func (h *Host) noteExit(p *proc) {
	if time.Since(p.started) >= h.o.Healthy {
		h.crashes = 0
	}
	h.crashes++
	h.notBefore = p.ended().Add(h.backoff())
	h.o.Log.Warn("plugin exited; it starts again on the next call", "err", p.exitError(), "restarts_in", time.Until(h.notBefore).Round(time.Millisecond))
}

func (h *Host) backoff() time.Duration {
	d := h.o.Backoff
	for i := 1; i < h.crashes && d < h.o.MaxBackoff; i++ {
		d *= 2
	}
	return min(d, h.o.MaxBackoff)
}

// env builds the program's environment: a few basics, the names its
// definition lists, and its resolved secrets. Nothing else of the daemon's
// environment reaches it.
func (h *Host) env() ([]string, error) {
	var env []string
	for _, k := range passEnv {
		if v := h.o.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	for _, k := range h.o.Def.Env {
		if v := h.o.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(h.o.Def.Secrets)) {
		if h.o.Secret == nil {
			return nil, fmt.Errorf("plugin %s needs secret %s, but no secret store is available", h.o.Name, k)
		}
		v, err := h.o.Secret(h.o.Def.Secrets[k])
		if err != nil {
			return nil, fmt.Errorf("plugin %s: secret %s (%s) cannot be read: %w", h.o.Name, k, h.o.Def.Secrets[k], err)
		}
		env = append(env, k+"="+v)
	}
	env = append(env, "BARNRAISE_PLUGIN="+h.o.Name, "BARNRAISE_PROJECT="+h.o.Project)
	return env, nil
}

func (h *Host) command() string {
	c := h.o.Def.Command
	switch {
	case strings.HasPrefix(c, "~/"):
		if home := h.o.Getenv("HOME"); home != "" {
			return filepath.Join(home, c[2:])
		}
	case !filepath.IsAbs(c) && strings.Contains(c, "/") && h.o.Root != "":
		return filepath.Join(h.o.Root, filepath.FromSlash(c))
	}
	return c
}

// start runs the program and does the handshake.
func (h *Host) start(ctx context.Context) (*proc, error) {
	env, err := h.env()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(h.command(), h.o.Def.Args...)
	cmd.Dir = h.o.Root
	cmd.Env = env
	setGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("plugin %s cannot start %s: %w", h.o.Name, h.o.Def.Command, err)
	}
	p := &proc{name: h.o.Name, cmd: cmd, stdin: stdin, log: h.o.Log, pending: map[int64]chan reply{}, done: make(chan struct{}), started: time.Now()}
	var readers sync.WaitGroup
	readers.Add(2)
	go func() { defer readers.Done(); p.readStdout(stdout) }()
	go func() { defer readers.Done(); p.readStderr(stderr) }()
	go func() {
		readers.Wait()
		err := cmd.Wait()
		p.finish(err)
	}()

	params := map[string]any{
		"protocol_version": ProtocolVersion, "barnraise_version": h.o.Version,
		"plugin": h.o.Name, "project": h.o.Project, "root": h.o.Root, "options": h.o.Def.Options,
	}
	raw, err := p.call(ctx, "initialize", params, h.o.StartTimeout, h.o.Grace)
	if err == nil {
		err = json.Unmarshal(raw, &p.info)
		if err != nil {
			err = fmt.Errorf("its answer to initialize is not an object with name, version, protocol_version and kinds: %w", err)
		}
	}
	if err == nil {
		switch {
		case p.info.ProtocolVersion != ProtocolVersion:
			err = fmt.Errorf("it speaks protocol version %d, and this Barnraise speaks %d", p.info.ProtocolVersion, ProtocolVersion)
		case p.info.Name == "":
			err = errors.New("its answer to initialize has no name")
		case len(p.info.Kinds) == 0:
			err = errors.New("its answer to initialize declares no kinds (tracker, check, notifier, hook, network)")
		}
	}
	if err != nil {
		p.kill()
		<-p.done
		if errors.Is(err, errTimeout) {
			err = fmt.Errorf("it did not answer initialize within %s", h.o.StartTimeout)
		}
		if pe := p.exitError(); pe != nil && !errors.Is(pe, errKilled) {
			err = fmt.Errorf("%w (%v)", err, pe)
		}
		return nil, fmt.Errorf("plugin %s failed its handshake: %w", h.o.Name, err)
	}
	h.o.Log.Info("plugin started", "name", p.info.Name, "version", p.info.Version, "kinds", p.info.Kinds, "pid", cmd.Process.Pid)
	return p, nil
}

var (
	errTimeout = errors.New("timeout")
	errKilled  = errors.New("stopped by Barnraise")
)

// proc is one run of the program.
type proc struct {
	name    string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	log     *slog.Logger
	info    Info
	started time.Time

	wmu sync.Mutex // one line at a time on stdin

	mu       sync.Mutex
	nextID   int64
	pending  map[int64]chan reply
	exitErr  error
	endedAt  time.Time
	killed   bool
	protoErr error

	done chan struct{}
}

func (p *proc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// dead reports a process that ended or that Barnraise is stopping.
func (p *proc) dead() bool {
	if p.exited() {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.killed || p.protoErr != nil
}

func (p *proc) ended() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.endedAt.IsZero() {
		return time.Now()
	}
	return p.endedAt
}

// exitError says why the process ended.
func (p *proc) exitError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.protoErr != nil:
		return p.protoErr
	case p.killed:
		return errKilled
	case p.exitErr != nil:
		return p.exitErr
	}
	return errors.New("it exited")
}

func (p *proc) finish(err error) {
	p.mu.Lock()
	p.exitErr = err
	p.endedAt = time.Now()
	pending := p.pending
	p.pending = map[int64]chan reply{}
	p.mu.Unlock()
	close(p.done)
	for _, ch := range pending {
		close(ch)
	}
}

func (p *proc) kill() {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	_ = killGroup(p.cmd)
}

// shutdown asks the program to stop, closes its input and kills it after grace.
func (p *proc) shutdown(grace time.Duration) {
	if !p.exited() {
		ctx, cancel := context.WithTimeout(context.Background(), grace)
		_, _ = p.call(ctx, "shutdown", nil, grace, 0)
		cancel()
		_ = p.stdin.Close()
	}
	select {
	case <-p.done:
	case <-time.After(grace):
		p.kill()
		<-p.done
	}
}

func (p *proc) write(m message) error {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_, err = p.stdin.Write(b)
	return err
}

func (p *proc) notify(method string, params any) {
	raw, _ := json.Marshal(params)
	_ = p.write(message{Method: method, Params: raw})
}

// call sends one request and waits for its answer, the context, the
// process's end or the timeout. On timeout the plugin is told to cancel,
// and gets grace to answer before errTimeout is returned.
func (p *proc) call(ctx context.Context, method string, params any, timeout, grace time.Duration) (json.RawMessage, error) {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	ch := make(chan reply, 1)
	p.mu.Lock()
	if p.pending == nil || p.exited() {
		p.mu.Unlock()
		return nil, p.gone()
	}
	p.nextID++
	id := p.nextID
	p.pending[id] = ch
	p.mu.Unlock()
	forget := func() {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
	}
	idRaw, _ := json.Marshal(id)
	if err := p.write(message{ID: idRaw, Method: method, Params: raw}); err != nil {
		forget()
		select {
		case <-p.done:
			return nil, p.gone()
		case <-time.After(100 * time.Millisecond):
		}
		return nil, fmt.Errorf("plugin %s: cannot send %s: %w", p.name, method, err)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	wait := func(r reply, ok bool) (json.RawMessage, error) {
		if !ok {
			return nil, p.gone()
		}
		if r.err != nil {
			return nil, r.err
		}
		return r.result, nil
	}
	select {
	case r, ok := <-ch:
		return wait(r, ok)
	case <-p.done:
		select {
		case r, ok := <-ch:
			return wait(r, ok)
		default:
		}
		forget()
		return nil, p.gone()
	case <-ctx.Done():
		p.notify("$/cancel", map[string]any{"id": id})
		forget()
		return nil, ctx.Err()
	case <-timer.C:
	}
	p.notify("$/cancel", map[string]any{"id": id})
	if grace > 0 {
		g := time.NewTimer(grace)
		defer g.Stop()
		select {
		case r, ok := <-ch:
			if ok && r.err != nil && r.err.Code == CodeCancelled {
				forget()
				return nil, errTimeout
			}
			return wait(r, ok)
		case <-g.C:
		}
	}
	forget()
	return nil, errTimeout
}

func (p *proc) gone() error {
	return fmt.Errorf("plugin %s stopped: %v", p.name, p.exitError())
}

// readStdout reads one JSON-RPC message per line. Anything that is not one
// is a protocol violation: stdout belongs to the protocol, so the process
// is stopped and starts again on the next call.
func (p *proc) readStdout(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := readLine(br)
		if len(bytes.TrimSpace(line)) > 0 && !p.handle(line) {
			_, _ = io.Copy(io.Discard, br)
			return
		}
		if err != nil {
			if errors.Is(err, errTooLong) {
				p.violate(fmt.Errorf("it wrote a line longer than %d bytes on stdout", MaxMessage))
				_, _ = io.Copy(io.Discard, br)
			}
			return
		}
	}
}

var errTooLong = errors.New("line too long")

func readLine(br *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > MaxMessage {
			return nil, errTooLong
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return buf, err
	}
}

func (p *proc) violate(err error) {
	p.log.Error("plugin broke the protocol; stopping it", "err", err)
	p.mu.Lock()
	p.protoErr = fmt.Errorf("it broke the protocol: %w", err)
	pending := p.pending
	p.pending = map[int64]chan reply{}
	p.mu.Unlock()
	for _, ch := range pending {
		close(ch)
	}
	_ = killGroup(p.cmd)
}

// handle dispatches one line; false means the process was stopped.
func (p *proc) handle(line []byte) bool {
	var m message
	if err := json.Unmarshal(line, &m); err != nil {
		p.violate(fmt.Errorf("it wrote a line on stdout that is not JSON (%.80q): %w", string(line), err))
		return false
	}
	if m.JSONRPC != "2.0" {
		p.violate(fmt.Errorf("it wrote a message without \"jsonrpc\": \"2.0\" (%.80q)", string(line)))
		return false
	}
	switch {
	case m.Method != "" && len(m.ID) == 0:
		p.notification(m)
	case m.Method != "":
		// The host serves no methods in this protocol version.
		_ = p.write(message{ID: m.ID, Error: &Error{Code: CodeMethodNotFound, Message: "the host serves no method " + m.Method}})
	default:
		var id int64
		if err := json.Unmarshal(m.ID, &id); err != nil {
			p.log.Warn("plugin answered with an id the host never sent", "id", string(m.ID))
			return true
		}
		p.mu.Lock()
		ch := p.pending[id]
		delete(p.pending, id)
		p.mu.Unlock()
		if ch == nil {
			return true // late answer to a cancelled call
		}
		ch <- reply{result: m.Result, err: m.Error}
	}
	return true
}

func (p *proc) notification(m message) {
	switch m.Method {
	case "$/log":
		var l struct {
			Level   string `json:"level"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(m.Params, &l)
		lvl := slog.LevelInfo
		switch strings.ToLower(l.Level) {
		case "debug":
			lvl = slog.LevelDebug
		case "warn", "warning":
			lvl = slog.LevelWarn
		case "error":
			lvl = slog.LevelError
		}
		p.log.Log(context.Background(), lvl, l.Message)
	default:
		p.log.Debug("plugin sent an unknown notification", "method", m.Method)
	}
}

// readStderr logs what the plugin writes on stderr, line by line.
func (p *proc) readStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if t := strings.TrimSpace(sc.Text()); t != "" {
			p.log.Info("plugin stderr", "line", t)
		}
	}
	_, _ = io.Copy(io.Discard, r)
}
