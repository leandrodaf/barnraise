package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/templates"
)

func TestDocExampleCompiles(t *testing.T) {
	root := t.TempDir()
	yml := `template: mobile-android
language: pt-BR
goal: Personal finance app, Android first, matching docs/prototipo-mobile
approves: owner
tracker:
  type: github
  repo: leandrodaf/financas-pluggy
  project: 3
checks:
  main: scripts/verificar.py
`
	cfg, err := Compile(root, []byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	if errs := cfg.Validate(); len(errs) > 0 {
		t.Fatal(errs)
	}
	if cfg.Language != "pt-BR" || cfg.Tracker.Project != "3" || cfg.CodeHost.Type != "github" || cfg.CodeHost.Repo != "leandrodaf/financas-pluggy" {
		t.Fatalf("%+v %+v", cfg.Tracker, cfg.CodeHost)
	}
	if cfg.Checks["main"].Run != "scripts/verificar.py" || cfg.Checks["main"].RunsOn != "local" {
		t.Fatalf("%+v", cfg.Checks["main"])
	}
	if cfg.Checks["maestro"].RunsOn != "machine:emulators" {
		t.Fatal("template check kept")
	}
	f, err := cfg.FlowFor("feature")
	if err != nil || f.Name != "feature-mobile" {
		t.Fatal(f, err)
	}
	names := []string{}
	for _, s := range f.Steps {
		names = append(names, s.Name)
	}
	want := "plan,plan-approval,implement,quick-look,check,maestro,design,analyst,integrate"
	if strings.Join(names, ",") != want {
		t.Fatalf("steps %v", names)
	}
	if s, _ := f.Step("plan-approval"); s.Type != domain.StepApproval || s.Approver != "analyst" {
		t.Fatalf("%+v", s)
	}
	if f.Rework["plan-approval.reject"] != "plan" {
		t.Fatal("approval rework")
	}
	if s, _ := f.Step("design"); !s.Review || s.DifferentFrom() != "implement" {
		t.Fatalf("%+v", s)
	}
	if s, _ := f.Step("plan"); s.Review {
		t.Fatal("plan is not a review")
	}
	if cfg.Roles["designer"].Needs[0] != "emulators" {
		t.Fatal("needs")
	}
	if cfg.Capacity.Goal != 80 || cfg.Proposals.PerAgentPerDay != 3 || cfg.Lease.TTL.Minutes() != 2 {
		t.Fatal("defaults")
	}
}

func TestAllTemplatesValidate(t *testing.T) {
	for _, name := range templates.Names() {
		cfg, err := Compile(t.TempDir(), []byte("template: "+name+"\nproject: x\n"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if errs := cfg.Validate(); len(errs) > 0 {
			t.Fatalf("%s: %v", name, errs)
		}
		for role := range cfg.Roles {
			if _, err := RolePrompt(cfg, role); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
		if templates.Description(name) == "" {
			t.Fatalf("%s has no description", name)
		}
		out, err := Explain(cfg)
		if err != nil || !strings.Contains(out, "flows:") {
			t.Fatal(err)
		}
	}
}

func TestCapacityAndErrors(t *testing.T) {
	cfg, err := Compile(t.TempDir(), []byte("capacity: { goal: 70%, bugs: 20%, improvements: 10% }\nself_improvement: { budget: 2/week }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Capacity.Goal != 70 || cfg.Capacity.SelfPerWeek != 2 {
		t.Fatal(cfg.Capacity)
	}
	bad := []string{
		"capacity: { goal: 70x }",
		"capacity: { other: 10% }",
		"proposals: { expire: soon }",
		"template: nope",
		"items: { target_size: big }",
		"flows: { f: { steps: { a: {} } } }",
		"models: { classes: { normal: [opus] } }",
		": : :",
	}
	for _, b := range bad {
		if _, err := Compile(t.TempDir(), []byte(b)); err == nil {
			t.Errorf("should fail: %s", b)
		}
	}
	cfg, _ = Compile(t.TempDir(), []byte("capacity: { goal: 70%, bugs: 10%, improvements: 5% }\napproves: boss\ntracker: { type: trello }\n"))
	if errs := cfg.Validate(); len(errs) != 3 {
		t.Fatalf("%v", errs)
	}
}

func TestFlowOverrideReplacesWhole(t *testing.T) {
	yml := `template: blank
flows:
  feature:
    steps:
      implement: { role: implementer }
      integrate: { run: merge_queue }
`
	cfg, err := Compile(t.TempDir(), []byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(cfg.Flows["feature"].Steps); n != 2 {
		t.Fatalf("flow must be replaced, got %d steps", n)
	}
	if _, ok := cfg.Flows["bug"]; !ok {
		t.Fatal("other template flows stay")
	}
}

func TestAreasFileAndLoad(t *testing.T) {
	root := t.TempDir()
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "barnraise init") {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(root, Dir), 0o755)
	_ = os.WriteFile(filepath.Join(root, Dir, "areas.yaml"), []byte("cards:\n  paths: [app/cards/**]\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, FileName), []byte("project: demo\nareas:\n  A-home: { paths: [app/home/**] }\n"), 0o644)
	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AreaOfFile("app/cards/X.kt") != "cards" || cfg.AreaOfFile("app/home/Y.kt") != "home" {
		t.Fatal(cfg.Areas)
	}
	problems, err := Check(root)
	if err != nil || len(problems) != 0 {
		t.Fatal(problems, err)
	}
	mc, err := ParseModelChoice("claude/opus@high")
	if err != nil || mc.Effort != "high" {
		t.Fatal(mc)
	}
}
