package app

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// MCP servers for agents (see domain/agentmcp.go). Each machine finds the
// servers it has in Claude's own configuration and barnraise.yaml, tests
// them now and then, and tells the cluster what it has in its heartbeat
// (never env or headers). The owner lets agents use a server from the
// panel; a session gets the servers its role may use that its machine has.

const (
	// mcpFindEvery is how long what a machine found is reused.
	mcpFindEvery = 30 * time.Second
	// mcpTestEvery spaces the background tests of every server.
	mcpTestEvery = 30 * time.Minute
)

// mcpCache is one project's servers on this machine and their last tests.
type mcpCache struct {
	mu      sync.Mutex
	found   []domain.MCPFound
	foundAt time.Time
	tests   map[string]domain.MCPStatus
	testing atomic.Bool
	testAt  time.Time
}

// MCPFound returns the servers this machine has for the project: Claude's
// (.mcp.json, ~/.claude.json) and barnraise.yaml's.
func (s *Service) MCPFound(ctx context.Context, p *Project) []domain.MCPFound {
	c := &p.mcp
	c.mu.Lock()
	if s.d.Clock.Now().Sub(c.foundAt) < mcpFindEvery && c.foundAt.After(time.Time{}) {
		out := slices.Clone(c.found)
		c.mu.Unlock()
		return out
	}
	c.mu.Unlock()
	cfg := p.Cfg()
	var found []domain.MCPFound
	for _, name := range sortedKeys(cfg.MCP) {
		found = append(found, domain.MCPFound{Name: name, Source: domain.MCPFromBarnraise, Def: cfg.MCP[name]})
	}
	if s.d.MCP != nil {
		more, err := s.d.MCP.Find(ctx, cfg.Root)
		if err != nil {
			s.d.Log.Warn("MCP servers of Claude's configuration", "project", p.Name, "err", err)
		}
		for _, f := range more {
			if _, ok := cfg.MCP[f.Name]; !ok {
				found = append(found, f)
			}
		}
	}
	c.mu.Lock()
	c.found, c.foundAt = found, s.d.Clock.Now()
	c.mu.Unlock()
	return slices.Clone(found)
}

// mcpStatuses is what the heartbeat tells of this machine's servers.
func (s *Service) mcpStatuses(ctx context.Context, p *Project) []domain.MCPStatus {
	found := s.MCPFound(ctx, p)
	c := &p.mcp
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]domain.MCPStatus, 0, len(found))
	for _, f := range found {
		st := domain.StatusOf(f)
		if t, ok := c.tests[f.Name]; ok {
			st.OK, st.Tools, st.Error, st.TestedAt = t.OK, t.Tools, t.Error, t.TestedAt
		}
		out = append(out, st)
	}
	return out
}

// testMCPsInBackground tests every server now and then, one at a time, so
// the panel shows which answer without anybody pressing Test.
func (s *Service) testMCPsInBackground(p *Project) {
	c := &p.mcp
	c.mu.Lock()
	due := s.d.Clock.Now().Sub(c.testAt) >= mcpTestEvery
	c.mu.Unlock()
	if !due || s.d.MCP == nil || !c.testing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer c.testing.Store(false)
		ctx := context.Background()
		for _, f := range s.MCPFound(ctx, p) {
			_, _ = s.testFound(ctx, p, f)
		}
		c.mu.Lock()
		c.testAt = s.d.Clock.Now()
		c.mu.Unlock()
	}()
}

// TestMCP tests one server on this machine now: the MCP handshake and the
// list of its tools.
func (s *Service) TestMCP(ctx context.Context, project, name string) (domain.MCPStatus, bool, error) {
	p, err := s.Project(project)
	if err != nil {
		return domain.MCPStatus{}, false, err
	}
	for _, f := range s.MCPFound(ctx, p) {
		if f.Name == name {
			st, err := s.testFound(ctx, p, f)
			return st, true, err
		}
	}
	return domain.MCPStatus{Name: name, Error: "not on this machine: add it to Claude here (claude mcp add) or to the project's .mcp.json"}, false, nil
}

func (s *Service) testFound(ctx context.Context, p *Project, f domain.MCPFound) (domain.MCPStatus, error) {
	st := domain.StatusOf(f)
	st.TestedAt = s.d.Clock.Now().UTC()
	switch def, err := s.resolveMCP(f.Name, f.Def); {
	case s.d.MCP == nil:
		st.Error = "this daemon cannot test MCP servers"
	case err != nil:
		st.Error = err.Error()
	default:
		tools, terr := s.d.MCP.Tools(ctx, def)
		if terr != nil {
			st.Error = truncate(terr.Error(), 400)
		} else {
			st.OK, st.Tools = true, tools
		}
	}
	c := &p.mcp
	c.mu.Lock()
	if c.tests == nil {
		c.tests = map[string]domain.MCPStatus{}
	}
	c.tests[f.Name] = st
	c.mu.Unlock()
	return st, nil
}

// resolveMCP turns the secret references of a definition (keychain:…,
// env:…) into their values.
func (s *Service) resolveMCP(name string, d domain.MCPServerDef) (domain.MCPServerDef, error) {
	resolve := func(where string, m map[string]string) (map[string]string, error) {
		if len(m) == 0 {
			return m, nil
		}
		out := make(map[string]string, len(m))
		for k, v := range m {
			if s.d.ResolveSecret != nil && (strings.HasPrefix(v, "keychain:") || strings.HasPrefix(v, "env:")) {
				rv, err := s.d.ResolveSecret(v)
				if err != nil {
					return nil, fmt.Errorf("mcp.%s: %s %s: %w", name, where, k, err)
				}
				v = rv
			}
			out[k] = v
		}
		return out, nil
	}
	var err error
	if d.Env, err = resolve("env", d.Env); err != nil {
		return d, err
	}
	d.Headers, err = resolve("header", d.Headers)
	return d, err
}

// sessionMCP are the servers a session of role gets on this machine, its
// secrets resolved, and the granted ones this machine does not have.
func (s *Service) sessionMCP(ctx context.Context, p *Project, role string) (map[string]domain.MCPServerDef, []string, error) {
	found := s.MCPFound(ctx, p)
	snap := p.Snapshot()
	defs := domain.AgentMCPFor(p.Cfg(), snap.AgentMCP, found, role)
	out := make(map[string]domain.MCPServerDef, len(defs))
	for name, d := range defs {
		rd, err := s.resolveMCP(name, d)
		if err != nil {
			return nil, nil, err
		}
		out[name] = rd
	}
	var missing []string
	for name, g := range snap.AgentMCP {
		if _, ok := out[name]; !ok && g.For(role) {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return out, missing, nil
}

// AgentMCPHosts are the hosts the granted servers of every project reach,
// for the egress proxy.
func (s *Service) AgentMCPHosts() []string {
	now := s.d.Clock.Now()
	c := &s.mcpHosts
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && now.Sub(c.at) < 10*time.Second {
		return c.list
	}
	c.at, c.list = now, s.agentMCPHosts()
	return c.list
}

// mcpHostsCache keeps AgentMCPHosts for a few seconds: the egress proxy
// asks at every request.
type mcpHostsCache struct {
	mu   sync.Mutex
	at   time.Time
	list []string
}

func (s *Service) agentMCPHosts() []string {
	set := map[string]bool{}
	for _, name := range s.Projects() {
		p, err := s.Project(name)
		if err != nil {
			continue
		}
		for _, h := range domain.AgentMCPHosts(p.Snapshot().AgentMCP, s.MCPFound(context.Background(), p)) {
			set[h] = true
		}
	}
	return sortedKeys(set)
}

// SetAgentMCP lets agents use a server, for some roles, or no longer.
func (s *Service) SetAgentMCP(ctx context.Context, project string, a Actor, g domain.AgentMCPGrant) error {
	p, err := s.Project(project)
	if err != nil {
		return err
	}
	if err := a.Require(ScopeAdmin, p.Name); err != nil {
		return err
	}
	g.Name = strings.TrimSpace(g.Name)
	if g.Name == "" || g.Name == "barnraise" {
		return fmt.Errorf("%w: name the MCP server", ErrInvalid)
	}
	if _, ok := p.Cfg().MCP[g.Name]; ok {
		return fmt.Errorf("%w: %s is in barnraise.yaml (mcp:), which decides its roles", ErrInvalid, g.Name)
	}
	cfg := p.Cfg()
	for _, r := range g.Roles {
		if _, ok := cfg.Roles[r]; !ok {
			return fmt.Errorf("%w: role %q does not exist in %s", ErrInvalid, r, p.Name)
		}
	}
	var hosts []string
	for _, h := range g.Hosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" && !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	g.Hosts = hosts
	sort.Strings(g.Roles)
	g.By, g.At = actorName(a), s.d.Clock.Now().UTC()
	_, err = s.emit(ctx, p, domain.EvAgentMCP, g.Name, actorName(a), domain.Payload{AgentMCP: &g})
	return err
}

// AgentMCPMachine is one machine's view of one server.
type AgentMCPMachine struct {
	Machine string           `json:"machine"`
	Found   bool             `json:"found"`
	Status  domain.MCPStatus `json:"status"`
}

// AgentMCPServer is one server across the cluster, and what agents get.
type AgentMCPServer struct {
	Name     string            `json:"name"`
	Kind     string            `json:"kind"`
	Summary  string            `json:"summary,omitempty"`
	FromYAML bool              `json:"from_yaml,omitempty"`
	Allowed  bool              `json:"allowed"`
	Roles    []string          `json:"roles,omitempty"`
	Hosts    []string          `json:"hosts,omitempty"`
	Machines []AgentMCPMachine `json:"machines"`
}

// AgentMCPView lists every server any machine has, or that agents were
// given, with what each machine says of it.
func (s *Service) AgentMCPView(ctx context.Context, project string, a Actor) ([]string, []AgentMCPServer, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, nil, err
	}
	if err := a.Require(ScopeItemsRead, p.Name); err != nil {
		return nil, nil, err
	}
	snap := p.Snapshot()
	cfg := p.Cfg()
	reports := map[string][]domain.MCPStatus{}
	for name, m := range snap.Machines {
		if m.Left {
			continue
		}
		reports[name] = m.MCP
	}
	// This machine's own, fresh (its heartbeat may be up to 30s old).
	reports[s.d.Machine] = s.mcpStatuses(ctx, p)
	byName := map[string]*AgentMCPServer{}
	get := func(name string) *AgentMCPServer {
		if x := byName[name]; x != nil {
			return x
		}
		x := &AgentMCPServer{Name: name}
		byName[name] = x
		return x
	}
	// This machine's view of a server comes first, then the others' in order.
	order := sortedKeys(reports)
	order = append([]string{s.d.Machine}, slices.DeleteFunc(order, func(m string) bool { return m == s.d.Machine })...)
	for _, name := range order {
		for _, st := range reports[name] {
			x := get(st.Name)
			if x.Kind == "" || st.Source == domain.MCPFromBarnraise {
				x.Kind, x.Summary = st.Kind, st.Summary
			}
			if st.Source == domain.MCPFromBarnraise {
				x.FromYAML = true
			}
		}
	}
	for name, g := range snap.AgentMCP {
		x := get(name)
		x.Allowed, x.Roles, x.Hosts = g.Allowed, g.Roles, g.Hosts
	}
	for name, d := range cfg.MCP {
		x := get(name)
		x.FromYAML, x.Allowed, x.Roles = true, true, d.Roles
	}
	machines := sortedKeys(reports)
	out := make([]AgentMCPServer, 0, len(byName))
	for _, name := range sortedKeys(byName) {
		x := byName[name]
		for _, m := range machines {
			mm := AgentMCPMachine{Machine: m}
			for _, st := range reports[m] {
				if st.Name == name {
					mm.Found, mm.Status = true, st
				}
			}
			x.Machines = append(x.Machines, mm)
		}
		out = append(out, *x)
	}
	return sortedKeys(cfg.Roles), out, nil
}
