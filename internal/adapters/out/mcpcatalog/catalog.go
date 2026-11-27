// Package mcpcatalog finds the MCP servers a machine has for a project in
// Claude Code's own configuration, and tests one by listing its tools over
// the MCP protocol (stdio, streamable HTTP or the older HTTP+SSE).
package mcpcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Catalog implements ports.MCPCatalog.
type Catalog struct {
	// Home is the user's home folder (default: os.UserHomeDir).
	Home string
	// Getenv expands ${VAR} in the definitions (default: os.Getenv).
	Getenv func(string) string
	// Timeout bounds one test (default 30s: npx may download the server).
	Timeout time.Duration
}

var _ ports.MCPCatalog = (*Catalog)(nil)

// New returns a catalog for this machine's user.
func New() *Catalog { return &Catalog{} }

// rawServer is a server as Claude Code writes it.
type rawServer struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// Find reads <root>/.mcp.json and ~/.claude.json: the servers for every
// folder (user scope) and those for root only (local scope). A later
// scope does not hide an earlier one of the same name; Claude itself
// prefers local over project over user, and so does the order here.
func (c *Catalog) Find(_ context.Context, root string) ([]domain.MCPFound, error) {
	var out []domain.MCPFound
	seen := map[string]bool{}
	add := func(src string, servers map[string]rawServer) {
		names := make([]string, 0, len(servers))
		for n := range servers {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if seen[n] || n == "barnraise" {
				continue
			}
			seen[n] = true
			out = append(out, domain.MCPFound{Name: n, Source: src, Def: c.def(servers[n])})
		}
	}
	var errs []error
	home := c.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	var user struct {
		MCPServers map[string]rawServer `json:"mcpServers"`
		Projects   map[string]struct {
			MCPServers map[string]rawServer `json:"mcpServers"`
		} `json:"projects"`
	}
	if home != "" {
		if err := readJSON(filepath.Join(home, ".claude.json"), &user); err != nil {
			errs = append(errs, err)
		}
	}
	if root != "" {
		abs, _ := filepath.Abs(root)
		if p, ok := user.Projects[abs]; ok {
			add(domain.MCPFromLocal, p.MCPServers)
		}
		var proj struct {
			MCPServers map[string]rawServer `json:"mcpServers"`
		}
		if err := readJSON(filepath.Join(root, ".mcp.json"), &proj); err != nil {
			errs = append(errs, err)
		}
		add(domain.MCPFromProject, proj.MCPServers)
	}
	add(domain.MCPFromUser, user.MCPServers)
	return out, errors.Join(errs...)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// def turns a Claude server into Barnraise's, expanding ${VAR} and
// ${VAR:-default} as Claude does.
func (c *Catalog) def(r rawServer) domain.MCPServerDef {
	d := domain.MCPServerDef{Command: c.expand(r.Command), URL: c.expand(r.URL)}
	for _, a := range r.Args {
		d.Args = append(d.Args, c.expand(a))
	}
	if len(r.Env) > 0 {
		d.Env = map[string]string{}
		for k, v := range r.Env {
			d.Env[k] = c.expand(v)
		}
	}
	if len(r.Headers) > 0 {
		d.Headers = map[string]string{}
		for k, v := range r.Headers {
			d.Headers[k] = c.expand(v)
		}
	}
	switch r.Type {
	case "sse":
		d.Type = "sse"
	case "http", "streamable-http":
		d.Type = "http"
	}
	if d.URL != "" && d.Type == "" {
		d.Type = "http"
	}
	return d
}

var varRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

func (c *Catalog) expand(s string) string {
	get := c.Getenv
	if get == nil {
		get = os.Getenv
	}
	return varRe.ReplaceAllStringFunc(s, func(m string) string {
		g := varRe.FindStringSubmatch(m)
		if v := get(g[1]); v != "" {
			return v
		}
		return g[3]
	})
}

// Tools runs the MCP handshake with the server and lists its tools.
func (c *Catalog) Tools(ctx context.Context, def domain.MCPServerDef) ([]string, error) {
	t := c.Timeout
	if t <= 0 {
		t = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, t)
	defer cancel()
	var (
		tools []string
		err   error
	)
	switch {
	case def.URL != "" && def.Type == "sse":
		tools, err = sseTools(ctx, def)
	case def.URL != "":
		tools, err = httpTools(ctx, def)
	case def.Command != "":
		tools, err = stdioTools(ctx, def)
	default:
		return nil, errors.New("neither a command nor a URL")
	}
	if err != nil && ctx.Err() != nil {
		return nil, fmt.Errorf("no answer in %s: %w", t, err)
	}
	sort.Strings(tools)
	return tools, err
}

// The JSON-RPC messages of the handshake.
const protocolVersion = "2025-06-18"

func initRequest() map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": protocolVersion, "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "barnraise", "version": "1"},
	}}
}

var (
	initialized = map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}
	listTools   = map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}}
)

// rpcReply is a JSON-RPC response.
type rpcReply struct {
	ID     *json.RawMessage `json:"id"`
	Result json.RawMessage  `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (r rpcReply) idIs(n int) bool {
	if r.ID == nil {
		return false
	}
	var v int
	return json.Unmarshal(*r.ID, &v) == nil && v == n
}

func (r rpcReply) err() error {
	if r.Error != nil {
		return fmt.Errorf("the server answered error %d: %s", r.Error.Code, r.Error.Message)
	}
	return nil
}

// toolNames reads a tools/list result.
func toolNames(raw json.RawMessage) ([]string, error) {
	var res struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	out := make([]string, 0, len(res.Tools))
	for _, t := range res.Tools {
		out = append(out, t.Name)
	}
	return out, nil
}
