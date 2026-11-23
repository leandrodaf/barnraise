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

// A project that declares its own checks does not keep the template's guess,
// unless one of its flows still points at it.
func TestTemplateChecksGiveWayToTheProjectsOwn(t *testing.T) {
	own := "project: demo\ntemplate: blank\nchecks:\n  gate: make verify\nflows:\n  feature:\n    steps:\n      implement: { role: implementer }\n      check: { run: checks.gate }\n      integrate: { run: merge_queue }\n" +
		"  bug:\n    steps:\n      implement: { role: implementer }\n      check: { run: checks.gate }\n      integrate: { run: merge_queue }\n"
	cfg, err := Compile(t.TempDir(), []byte(own))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Checks["main"]; ok {
		t.Fatalf("the template's main check must go: %v", cfg.Checks)
	}
	if cfg.Checks["gate"].Run != "make verify" {
		t.Fatalf("checks %v", cfg.Checks)
	}
	// The bug flow of the template still names checks.main: it stays.
	partial := "project: demo\ntemplate: blank\nchecks:\n  extra: make extra\n"
	cfg, err = Compile(t.TempDir(), []byte(partial))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Checks["main"]; !ok {
		t.Fatal("a template check a flow still names is kept")
	}
	// No checks of its own: the template's stay.
	cfg, err = Compile(t.TempDir(), []byte("project: demo\ntemplate: blank\n"))
	if err != nil || cfg.Checks["main"].Run == "" {
		t.Fatalf("template checks: %v %v", cfg.Checks, err)
	}
}
