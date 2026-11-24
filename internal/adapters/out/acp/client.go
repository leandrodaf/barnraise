package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/leandrodaf/barnraise/internal/adapters/out/provcommon"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// maxReadFile is the largest file handed to the agent in one read.
const maxReadFile = 16 << 20

// client is Barnraise's side of the protocol: what the agent asks of its
// editor (read and write files, ask permission, run a terminal), decided by
// the same policy the hooks of the other providers apply. It is free of any
// protocol version: the dialect decodes the requests into these calls.
type client struct {
	command   provcommon.CommandFunc
	pol       domain.Policy
	exists    func(rel string) bool
	workdir   string
	container *ports.ContainerSpec
	runAs     string
	sandbox   bool
	// confine is Barnraise's own confinement of the session (Landlock),
	// used for terminals where bubblewrap cannot start.
	confine *ports.Confine
	env     *provcommon.Env // the session's environment, for terminals
	emit    func(ports.ProviderEvent)
	terms   terminals

	mu     sync.Mutex
	calls  map[string]*toolCall
	denied map[string]bool // tool calls whose permission was refused
	late   map[string]bool // tool calls already reported after the fact
}

func newClient(command provcommon.CommandFunc, pol domain.Policy, exists func(string) bool, spec ports.SessionSpec, sandbox bool, env *provcommon.Env, emit func(ports.ProviderEvent)) *client {
	return &client{command: command, pol: pol, exists: exists, workdir: spec.Workdir, container: spec.Container, runAs: spec.RunAsUser,
		sandbox: sandbox && spec.Container == nil, confine: spec.Confine, env: env, emit: emit, terms: terminals{m: map[string]*terminal{}},
		calls: map[string]*toolCall{}, denied: map[string]bool{}, late: map[string]bool{}}
}

func policyError(d domain.Decision) *RPCError {
	return &RPCError{Code: codePolicy, Message: "Barnraise policy: " + d.Reason + ". This is blocked by design; continue without it or deliver with a concern."}
}

// decide applies the policy, relative paths resolved from cwd.
func (c *client) decide(tc domain.ToolCall, cwd string) domain.Decision {
	p := c.pol
	if cwd != "" {
		p.Cwd = cwd
	}
	return p.Decide(tc, c.exists)
}

// violation reports a refusal to the daemon (a policy.violation event).
func (c *client) violation(tool string, d domain.Decision, excerpt string) {
	rule, _ := json.Marshal(map[string]string{"rule": d.Rule})
	in, _ := json.Marshal(map[string]string{"excerpt": excerpt})
	c.emit(ports.ProviderEvent{Kind: ports.PEPermission, IsError: true, Tool: tool, Text: d.Reason, Input: in, Raw: rule})
}

// abs resolves a path of the agent (the protocol wants absolute ones).
func (c *client) abs(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(c.workdir, p)
}

// visible reports whether the agent could see path itself: in a container
// only what is mounted is there, so the client does not reach further.
func (c *client) visible(path string) bool {
	if c.container == nil {
		return true
	}
	for _, m := range c.container.Mounts {
		m = strings.TrimSuffix(m, ":ro")
		if path == m || strings.HasPrefix(path, strings.TrimSuffix(m, "/")+"/") {
			return true
		}
	}
	return false
}

func (c *client) readFile(ctx context.Context, path string, line, limit int) (string, *RPCError) {
	if path == "" {
		return "", &RPCError{Code: codeInvalidParams, Message: "path is required"}
	}
	path = c.abs(path)
	if d := c.decide(domain.ToolCall{Tool: "Read", Paths: []string{path}}, ""); !d.Allow {
		c.violation("Read", d, path)
		return "", policyError(d)
	}
	if !c.visible(path) {
		return "", &RPCError{Code: codePolicy, Message: "Barnraise policy: " + path + " is not mounted in the agent's container"}
	}
	var data []byte
	var err error
	if c.runAs != "" {
		data, err = c.asUser(ctx, nil, "cat", "--", path)
	} else {
		var st os.FileInfo
		if st, err = os.Stat(path); err == nil && st.Size() > maxReadFile {
			return "", &RPCError{Code: codeInvalidParams, Message: fmt.Sprintf("%s is larger than %d bytes; read part of it with a terminal", path, maxReadFile)}
		}
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return "", &RPCError{Code: codeInternal, Message: err.Error()}
	}
	return sliceLines(string(data), line, limit), nil
}

// sliceLines keeps limit lines from line (1-based); zeros mean all.
func sliceLines(s string, line, limit int) string {
	if line <= 1 && limit <= 0 {
		return s
	}
	lines := strings.SplitAfter(s, "\n")
	start := max(line-1, 0)
	if start >= len(lines) {
		return ""
	}
	end := len(lines)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	return strings.Join(lines[start:end], "")
}

func (c *client) writeFile(ctx context.Context, path, content string) *RPCError {
	if path == "" {
		return &RPCError{Code: codeInvalidParams, Message: "path is required"}
	}
	path = c.abs(path)
	if d := c.decide(domain.ToolCall{Tool: "Write", Paths: []string{path}, Write: true}, ""); !d.Allow {
		c.violation("Write", d, path)
		return policyError(d)
	}
	if !c.visible(path) {
		return &RPCError{Code: codePolicy, Message: "Barnraise policy: " + path + " is not mounted in the agent's container"}
	}
	var err error
	if c.runAs != "" {
		// As the agent's own user: the file belongs to it, like one its tools wrote.
		if _, err = c.asUser(ctx, nil, "mkdir", "-p", "--", filepath.Dir(path)); err == nil {
			_, err = c.asUser(ctx, strings.NewReader(content), "tee", "--", path)
		}
	} else {
		mode := os.FileMode(0o644)
		if st, serr := os.Stat(path); serr == nil {
			mode = st.Mode().Perm()
		}
		if err = os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			err = os.WriteFile(path, []byte(content), mode)
		}
	}
	if err != nil {
		return &RPCError{Code: codeInternal, Message: err.Error()}
	}
	return nil
}

// asUser runs a small file command as the session's Linux user.
func (c *client) asUser(ctx context.Context, stdin *strings.Reader, name string, args ...string) ([]byte, error) {
	env := provcommon.MinimalEnv()
	prog, a := provcommon.SudoArgs(c.runAs, env, name, args)
	cmd := c.command(ctx, prog, a...)
	cmd.Env = append(cmd.Env, env.List()...)
	var out, errb bytes.Buffer
	if name == "tee" {
		cmd.Stdout = nil // tee echoes the content; it is not wanted
	} else {
		cmd.Stdout = &out
	}
	cmd.Stderr = &errb
	if stdin != nil {
		cmd.Stdin = stdin
	}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s as %s: %w: %s", name, c.runAs, err, strings.TrimSpace(errb.String()))
	}
	if out.Len() > maxReadFile {
		return nil, fmt.Errorf("file larger than %d bytes", maxReadFile)
	}
	return out.Bytes(), nil
}

// call turns a tool call of the agent into the policy's shape.
func (c *client) call(t toolCall) domain.ToolCall {
	var in map[string]any
	_ = json.Unmarshal(t.RawInput, &in)
	paths := append([]string(nil), t.Paths...)
	for _, k := range []string{"path", "file_path", "filePath", "abs_path", "absolute_path", "notebook_path", "source", "destination", "old_path", "new_path"} {
		if v, ok := in[k].(string); ok && v != "" && !slices.Contains(paths, v) {
			paths = append(paths, v)
		}
	}
	for i, p := range paths {
		paths[i] = c.abs(p)
	}
	switch t.Kind {
	case "execute":
		cmd := inputCommand(in)
		if cmd == "" {
			cmd = strings.Trim(strings.TrimSpace(t.Title), "`")
		}
		return domain.ToolCall{Tool: "Bash", Command: cmd}
	case "edit", "delete", "move":
		return domain.ToolCall{Tool: "Edit", Paths: paths, Write: true}
	case "read", "search":
		return domain.ToolCall{Tool: "Read", Paths: paths}
	case "fetch":
		u, _ := in["url"].(string)
		return domain.ToolCall{Tool: "WebFetch", URL: u}
	}
	return domain.ToolCall{Tool: firstNonEmpty(t.Kind, "other"), Paths: paths, Command: inputCommand(in)}
}

// inputCommand reads a shell command from a tool's raw input.
func inputCommand(in map[string]any) string {
	var parts []string
	for _, k := range []string{"command", "cmd"} {
		switch v := in[k].(type) {
		case string:
			parts = append(parts, v)
		case []any:
			for _, a := range v {
				parts = append(parts, fmt.Sprint(a))
			}
		}
		if len(parts) > 0 {
			break
		}
	}
	if len(parts) == 0 {
		return ""
	}
	if args, ok := in["args"].([]any); ok {
		for _, a := range args {
			parts = append(parts, fmt.Sprint(a))
		}
	}
	return strings.Join(parts, " ")
}

// permission answers a permission request with the policy: the agent asked,
// Barnraise decides, no person is waited for. A mode switch is refused: the
// session stays in the mode it started with, so the agent keeps asking.
func (c *client) permission(_ context.Context, t toolCall, opts []permOption) (string, bool) {
	c.track(t)
	var d domain.Decision
	if t.Kind == "switch_mode" {
		d = domain.Decision{Rule: "mode", Reason: "an agent session cannot change its own permission mode"}
	} else {
		d = c.decide(c.call(t), "")
	}
	c.mu.Lock()
	if t.ID != "" {
		c.denied[t.ID] = !d.Allow
	}
	c.mu.Unlock()
	if !d.Allow {
		c.violation(toolName(t), d, firstNonEmpty(t.Title, string(t.RawInput)))
		return pick(opts, "reject_once", "reject_always")
	}
	return pick(opts, "allow_once", "allow_always")
}

// pick returns the first option of the kinds given, in that order; the
// request is answered "cancelled" when the agent offered none of them.
func pick(opts []permOption, kinds ...string) (string, bool) {
	for _, k := range kinds {
		for _, o := range opts {
			if o.Kind == k {
				return o.ID, true
			}
		}
	}
	return "", false
}

// track follows the tool calls the agent reports. An edit it reports done
// without having asked is checked after the fact: it cannot be undone from
// here, but the owner hears about it like any other violation.
func (c *client) track(u toolCall) {
	if u.ID == "" {
		return
	}
	c.mu.Lock()
	t, ok := c.calls[u.ID]
	if !ok {
		t = &toolCall{ID: u.ID}
		c.calls[u.ID] = t
	}
	t.merge(u)
	check := t.Status == "completed" && (t.Kind == "edit" || t.Kind == "delete" || t.Kind == "move") && !c.denied[t.ID] && !c.late[t.ID]
	snapshot := *t
	if check {
		c.late[t.ID] = true
	}
	c.mu.Unlock()
	if !check {
		return
	}
	if d := c.decide(c.call(snapshot), ""); !d.Allow {
		d.Reason += " (the agent did this with its own tool without asking; reported after the fact)"
		c.violation(toolName(snapshot), d, firstNonEmpty(snapshot.Title, strings.Join(snapshot.Paths, " ")))
	}
}

// toolName is the Barnraise name of an ACP tool kind (the names the other
// providers use, so the counters of the runtime work the same).
func toolName(t toolCall) string {
	switch t.Kind {
	case "read":
		return "Read"
	case "search":
		return "Grep"
	case "edit":
		return "Edit"
	case "delete":
		return "Delete"
	case "move":
		return "Move"
	case "execute":
		return "Bash"
	case "fetch":
		return "WebFetch"
	case "think":
		return "Think"
	case "switch_mode":
		return "SwitchMode"
	}
	return firstNonEmpty(t.Title, t.Kind, "tool")
}
