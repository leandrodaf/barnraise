package config

import (
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func TestSandboxKeys(t *testing.T) {
	yml := `template: blank
project: x
policy:
  deny_commands: ['\bterraform\b']
worktree:
  setup: npm ci
  sandbox: build
sandbox:
  read: [/opt/sdk]
  write: [~/.cache/ms-playwright]
  ports: [5432]
  deny_commands: ['\bkubectl\b']
checks:
  unit: { run: go test ./..., sandbox: check }
  e2e: { run: npm run e2e, sandbox: open }
  plain: make lint
`
	cfg, err := Compile(t.TempDir(), []byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	sb := cfg.Sandbox
	if sb.SetupSandbox() != domain.SandboxBuild || sb.Read[0] != "/opt/sdk" || sb.Write[0] != "~/.cache/ms-playwright" || sb.Ports[0] != 5432 {
		t.Fatalf("%+v", sb)
	}
	if len(sb.DenyCommands) != 2 || sb.DenyCommands[0] != `\bterraform\b` || sb.DenyCommands[1] != `\bkubectl\b` {
		t.Fatalf("policy.deny_commands joins sandbox.deny_commands: %v", sb.DenyCommands)
	}
	if cfg.Checks["unit"].Sandbox != "check" || cfg.Checks["e2e"].Sandbox != "open" || domain.CheckSandbox(cfg.Checks["plain"]) != domain.SandboxCheck {
		t.Fatalf("check profiles: %+v", cfg.Checks)
	}
	if errs := cfg.Validate(); len(errs) != 0 {
		t.Fatal(errs)
	}
	out, err := Explain(cfg)
	if err != nil || !strings.Contains(out, "sandbox: build") {
		t.Fatalf("explain shows the setup profile: %v\n%s", err, out)
	}

	bad, err := Compile(t.TempDir(), []byte("template: blank\nproject: x\nchecks:\n  unit: { run: x, sandbox: jail }\nworktree: { sandbox: nope }\n"))
	if err != nil {
		t.Fatal(err)
	}
	var msgs []string
	for _, e := range bad.Validate() {
		msgs = append(msgs, e.Error())
	}
	all := strings.Join(msgs, "\n")
	if !strings.Contains(all, "checks.unit.sandbox") || !strings.Contains(all, "worktree.sandbox") {
		t.Fatalf("unknown profiles are reported: %s", all)
	}
	// Without any sandbox key the defaults hold.
	plain, _ := Compile(t.TempDir(), []byte("template: blank\nproject: x\n"))
	if plain.Sandbox.SetupSandbox() != domain.SandboxSetup || len(plain.Sandbox.Ports) != 0 {
		t.Fatalf("defaults: %+v", plain.Sandbox)
	}
}
