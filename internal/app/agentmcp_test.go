package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// fakeCatalog is a machine's MCP servers in Claude's configuration.
type fakeCatalog struct {
	mu     sync.Mutex
	found  []domain.MCPFound
	tools  map[string][]string // by command or URL
	fail   map[string]string
	tested []domain.MCPServerDef
}

func (c *fakeCatalog) Find(context.Context, string) ([]domain.MCPFound, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.found), nil
}

func (c *fakeCatalog) Tools(_ context.Context, def domain.MCPServerDef) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tested = append(c.tested, def)
	key := def.Command + def.URL
	if why := c.fail[key]; why != "" {
		return nil, errors.New(why)
	}
	return c.tools[key], nil
}

func machineCatalog() *fakeCatalog {
	return &fakeCatalog{
		found: []domain.MCPFound{
			{Name: "figma", Source: domain.MCPFromProject, Def: domain.MCPServerDef{Command: "figma-mcp", Env: map[string]string{"FIGMA_TOKEN": "tok"}}},
			{Name: "github", Source: domain.MCPFromUser, Def: domain.MCPServerDef{URL: "https://api.githubcopilot.com/mcp/", Type: "http", Headers: map[string]string{"Authorization": "keychain:gh"}}},
			{Name: "mail", Source: domain.MCPFromUser, Def: domain.MCPServerDef{Command: "mail-mcp"}},
		},
		tools: map[string][]string{"figma-mcp": {"get_file"}, "https://api.githubcopilot.com/mcp/": {"get_issue", "create_issue"}},
		fail:  map[string]string{"mail-mcp": "mail-mcp is not installed on this machine"},
	}
}

func sessionServers(t *testing.T, path string) map[string]map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Servers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Servers
}

// The owner's servers stay out of a session until the panel lets agents
// use them, and then only for the roles chosen; the session carries the
// machine's own definition, secrets resolved, headers included.
func TestAgentsGetOnlyTheMCPServersTheOwnerAllowed(t *testing.T) {
	e := newEnv(t)
	cat := machineCatalog()
	e.svc.d.MCP = cat
	e.svc.d.ResolveSecret = func(ref string) (string, error) {
		if ref == "keychain:gh" {
			return "Bearer ghp", nil
		}
		return "", errors.New("unknown " + ref)
	}
	e.account("fake-1")

	e.ready("simple", "before any grant", "x")
	release := e.holdSessions()
	e.fill()
	if got := sessionServers(t, e.prov.Specs()[0].MCPConfig); len(got) != 1 || got["barnraise"] == nil {
		t.Fatalf("without a grant an agent gets Barnraise's server only: %v", got)
	}
	e.svc.Runtime().MaxSessions = -1
	release()
	e.idle()

	if err := e.svc.SetAgentMCP(e.ctx, "demo", owner(), domain.AgentMCPGrant{Name: "github", Allowed: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetAgentMCP(e.ctx, "demo", owner(), domain.AgentMCPGrant{Name: "figma", Allowed: true, Roles: []string{"reviewer"}}); err != nil {
		t.Fatal(err)
	}
	e.svc.Runtime().MaxSessions = 0
	e.ready("simple", "after the grants", "y")
	release = e.holdSessions()
	e.fill()
	specs := e.prov.Specs()
	got := sessionServers(t, specs[len(specs)-1].MCPConfig)
	if got["github"] == nil || got["figma"] != nil || got["mail"] != nil {
		t.Fatalf("the implementer gets github (every role) but not figma (reviewer) nor mail (not allowed): %v", got)
	}
	gh := got["github"]
	if gh["url"] != "https://api.githubcopilot.com/mcp/" || gh["type"] != "http" || gh["headers"].(map[string]any)["Authorization"] != "Bearer ghp" {
		t.Fatalf("github %v", gh)
	}
	e.svc.Runtime().MaxSessions = -1
	release()
	e.idle()
}

// barnraise.yaml's servers are always given to their roles, and the panel
// cannot change them there.
func TestAgentMCPYAMLWinsAndIsNotGranted(t *testing.T) {
	e := newEnv(t, withCfg(func(c *domain.ProjectConfig) {
		c.MCP = map[string]domain.MCPServerDef{"figma": {Command: "yaml-figma"}}
	}))
	e.svc.d.MCP = machineCatalog()
	err := e.svc.SetAgentMCP(e.ctx, "demo", owner(), domain.AgentMCPGrant{Name: "figma", Allowed: true})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	if err := e.svc.SetAgentMCP(e.ctx, "demo", owner(), domain.AgentMCPGrant{Name: "github", Allowed: true, Roles: []string{"nobody"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown role: %v", err)
	}
	if err := e.svc.SetAgentMCP(e.ctx, "demo", Actor{}, domain.AgentMCPGrant{Name: "github", Allowed: true}); err == nil {
		t.Fatal("only the owner lets agents use a server")
	}
	defs, missing, err := e.svc.sessionMCP(e.ctx, e.p, "implementer")
	if err != nil || defs["figma"].Command != "yaml-figma" || len(missing) != 0 {
		t.Fatalf("%v %v %v", defs, missing, err)
	}
}

// A server granted but absent from this machine leaves the session without
// it and is named; the panel shows, per machine, who has it and if it works.
func TestAgentMCPViewAcrossMachines(t *testing.T) {
	e := newEnv(t)
	cat := machineCatalog()
	e.svc.d.MCP = cat
	if err := e.svc.SetAgentMCP(e.ctx, "demo", owner(), domain.AgentMCPGrant{Name: "postgres", Allowed: true, Hosts: []string{" DB.Example.com "}}); err != nil {
		t.Fatal(err)
	}
	if _, missing, _ := e.svc.sessionMCP(e.ctx, e.p, "implementer"); !slices.Equal(missing, []string{"postgres"}) {
		t.Fatalf("missing %v", missing)
	}
	st, found, err := e.svc.TestMCP(e.ctx, "demo", "figma")
	if err != nil || !found || !st.OK || !slices.Equal(st.Tools, []string{"get_file"}) {
		t.Fatalf("figma %+v %v %v", st, found, err)
	}
	if st, _, _ := e.svc.TestMCP(e.ctx, "demo", "mail"); st.OK || !strings.Contains(st.Error, "not installed") {
		t.Fatalf("mail %+v", st)
	}
	if st, found, _ := e.svc.TestMCP(e.ctx, "demo", "postgres"); found || !strings.Contains(st.Error, "not on this machine") {
		t.Fatalf("postgres %+v", st)
	}
	// Another machine has postgres and not figma.
	e.p.mu.Lock()
	e.p.state.Machines["dma"] = &domain.Machine{Name: "dma", LastSeen: e.clock.Now(), MCP: []domain.MCPStatus{{Name: "postgres", Source: domain.MCPFromLocal, Kind: "local", OK: true, Tools: []string{"query"}}}}
	e.p.mu.Unlock()
	roles, servers, err := e.svc.AgentMCPView(e.ctx, "demo", owner())
	if err != nil || len(roles) == 0 {
		t.Fatal(roles, err)
	}
	by := map[string]AgentMCPServer{}
	for _, s := range servers {
		by[s.Name] = s
	}
	pg, fig := by["postgres"], by["figma"]
	if !pg.Allowed || !slices.Equal(pg.Hosts, []string{"db.example.com"}) || fig.Allowed || fig.Summary != "figma-mcp" {
		t.Fatalf("postgres %+v figma %+v", pg, fig)
	}
	where := func(s AgentMCPServer) map[string]bool {
		out := map[string]bool{}
		for _, m := range s.Machines {
			out[m.Machine] = m.Found
		}
		return out
	}
	if w := where(pg); !w["dma"] || w[e.svc.d.Machine] {
		t.Fatalf("postgres is on dma only: %v", w)
	}
	if w := where(fig); w["dma"] || !w[e.svc.d.Machine] {
		t.Fatalf("figma is here only: %v", w)
	}
	for _, m := range fig.Machines {
		if m.Machine == e.svc.d.Machine && (!m.Status.OK || len(m.Status.Tools) != 1) {
			t.Fatalf("the test result shows: %+v", m)
		}
	}
	if hosts := e.svc.AgentMCPHosts(); !slices.Equal(hosts, []string{"db.example.com"}) {
		t.Fatalf("hosts %v", hosts)
	}
	// Taking the grant back closes the hosts (after the short cache).
	if err := e.svc.SetAgentMCP(e.ctx, "demo", owner(), domain.AgentMCPGrant{Name: "postgres"}); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Minute)
	if hosts := e.svc.AgentMCPHosts(); len(hosts) != 0 {
		t.Fatalf("hosts %v", hosts)
	}
	if _, ok := e.p.Snapshot().AgentMCP["postgres"]; ok {
		t.Fatal("a grant taken back is forgotten")
	}
}

// The heartbeat says what the machine has, never the secrets, and the
// servers are tested in the background without anybody pressing Test.
func TestHeartbeatCarriesMCPStatusesWithoutSecrets(t *testing.T) {
	e := newEnv(t)
	cat := machineCatalog()
	e.svc.d.MCP = cat
	sts := e.svc.mcpStatuses(e.ctx, e.p)
	if len(sts) != 3 {
		t.Fatalf("statuses %+v", sts)
	}
	b, _ := json.Marshal(sts)
	if strings.Contains(string(b), "tok") || strings.Contains(string(b), "keychain:gh") {
		t.Fatalf("a secret in the heartbeat: %s", b)
	}
	e.svc.testMCPsInBackground(e.p)
	eventually(t, "every server tested", func() bool {
		cat.mu.Lock()
		defer cat.mu.Unlock()
		return len(cat.tested) == 3
	})
	eventually(t, "results kept", func() bool {
		for _, st := range e.svc.mcpStatuses(e.ctx, e.p) {
			if st.TestedAt.IsZero() {
				return false
			}
		}
		return true
	})
}
