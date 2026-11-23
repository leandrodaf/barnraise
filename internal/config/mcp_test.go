package config

import (
	"strings"
	"testing"
)

func TestMCPServersFromYAML(t *testing.T) {
	yml := `project: demo
template: blank
mcp:
  figma:
    command: figma-mcp
    args: [--stdio]
    env: { FIGMA_TOKEN: "keychain:mcp/figma" }
    roles: [implementer]
  docs:
    url: https://docs.example/mcp
`
	cfg, err := Compile(t.TempDir(), []byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	if errs := cfg.Validate(); len(errs) != 0 {
		t.Fatal(errs)
	}
	if f := cfg.MCP["figma"]; f.Command != "figma-mcp" || f.Env["FIGMA_TOKEN"] != "keychain:mcp/figma" || len(f.Roles) != 1 {
		t.Fatalf("figma %+v", f)
	}
	if len(cfg.MCPFor("reviewer")) != 1 || len(cfg.MCPFor("implementer")) != 2 {
		t.Fatalf("per role: %v", cfg.MCP)
	}
	bad, err := Compile(t.TempDir(), []byte("project: demo\ntemplate: blank\nmcp:\n  x: { command: a, roles: [ghost] }\n"))
	if err == nil {
		errs := bad.Validate()
		if len(errs) == 0 || !strings.Contains(errs[0].Error(), "ghost") {
			t.Fatalf("a role that does not exist must be reported: %v", errs)
		}
	} else if !strings.Contains(err.Error(), "ghost") {
		t.Fatal(err)
	}
}
