package domain

import (
	"slices"
	"testing"
)

func TestAgentMCPFor(t *testing.T) {
	cfg := &ProjectConfig{MCP: map[string]MCPServerDef{"docs": {URL: "https://docs.example/mcp", Roles: []string{"implementer"}}, "figma": {Command: "yaml-figma"}}}
	found := []MCPFound{
		{Name: "figma", Source: MCPFromProject, Def: MCPServerDef{Command: "claude-figma"}},
		{Name: "github", Source: MCPFromUser, Def: MCPServerDef{URL: "https://api.githubcopilot.com/mcp/"}},
		{Name: "db", Source: MCPFromLocal, Def: MCPServerDef{Command: "pg"}},
		{Name: "docs", Source: MCPFromBarnraise, Def: cfg.MCP["docs"]},
	}
	grants := map[string]*AgentMCPGrant{
		"github": {Name: "github", Allowed: true},
		"db":     {Name: "db", Allowed: true, Roles: []string{"reviewer"}},
		"figma":  {Name: "figma", Allowed: true},
	}
	impl := AgentMCPFor(cfg, grants, found, "implementer")
	if impl["figma"].Command != "yaml-figma" || impl["github"].URL == "" || impl["docs"].URL == "" {
		t.Fatalf("implementer %v", impl)
	}
	if _, ok := impl["db"]; ok {
		t.Fatal("db is the reviewer's")
	}
	rev := AgentMCPFor(cfg, grants, found, "reviewer")
	if _, ok := rev["docs"]; ok || rev["db"].Command != "pg" {
		t.Fatalf("reviewer %v", rev)
	}
	if got := AgentMCPFor(nil, nil, found, "implementer"); len(got) != 0 {
		t.Fatalf("nothing allowed: %v", got)
	}
}

func TestAgentMCPHostsAndApply(t *testing.T) {
	s := NewState("p")
	s.applyAgentMCP(&AgentMCPGrant{Name: "github", Allowed: true})
	s.applyAgentMCP(&AgentMCPGrant{Name: "db", Allowed: true, Hosts: []string{"DB.example.com"}})
	s.applyAgentMCP(&AgentMCPGrant{Name: "off", Allowed: false, Roles: []string{"reviewer"}})
	found := []MCPFound{
		{Name: "github", Def: MCPServerDef{URL: "https://API.githubcopilot.com/mcp/"}},
		{Name: "off", Def: MCPServerDef{URL: "https://off.example/mcp"}},
	}
	if got := AgentMCPHosts(s.AgentMCP, found); !slices.Equal(got, []string{"api.githubcopilot.com", "db.example.com"}) {
		t.Fatalf("hosts %v", got)
	}
	s.applyAgentMCP(&AgentMCPGrant{Name: "db"})
	if _, ok := s.AgentMCP["db"]; ok {
		t.Fatal("a grant with nothing left is forgotten")
	}
	if _, ok := s.AgentMCP["off"]; !ok {
		t.Fatal("a grant switched off keeps its roles for the next time")
	}
	if (&AgentMCPGrant{Name: "off", Roles: []string{"reviewer"}}).For("reviewer") {
		t.Fatal("not allowed is not allowed for any role")
	}
}

func TestURLHostPath(t *testing.T) {
	for raw, want := range map[string][2]string{
		"https://api.githubcopilot.com/mcp/":       {"api.githubcopilot.com", "/mcp/"},
		"https://user:pw@mcp.example:8443/x?key=1": {"mcp.example:8443", "/x"},
		"http://localhost:3000":                    {"localhost:3000", ""},
	} {
		h, p := urlHostPath(raw)
		if h != want[0] || p != want[1] {
			t.Errorf("%s: %q %q", raw, h, p)
		}
	}
	if st := StatusOf(MCPFound{Name: "x", Def: MCPServerDef{URL: "https://mcp.example/s?token=secret"}}); st.Summary != "mcp.example/s" {
		t.Errorf("summary %q", st.Summary)
	}
}
