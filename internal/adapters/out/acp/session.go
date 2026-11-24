package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/provcommon"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// codeAuthRequired is the error ACP agents answer with when they need a login.
const codeAuthRequired = -32000

// stderrTail is how much of the agent's stderr is kept for error messages.
const stderrTail = 16 << 10

// session runs one agent program and one prompt turn; it implements ports.Session.
type session struct {
	p      *Provider
	spec   ports.SessionSpec
	name   string
	cmd    *exec.Cmd
	conn   *Conn
	client *client
	stderr *limitBuffer
	log    *transcript
	start  time.Time

	queue  *provcommon.EventQueue
	events chan ports.ProviderEvent

	dmu     sync.Mutex
	d       dialect
	loading atomic.Bool

	mu        sync.Mutex
	sessionID string
	msg       strings.Builder // the agent's last message
	msgID     string
	usage     domain.TokenUsage
	limited   bool
	resetsAt  time.Time

	stopOnce sync.Once
	stopCh   chan struct{}
	exited   chan struct{}
	served   chan struct{}
	done     chan struct{}
	result   ports.SessionResult
	err      error
}

func (p *Provider) start(ctx context.Context, spec ports.SessionSpec) (*session, error) {
	pol, err := loadPolicy(spec)
	if err != nil {
		return nil, err
	}
	env := provcommon.MinimalEnv(p.opts.ExtraEnvKeys...)
	env.Merge(spec.Env)
	env.Merge(spec.ACP.Env)
	name, args := spec.ACP.Command, append([]string(nil), spec.ACP.Args...)
	if spec.Container != nil {
		name, args = provcommon.ContainerArgs(spec.Container, env, spec.Workdir, name, args)
	} else if spec.RunAsUser != "" {
		name, args = provcommon.SudoArgs(spec.RunAsUser, env, name, args)
	}
	cmd := p.opts.Command(context.Background(), name, args...)
	cmd.Dir = spec.Workdir
	cmd.Env = append(cmd.Env, env.List()...)
	provcommon.SetGroup(cmd)
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		return nil, err
	}
	s := &session{p: p, spec: spec, name: "acp " + spec.ACP.Name, cmd: cmd, stderr: &limitBuffer{limit: stderrTail},
		queue: provcommon.NewEventQueue(), events: make(chan ports.ProviderEvent), d: latest(),
		stopCh: make(chan struct{}), exited: make(chan struct{}), served: make(chan struct{}), done: make(chan struct{})}
	s.usage = domain.TokenUsage{Agent: spec.Agent, Account: spec.Account.ID, Provider: spec.ACP.Name, Model: spec.Model, Role: spec.Role}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, s.stderr
	cmd.WaitDelay = 2 * time.Second
	s.log = openTranscript(spec.Workdir, spec.ACP.Name)
	s.start = time.Now()
	if err := cmd.Start(); err != nil {
		for _, f := range []*os.File{inR, inW, outR, outW} {
			_ = f.Close()
		}
		s.log.close()
		return nil, fmt.Errorf("%s: start %s: %w", s.name, spec.ACP.Command, err)
	}
	_ = inR.Close()
	_ = outW.Close()

	s.client = newClient(p.opts.Command, pol, p.existsOnMain(spec.Workdir, pol.MainBranch), spec, p.sandboxWorks(), env, s.queue.Push)
	s.conn = NewConn(inW, s.onRequest, s.onNotification, s.log.observe)
	go s.queue.Forward(s.events)
	go func() {
		_ = cmd.Wait()
		close(s.exited)
	}()
	go func() {
		s.conn.Serve(outR)
		_ = outR.Close()
		close(s.served)
	}()
	go s.run(ctx)
	return s, nil
}

// Events implements ports.Session.
func (s *session) Events() <-chan ports.ProviderEvent { return s.events }

// Wait implements ports.Session.
func (s *session) Wait() (ports.SessionResult, error) {
	<-s.done
	return s.result, s.err
}

// Stop implements ports.Session: the running turn is cancelled, then the
// program is asked to end, then killed.
func (s *session) Stop() error {
	s.stopOnce.Do(func() { close(s.stopCh) })
	return nil
}

// Pid of the agent program (for diagnostics).
func (s *session) Pid() int {
	if s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

func (s *session) dialect() dialect {
	s.dmu.Lock()
	defer s.dmu.Unlock()
	return s.d
}

func (s *session) onRequest(ctx context.Context, method string, params json.RawMessage) (any, *RPCError) {
	res, rerr, ok := s.dialect().Serve(ctx, s.client, method, params)
	if !ok {
		return nil, &RPCError{Code: codeMethodNotFound, Message: "method not found: " + method}
	}
	return res, rerr
}

func (s *session) onNotification(method string, params json.RawMessage) {
	for _, u := range s.dialect().ParseUpdate(method, params) {
		if u.Kind == updToolCall || u.Kind == updToolUpdate {
			s.client.track(u.Tool)
		}
		if s.loading.Load() {
			continue // the history of a resumed session, already seen
		}
		for _, ev := range s.eventsOf(u) {
			s.queue.Push(ev)
		}
	}
}

// eventsOf maps a session update to the normalized events.
func (s *session) eventsOf(u update) []ports.ProviderEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch u.Kind {
	case updMessage:
		if u.MessageID != s.msgID {
			s.msg.Reset()
			s.msgID = u.MessageID
		}
		s.msg.WriteString(u.Text)
		return []ports.ProviderEvent{{Kind: ports.PEMessage, SessionID: s.sessionID, Text: u.Text, Raw: u.Raw}}
	case updToolCall:
		s.msg.Reset() // the last message is the one after the last tool
		return []ports.ProviderEvent{{Kind: ports.PEToolUse, SessionID: s.sessionID, Tool: toolName(u.Tool), Input: u.Tool.RawInput, Raw: u.Raw}}
	case updToolUpdate:
		if u.Tool.Status != "completed" && u.Tool.Status != "failed" {
			return nil
		}
		return []ports.ProviderEvent{{Kind: ports.PEToolResult, SessionID: s.sessionID, Text: u.Tool.Output, IsError: u.Tool.Status == "failed", Raw: u.Raw}}
	case updUsage:
		if !u.HasCost {
			return nil
		}
		s.usage.CostUSD = u.CostUSD
		usage := s.usage
		return []ports.ProviderEvent{{Kind: ports.PEUsage, SessionID: s.sessionID, Usage: &usage, Raw: u.Raw}}
	}
	return nil
}

func (s *session) push(ev ports.ProviderEvent) { s.queue.Push(ev) }

// run drives the conversation, then ends the program and reports.
func (s *session) run(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var timeout <-chan time.Time
	if s.spec.Timeout > 0 {
		t := time.NewTimer(s.spec.Timeout)
		defer t.Stop()
		timeout = t.C
	}
	// Why the session was ended from our side, if it was.
	var reason atomic.Pointer[error]
	setReason := func(err error) { reason.CompareAndSwap(nil, &err) }
	turnCtx, cancelTurn := context.WithCancel(context.Background())
	defer cancelTurn()
	go func() {
		select {
		case <-s.stopCh:
			setReason(provcommon.ErrStopped)
		case <-runCtx.Done():
			setReason(ctx.Err())
		case <-timeout:
			setReason(fmt.Errorf("%w after %s", provcommon.ErrTimeout, s.spec.Timeout))
		case <-turnCtx.Done():
			return
		}
		// Ask the agent to end its turn; it answers the prompt with "cancelled".
		s.mu.Lock()
		id := s.sessionID
		s.mu.Unlock()
		if id != "" {
			m, params := s.dialect().Cancel(id)
			_ = s.conn.Notify(m, params)
		}
		select {
		case <-time.After(s.p.opts.Grace):
			cancelTurn()
		case <-turnCtx.Done():
		}
	}()

	end, convErr := s.converse(turnCtx)
	cancelTurn()
	s.shutdown()

	code := -1
	if s.cmd.ProcessState != nil {
		code = s.cmd.ProcessState.ExitCode()
	}
	stderr, _ := s.stderr.snapshot()
	var err error
	r := reason.Load()
	switch {
	case r != nil && (convErr != nil || end.StopReason == "cancelled"):
		err = fmt.Errorf("%s: %w", s.name, *r)
	case convErr != nil:
		text := convErr.Error() + " " + stderr
		if provcommon.IsLimitText(text) {
			s.mu.Lock()
			s.limited, s.resetsAt = true, provcommon.ParseResetsAt(text, s.p.opts.Now())
			s.mu.Unlock()
			s.push(ports.ProviderEvent{Kind: ports.PELimit, Text: firstLine(convErr.Error()), ResetsAt: s.resetsAt})
		} else {
			err = fmt.Errorf("%s: %w%s", s.name, convErr, stderrNote(stderr))
			s.push(ports.ProviderEvent{Kind: ports.PEError, Text: err.Error(), IsError: true})
		}
	case end.StopReason == "refusal":
		err = fmt.Errorf("%s: the agent refused the prompt", s.name)
	}

	s.mu.Lock()
	res := ports.SessionResult{SessionID: s.sessionID, Text: strings.TrimSpace(s.msg.String()), Usage: s.usage, ExitCode: code,
		Limited: s.limited, ResetsAt: s.resetsAt, Duration: time.Since(s.start)}
	s.mu.Unlock()
	if res.Usage.Duration == 0 {
		res.Usage.Duration = res.Duration
	}
	if convErr == nil {
		u := res.Usage
		s.push(ports.ProviderEvent{Kind: ports.PEDone, SessionID: res.SessionID, Text: res.Text, Usage: &u, IsError: err != nil})
	}
	s.log.close()
	s.result, s.err = res, err
	s.queue.Close()
	close(s.done)
}

// converse is the protocol's happy path: initialize, open (or resume) the
// session, pick the model, send the prompt and wait for the turn to end.
func (s *session) converse(ctx context.Context) (turnEnd, error) {
	call := func(method string, params any) (json.RawMessage, error) {
		var raw json.RawMessage
		err := s.conn.Call(ctx, method, params, &raw)
		if errors.Is(err, ErrClosed) {
			select {
			case <-s.exited:
				return nil, fmt.Errorf("the agent program ended (exit code %d) before answering %s", s.cmd.ProcessState.ExitCode(), method)
			case <-time.After(s.p.opts.Grace):
				return nil, fmt.Errorf("the agent program closed its output before answering %s", method)
			}
		}
		return raw, err
	}
	m, params := latest().Initialize(clientInfo{Name: "barnraise", Version: s.p.opts.Version, ReadFiles: true, WriteFiles: true, Terminals: true})
	raw, err := call(m, params)
	if err != nil {
		return turnEnd{}, fmt.Errorf("initialize: %w", err)
	}
	info, err := latest().ParseInitialize(raw)
	if err != nil {
		return turnEnd{}, fmt.Errorf("initialize: %w", err)
	}
	d, err := dialectFor(info.Version)
	if err != nil {
		return turnEnd{}, err
	}
	if d != latest() {
		if info, err = d.ParseInitialize(raw); err != nil {
			return turnEnd{}, fmt.Errorf("initialize: %w", err)
		}
	}
	s.dmu.Lock()
	s.d = d
	s.dmu.Unlock()

	mcp, err := mcpServers(s.spec.MCPConfig)
	if err != nil {
		return turnEnd{}, err
	}
	var sess sessionInfo
	if s.spec.ResumeID != "" && info.LoadSession {
		s.loading.Store(true)
		m, params = d.LoadSession(s.spec.ResumeID, s.spec.Workdir, mcp, info)
		raw, err = call(m, params)
		s.loading.Store(false)
		if err == nil {
			sess, err = d.ParseNewSession(raw)
			sess.ID = firstNonEmpty(sess.ID, s.spec.ResumeID)
		}
	} else {
		m, params = d.NewSession(s.spec.Workdir, mcp, info)
		raw, err = call(m, params)
		if err == nil {
			sess, err = d.ParseNewSession(raw)
		}
	}
	var rerr *RPCError
	if errors.As(err, &rerr) && rerr.Code == codeAuthRequired {
		how := ""
		if len(info.AuthMethods) > 0 {
			how = " (" + strings.Join(info.AuthMethods, ", ") + ")"
		}
		return turnEnd{}, fmt.Errorf("the agent needs a login%s: run %s once by hand as the account's user and log in: %w", how, s.spec.ACP.Command, err)
	}
	if err != nil {
		return turnEnd{}, fmt.Errorf("open session: %w", err)
	}
	if sess.ID == "" {
		return turnEnd{}, errors.New("open session: the agent gave no session id")
	}
	s.mu.Lock()
	s.sessionID = sess.ID
	s.mu.Unlock()

	if model := s.spec.Model; model != "" && model != "default" {
		if m, params, ok := d.SelectModel(sess, model); ok {
			if _, err := call(m, params); err != nil {
				s.push(ports.ProviderEvent{Kind: ports.PEError, SessionID: sess.ID, Text: "model " + model + " not selected: " + err.Error()})
			}
		}
	}
	s.push(ports.ProviderEvent{Kind: ports.PEInit, SessionID: sess.ID, Text: firstNonEmpty(s.spec.Model, info.Name)})

	m, params = d.Prompt(sess.ID, []string{s.spec.SystemPrompt, s.spec.Prompt})
	raw, err = call(m, params)
	if err != nil {
		return turnEnd{}, fmt.Errorf("prompt: %w", err)
	}
	end, err := d.ParsePrompt(raw)
	if err != nil {
		return turnEnd{}, fmt.Errorf("prompt: %w", err)
	}
	if end.Usage != nil {
		s.mu.Lock()
		s.usage.Input += end.Usage.Input
		s.usage.Output += end.Usage.Output
		s.usage.CacheRead += end.Usage.CacheRead
		s.usage.CacheWrite += end.Usage.CacheWrite
		u := s.usage
		s.mu.Unlock()
		s.push(ports.ProviderEvent{Kind: ports.PEUsage, SessionID: sess.ID, Usage: &u})
	}
	return end, nil
}

// shutdown ends the program: stdin closed (agents end on it), then SIGINT,
// then SIGKILL, each after the grace period. Terminals go with it.
func (s *session) shutdown() {
	s.conn.CloseWrite()
	grace := s.p.opts.Grace
	select {
	case <-s.exited:
	case <-time.After(grace):
		_ = provcommon.InterruptGroup(s.cmd)
		select {
		case <-s.exited:
		case <-time.After(grace):
			_ = provcommon.KillGroup(s.cmd)
			<-s.exited
		}
	}
	// Children left behind may keep stdout open.
	select {
	case <-s.served:
	case <-time.After(2 * time.Second):
		_ = provcommon.KillGroup(s.cmd)
		<-s.served
	}
	s.client.closeTerminals()
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

func stderrNote(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}
	lines := strings.Split(stderr, "\n")
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	return ": " + strings.Join(lines, " | ")
}

// transcript is the session's whole conversation, one JSON line per
// message in either direction, in the session folder of the worktree
// (excluded from git). It is what to read when an agent misbehaves.
type transcript struct {
	mu      sync.Mutex
	f       *os.File
	written int64
}

// maxTranscript bounds the transcript of one session.
const maxTranscript = 64 << 20

func openTranscript(workdir, agent string) *transcript {
	dir := filepath.Join(workdir, ".barnraise-session")
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return &transcript{}
	}
	f, err := os.OpenFile(filepath.Join(dir, "acp-"+safeName(agent)+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return &transcript{}
	}
	return &transcript{f: f}
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return '_'
	}, strings.ToLower(s))
}

func (t *transcript) observe(incoming bool, line []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.f == nil || t.written > maxTranscript {
		return
	}
	dir := "out"
	if incoming {
		dir = "in"
	}
	msg := json.RawMessage(line)
	if !json.Valid(line) {
		msg, _ = json.Marshal(string(line))
	}
	b, _ := json.Marshal(struct {
		At  time.Time       `json:"at"`
		Dir string          `json:"dir"`
		Msg json.RawMessage `json:"msg"`
	}{time.Now().UTC(), dir, msg})
	n, _ := t.f.Write(append(b, '\n'))
	t.written += int64(n)
}

func (t *transcript) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.f != nil {
		_ = t.f.Close()
		t.f = nil
	}
}

var _ ports.Session = (*session)(nil)
