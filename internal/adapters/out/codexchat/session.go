// Package codexchat runs the owner's conversation on Codex: one thread on the
// local Codex app-server daemon, started with Remote Control on, so the
// ChatGPT app on a phone (paired once with a code) shows and drives the same
// thread the panel and the command line talk to. The daemon speaks JSON-RPC
// over a WebSocket on a Unix socket under the account's CODEX_HOME.
package codexchat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// Session is the conversation's Codex thread on this machine.
type Session struct {
	// Bin is the codex command (default "codex" on the PATH).
	Bin string

	mu      sync.Mutex
	conn    *client
	spec    ports.ChatSpec
	thread  string
	turn    string // the turn under way, "" when idle
	remote  string // Remote Control status: connected, connecting, errored, disabled
	started time.Time
	err     string
}

var (
	_ ports.ChatSession = (*Session)(nil)
	_ ports.ChatPairer  = (*Session)(nil)
)

// New returns a session using the codex command on the PATH.
func New() *Session { return &Session{} }

func (s *Session) bin() string {
	if s.Bin != "" {
		return s.Bin
	}
	return "codex"
}

// home is the account's CODEX_HOME (default ~/.codex).
func home(configDir string) string {
	if configDir != "" {
		return configDir
	}
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".codex")
}

// SocketPath is where the account's app-server daemon listens.
func SocketPath(configDir string) string {
	return filepath.Join(home(configDir), "app-server-control", "app-server-control.sock")
}

func (s *Session) command(ctx context.Context, configDir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, s.bin(), args...)
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home(configDir))
	return cmd
}

// ensureDaemon starts the account's app-server daemon with Remote Control on
// (it does nothing when it runs).
func (s *Session) ensureDaemon(configDir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := s.command(ctx, configDir, "remote-control", "start", "--json").CombinedOutput()
	if err != nil {
		return fmt.Errorf("codexchat: codex remote-control start: %w: %s", err, lastLine(string(out)))
	}
	return nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	l := lines[len(lines)-1]
	if r := []rune(l); len(r) > 300 {
		l = string(r[:300])
	}
	return l
}

// sandbox: the thread writes only in its own (empty) folder, and reaches
// the network, which the barnraise command's Unix socket needs: read-only
// Codex blocks every socket.
const sandbox = "workspace-write"

// sandboxConfig lets the workspace-write sandbox open sockets.
var sandboxConfig = map[string]any{"sandbox_workspace_write": map[string]any{"network_access": true}}

// Start connects to the account's daemon and opens the conversation's
// thread: SessionID resumes it; "" starts one. The id it runs is returned.
func (s *Session) Start(spec ports.ChatSpec) (string, error) {
	s.mu.Lock()
	if s.conn != nil {
		id := s.thread
		s.mu.Unlock()
		return id, nil
	}
	s.mu.Unlock()
	if _, err := exec.LookPath(s.bin()); err != nil {
		return "", fmt.Errorf("codexchat: %s is not installed: %w", s.bin(), err)
	}
	if err := s.ensureDaemon(spec.ConfigDir); err != nil {
		return "", err
	}
	c, err := dial(SocketPath(spec.ConfigDir), s.notify)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := c.call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "barnraise", "title": "Barnraise", "version": "1"}}); err != nil {
		c.close()
		return "", err
	}
	_ = c.notifyServer("initialized")
	params := map[string]any{"cwd": spec.Dir, "developerInstructions": spec.SystemPrompt, "approvalPolicy": "never", "sandbox": sandbox, "config": sandboxConfig}
	if spec.Model != "" {
		params["model"] = spec.Model
	}
	method := "thread/start"
	if spec.Resume && spec.SessionID != "" {
		method, params["threadId"] = "thread/resume", spec.SessionID
	}
	res, err := c.call(ctx, method, params)
	if err != nil && method == "thread/resume" {
		// A thread the daemon no longer has: start a new one.
		delete(params, "threadId")
		res, err = c.call(ctx, "thread/start", params)
	}
	if err != nil {
		c.close()
		return "", err
	}
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(res, &started); err != nil || started.Thread.ID == "" {
		c.close()
		return "", fmt.Errorf("codexchat: %s answered no thread: %s", method, res)
	}
	if spec.Name != "" {
		_, _ = c.call(ctx, "thread/name/set", map[string]any{"threadId": started.Thread.ID, "name": spec.Name})
	}
	s.mu.Lock()
	s.conn, s.spec, s.thread, s.turn, s.started, s.err = c, spec, started.Thread.ID, "", time.Now(), ""
	if s.remote == "" {
		s.remote = "connecting"
	}
	s.mu.Unlock()
	go func() {
		<-c.done
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.conn == c {
			s.conn, s.turn = nil, ""
			s.err = "the Codex app-server closed the connection"
		}
	}()
	return started.Thread.ID, nil
}

// notify follows the daemon: turns starting and ending, Remote Control.
func (s *Session) notify(method string, params json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var p struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
		Status json.RawMessage `json:"status"`
	}
	_ = json.Unmarshal(params, &p)
	if p.ThreadID != "" && p.ThreadID != s.thread {
		return
	}
	switch method {
	case "turn/started":
		s.turn = p.Turn.ID
	case "turn/completed":
		s.turn = ""
	case "remoteControl/status/changed":
		var st string
		if json.Unmarshal(p.Status, &st) == nil {
			s.remote = st
		}
	}
}

// ErrNotRunning: the session is not running.
var ErrNotRunning = errors.New("codexchat: the conversation session is not running")

func (s *Session) live() (*client, string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil, "", "", ErrNotRunning
	}
	return s.conn, s.thread, s.turn, nil
}

// Send starts a turn with the message, or steers the turn under way.
func (s *Session) Send(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("codexchat: empty message")
	}
	c, thread, turn, err := s.live()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	input := []map[string]string{{"type": "text", "text": text}}
	if turn != "" {
		if _, err := c.call(ctx, "turn/steer", map[string]any{"threadId": thread, "expectedTurnId": turn, "input": input}); err == nil {
			return nil
		}
	}
	res, err := c.call(ctx, "turn/start", map[string]any{"threadId": thread, "input": input})
	if err != nil {
		return err
	}
	var started struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(res, &started) == nil && started.Turn.ID != "" {
		s.mu.Lock()
		s.turn = started.Turn.ID
		s.mu.Unlock()
	}
	return nil
}

// Interrupt stops the turn under way.
func (s *Session) Interrupt() error {
	c, thread, turn, err := s.live()
	if err != nil {
		return err
	}
	if turn == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err = c.call(ctx, "turn/interrupt", map[string]any{"threadId": thread, "turnId": turn})
	return err
}

// Stop leaves the thread: it stays on the daemon (and on the phone) and
// resumes on the next Start.
func (s *Session) Stop() error {
	s.mu.Lock()
	c := s.conn
	s.conn, s.turn, s.err = nil, "", ""
	s.mu.Unlock()
	if c != nil {
		c.close()
	}
	return nil
}

// Status says whether the thread is open, whether a turn runs and how Remote
// Control stands.
func (s *Session) Status() ports.ChatStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := ports.ChatStatus{Running: s.conn != nil, Err: s.err, Provider: "codex"}
	if st.Running {
		st.Busy, st.StartedAt, st.Remote = s.turn != "", s.started, s.remote
	}
	return st
}

// Pair asks the daemon for a short-lived code that pairs the ChatGPT app.
func (s *Session) Pair(configDir string) (ports.ChatPairing, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := s.ensureDaemon(configDir); err != nil {
		return ports.ChatPairing{}, err
	}
	out, err := s.command(ctx, configDir, "remote-control", "pair", "--json").Output()
	if err != nil {
		return ports.ChatPairing{}, fmt.Errorf("codexchat: codex remote-control pair: %w", err)
	}
	var p struct {
		Code      string `json:"pairingCode"`
		Manual    string `json:"manualPairingCode"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	for _, l := range strings.Split(string(out), "\n") {
		if json.Unmarshal([]byte(l), &p) == nil && p.Manual != "" {
			return ports.ChatPairing{Code: p.Code, Manual: p.Manual, Expires: time.Unix(p.ExpiresAt, 0).UTC()}, nil
		}
	}
	return ports.ChatPairing{}, fmt.Errorf("codexchat: no pairing code in %q", lastLine(string(out)))
}

// client is one JSON-RPC connection to the daemon.
type client struct {
	ws      *websocket.Conn
	ctx     context.Context
	cancel  context.CancelFunc
	next    atomic.Int64
	mu      sync.Mutex
	waiting map[int64]chan rpcReply
	done    chan struct{}
	onNote  func(string, json.RawMessage)
}

type rpcReply struct {
	Result json.RawMessage
	Err    error
}

func dial(sock string, onNote func(string, json.RawMessage)) (*client, error) {
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}}
	ctx, cancel := context.WithCancel(context.Background())
	dctx, dcancel := context.WithTimeout(ctx, 15*time.Second)
	defer dcancel()
	ws, _, err := websocket.Dial(dctx, "ws://localhost/", &websocket.DialOptions{HTTPClient: hc})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("codexchat: the Codex app-server at %s: %w", sock, err)
	}
	ws.SetReadLimit(64 << 20)
	c := &client{ws: ws, ctx: ctx, cancel: cancel, waiting: map[int64]chan rpcReply{}, done: make(chan struct{}), onNote: onNote}
	go c.read()
	return c, nil
}

func (c *client) read() {
	defer close(c.done)
	defer c.failAll(errors.New("codexchat: connection closed"))
	for {
		_, b, err := c.ws.Read(c.ctx)
		if err != nil {
			return
		}
		var m struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		switch {
		case m.ID != nil && m.Method == "":
			c.mu.Lock()
			ch := c.waiting[*m.ID]
			delete(c.waiting, *m.ID)
			c.mu.Unlock()
			if ch != nil {
				r := rpcReply{Result: m.Result}
				if m.Error != nil {
					r.Err = errors.New("codex: " + m.Error.Message)
				}
				ch <- r
			}
		case m.ID != nil:
			// A request of the server (an approval): the thread runs with
			// approvals off, so anything asked is declined.
			_ = c.write(map[string]any{"jsonrpc": "2.0", "id": *m.ID, "error": map[string]any{"code": -32601, "message": "barnraise answers no requests"}})
		case m.Method != "" && c.onNote != nil:
			c.onNote(m.Method, m.Params)
		}
	}
}

func (c *client) failAll(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, ch := range c.waiting {
		ch <- rpcReply{Err: err}
		delete(c.waiting, id)
	}
}

func (c *client) write(v any) error {
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
	defer cancel()
	return c.ws.Write(ctx, websocket.MessageText, b)
}

func (c *client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.next.Add(1)
	ch := make(chan rpcReply, 1)
	c.mu.Lock()
	c.waiting[id] = ch
	c.mu.Unlock()
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		c.mu.Lock()
		delete(c.waiting, id)
		c.mu.Unlock()
		return nil, err
	}
	select {
	case r := <-ch:
		return r.Result, r.Err
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.waiting, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("codexchat: %s: %w", method, ctx.Err())
	}
}

func (c *client) notifyServer(method string) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method})
}

func (c *client) close() {
	_ = c.ws.Close(websocket.StatusNormalClosure, "")
	c.cancel()
}
