package domain

import (
	"slices"
	"sort"
	"strings"
	"time"
)

// The MCP servers agents may use. The owner's own Claude, opened in the
// project's folder, keeps every server of the machine and of the project
// (.mcp.json): Barnraise never touches that configuration. Agents run with
// only Barnraise's server, the `mcp:` servers of barnraise.yaml, and the
// servers the owner lets them use from the panel (an AgentMCPGrant). A
// server is found on each machine in Claude's own configuration, so a
// grant needs no copy of its settings or secrets: a machine that does not
// have it runs its sessions without it, and the panel says so.

// MCP sources: where a machine found a server.
const (
	MCPFromProject   = "project"   // .mcp.json in the project's folder
	MCPFromUser      = "user"      // ~/.claude.json, every folder
	MCPFromLocal     = "local"     // ~/.claude.json, this project's folder only
	MCPFromBarnraise = "barnraise" // mcp: in barnraise.yaml (always given to its roles)
)

// MCPFound is one server a machine has, with its full definition. It
// stays on the machine: its env and headers may hold secrets.
type MCPFound struct {
	Name   string       `json:"name"`
	Source string       `json:"source"`
	Def    MCPServerDef `json:"def"`
}

// Kind is local (a command) or remote (a URL).
func (f MCPFound) Kind() string {
	if f.Def.URL != "" {
		return "remote"
	}
	return "local"
}

// MCPStatus is what a machine tells the cluster about one of its servers:
// never its env or headers, only what it is and whether it answers.
type MCPStatus struct {
	Name     string    `json:"name"`
	Source   string    `json:"source"`
	Kind     string    `json:"kind"`
	Summary  string    `json:"summary,omitempty"` // the command, or the URL's host and path
	OK       bool      `json:"ok,omitempty"`
	Tools    []string  `json:"tools,omitempty"`
	Error    string    `json:"error,omitempty"`
	TestedAt time.Time `json:"tested_at,omitzero"`
}

// StatusOf is the shareable part of a found server.
func StatusOf(f MCPFound) MCPStatus {
	st := MCPStatus{Name: f.Name, Source: f.Source, Kind: f.Kind()}
	if f.Def.URL != "" {
		host, path := urlHostPath(f.Def.URL)
		st.Summary = host + path
	} else {
		st.Summary = strings.TrimSpace(f.Def.Command + " " + strings.Join(f.Def.Args, " "))
		if len(st.Summary) > 120 {
			st.Summary = st.Summary[:120] + "…"
		}
	}
	return st
}

// AgentMCPGrant lets agents use a server the machines have.
type AgentMCPGrant struct {
	Name    string   `json:"name"`
	Allowed bool     `json:"allowed"`
	Roles   []string `json:"roles,omitempty"` // empty: every role
	// Hosts a local server reaches over the network, opened in the egress
	// proxy while it is allowed (a remote server's own host is opened by
	// its URL).
	Hosts []string  `json:"hosts,omitempty"`
	By    string    `json:"by,omitempty"`
	At    time.Time `json:"at,omitzero"`
}

// For reports whether the grant gives the server to role.
func (g *AgentMCPGrant) For(role string) bool {
	return g != nil && g.Allowed && (len(g.Roles) == 0 || slices.Contains(g.Roles, role))
}

// AgentMCPFor returns, for one role, the servers of found that agents may
// use: barnraise.yaml's for the role, and the granted ones. A name in
// barnraise.yaml wins over a found server of the same name.
func AgentMCPFor(cfg *ProjectConfig, grants map[string]*AgentMCPGrant, found []MCPFound, role string) map[string]MCPServerDef {
	out := map[string]MCPServerDef{}
	for _, f := range found {
		if f.Name == "barnraise" || f.Source == MCPFromBarnraise {
			continue
		}
		if g := grants[f.Name]; g.For(role) {
			if _, ok := out[f.Name]; !ok {
				out[f.Name] = f.Def
			}
		}
	}
	if cfg != nil {
		for name, d := range cfg.MCPFor(role) {
			out[name] = d
		}
	}
	return out
}

// AgentMCPHosts are the hosts the granted servers reach: a remote server's
// own host and the hosts declared on the grant.
func AgentMCPHosts(grants map[string]*AgentMCPGrant, found []MCPFound) []string {
	set := map[string]bool{}
	for _, f := range found {
		g := grants[f.Name]
		if g == nil || !g.Allowed {
			continue
		}
		if f.Def.URL != "" {
			if host, _ := urlHostPath(f.Def.URL); host != "" {
				set[strings.ToLower(HostOnly(host))] = true
			}
		}
	}
	for _, g := range grants {
		if g == nil || !g.Allowed {
			continue
		}
		for _, h := range g.Hosts {
			if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
				set[h] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// applyAgentMCP folds a grant: one that is not allowed and says nothing
// else is forgotten.
func (s *State) applyAgentMCP(g *AgentMCPGrant) {
	if g == nil || g.Name == "" {
		return
	}
	if s.AgentMCP == nil {
		s.AgentMCP = map[string]*AgentMCPGrant{}
	}
	if !g.Allowed && len(g.Roles) == 0 && len(g.Hosts) == 0 {
		delete(s.AgentMCP, g.Name)
		return
	}
	c := *g
	s.AgentMCP[g.Name] = &c
}

// urlHostPath splits an http(s) URL into its host (with any port, without
// credentials) and its path, leaving out the query: enough to show a
// server and to open its host, without the adapters' URL parser.
func urlHostPath(raw string) (host, path string) {
	rest := raw
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	host, path = rest, ""
	if i := strings.Index(rest, "/"); i >= 0 {
		host, path = rest[:i], rest[i:]
	}
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	return host, path
}
