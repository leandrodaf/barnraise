package acp

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/leandrodaf/barnraise/internal/adapters/out/provcommon"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// defaultOutputLimit bounds what a terminal keeps when the agent sets no limit.
const defaultOutputLimit = 1 << 20

// maxTerminals bounds the terminals one session may hold at once.
const maxTerminals = 32

type terminal struct {
	cmd    *exec.Cmd
	out    *limitBuffer
	done   chan struct{}
	status exitStatus
}

type terminals struct {
	mu   sync.Mutex
	next int
	m    map[string]*terminal
}

func (c *client) createTerminal(_ context.Context, command string, args []string, env map[string]string, cwd string, limit int) (string, *RPCError) {
	if strings.TrimSpace(command) == "" {
		return "", &RPCError{Code: codeInvalidParams, Message: "command is required"}
	}
	if cwd == "" {
		cwd = c.workdir
	}
	cwd = c.abs(cwd)
	line := shellJoin(command, args)
	if d := c.decide(domain.ToolCall{Tool: "Bash", Command: line}, cwd); !d.Allow {
		c.violation("Bash", d, line)
		return "", policyError(d)
	}
	if !c.visible(cwd) {
		return "", &RPCError{Code: codePolicy, Message: "Barnraise policy: " + cwd + " is not mounted in the agent's container"}
	}
	c.terms.mu.Lock()
	running := len(c.terms.m)
	c.terms.mu.Unlock()
	if running >= maxTerminals {
		return "", &RPCError{Code: codeInternal, Message: fmt.Sprintf("too many terminals (%d); release the ones that are done", maxTerminals)}
	}

	e := provcommon.MinimalEnv()
	for _, kv := range c.env.List() {
		k, v, _ := strings.Cut(kv, "=")
		e.Set(k, v)
	}
	for _, k := range sortedKeys(env) {
		if !reservedTermEnv[k] {
			e.Set(k, env[k])
		}
	}
	name, a := command, args
	switch {
	case c.container != nil:
		// The command runs in a container of its own made like the
		// session's: same image, same mounts, nothing else of the machine.
		name, a = provcommon.ContainerArgs(c.container, e, cwd, name, a)
	default:
		if c.sandbox {
			cache := filepath.Join(c.workdir, ".barnraise-session", "cache")
			// The home folder is read-only in the sandbox: caches go to the
			// session's folder, kept from one command to the next.
			if _, ok := env["XDG_CACHE_HOME"]; !ok {
				e.Set("XDG_CACHE_HOME", cache)
			}
			if _, ok := env["GOCACHE"]; !ok {
				e.Set("GOCACHE", filepath.Join(cache, "go-build"))
			}
			sock, _ := e.Get("BARNRAISE_SOCKET")
			name, a = sandboxArgs(c.pol, c.workdir, sock, cwd, name, a)
		}
		if c.runAs != "" {
			name, a = provcommon.SudoArgs(c.runAs, e, name, a)
		}
	}
	if limit <= 0 {
		limit = defaultOutputLimit
	}
	cmd := c.command(context.Background(), name, a...)
	cmd.Dir = cwd
	if c.container != nil {
		cmd.Dir = "" // the path is the container's; the engine changes into it
	}
	cmd.Env = append(cmd.Env, e.List()...)
	buf := &limitBuffer{limit: limit}
	cmd.Stdout, cmd.Stderr = buf, buf
	cmd.WaitDelay = time.Second
	provcommon.SetGroup(cmd)
	if err := cmd.Start(); err != nil {
		return "", &RPCError{Code: codeInternal, Message: "start " + command + ": " + err.Error()}
	}
	t := &terminal{cmd: cmd, out: buf, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		var st exitStatus
		switch ps := cmd.ProcessState; {
		case ps == nil:
			code := 127
			st.Code = &code
		case ps.ExitCode() >= 0:
			code := ps.ExitCode()
			st.Code = &code
		default:
			sig := signalName(ps.String())
			st.Signal = &sig
		}
		t.status = st
		close(t.done)
	}()
	c.terms.mu.Lock()
	c.terms.next++
	id := "term-" + strconv.Itoa(c.terms.next)
	c.terms.m[id] = t
	c.terms.mu.Unlock()
	return id, nil
}

// reservedTermEnv cannot be replaced by the agent's request: they tie the
// command to this session and its policy.
var reservedTermEnv = map[string]bool{"BARNRAISE_TOKEN": true, "BARNRAISE_POLICY": true, "BARNRAISE_SOCKET": true,
	"BARNRAISE_WORKTREE": true, "HTTPS_PROXY": true, "HTTP_PROXY": true, "https_proxy": true, "http_proxy": true,
	"NO_PROXY": true, "no_proxy": true, "LD_PRELOAD": true, "LD_LIBRARY_PATH": true, "DYLD_INSERT_LIBRARIES": true}

func signalName(state string) string {
	if s, ok := strings.CutPrefix(state, "signal: "); ok {
		return s
	}
	return state
}

func (c *client) terminal(id string) (*terminal, *RPCError) {
	c.terms.mu.Lock()
	defer c.terms.mu.Unlock()
	t, ok := c.terms.m[id]
	if !ok {
		return nil, &RPCError{Code: codeInvalidParams, Message: "unknown terminal " + id}
	}
	return t, nil
}

func (c *client) terminalOutput(id string) (string, bool, *exitStatus, *RPCError) {
	t, rerr := c.terminal(id)
	if rerr != nil {
		return "", false, nil, rerr
	}
	out, truncated := t.out.snapshot()
	select {
	case <-t.done:
		st := t.status
		return out, truncated, &st, nil
	default:
		return out, truncated, nil, nil
	}
}

func (c *client) waitTerminal(ctx context.Context, id string) (exitStatus, *RPCError) {
	t, rerr := c.terminal(id)
	if rerr != nil {
		return exitStatus{}, rerr
	}
	select {
	case <-t.done:
		return t.status, nil
	case <-ctx.Done():
		return exitStatus{}, &RPCError{Code: codeInternal, Message: "session ended"}
	}
}

func (c *client) killTerminal(id string) *RPCError {
	t, rerr := c.terminal(id)
	if rerr != nil {
		return rerr
	}
	_ = provcommon.KillGroup(t.cmd)
	return nil
}

func (c *client) releaseTerminal(id string) *RPCError {
	t, rerr := c.terminal(id)
	if rerr != nil {
		return rerr
	}
	_ = provcommon.KillGroup(t.cmd)
	c.terms.mu.Lock()
	delete(c.terms.m, id)
	c.terms.mu.Unlock()
	return nil
}

// closeTerminals kills what the agent left running when the session ends.
func (c *client) closeTerminals() {
	c.terms.mu.Lock()
	ts := make([]*terminal, 0, len(c.terms.m))
	for id, t := range c.terms.m {
		ts = append(ts, t)
		delete(c.terms.m, id)
	}
	c.terms.mu.Unlock()
	for _, t := range ts {
		_ = provcommon.KillGroup(t.cmd)
		select {
		case <-t.done:
		case <-time.After(3 * time.Second):
		}
	}
}

// shellJoin writes a command line the way a shell would read it, for the
// policy (which matches command text) and for messages.
func shellJoin(command string, args []string) string {
	parts := []string{command}
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n'\"\\$`|&;<>()*?[]{}~#!") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// limitBuffer keeps the last limit bytes written, cut on a character boundary.
type limitBuffer struct {
	mu        sync.Mutex
	limit     int
	buf       []byte
	truncated bool
}

func (b *limitBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.limit; over > 0 {
		for over < len(b.buf) && !utf8.RuneStart(b.buf[over]) {
			over++
		}
		b.buf = append(b.buf[:0], b.buf[over:]...)
		b.truncated = true
	}
	return len(p), nil
}

func (b *limitBuffer) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf), b.truncated
}
