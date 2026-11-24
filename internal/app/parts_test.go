package app

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// The Parts screen comes from here: each part with its folder, its own flow
// per kind (the project's for the kinds it leaves out), the checks that run
// for it, its own barnraise.yaml for the owner, and its work counted by the
// same columns as the board.
func TestPartsOfAProject(t *testing.T) {
	e := newEnv(t, withCfg(func(c *domain.ProjectConfig) {
		c.Areas = map[string]domain.AreaDef{
			"mobile": {Name: "mobile", Dir: "mobile", About: "The app", Stack: "react-native", Paths: []string{"mobile/**"}, KindFlows: map[string]string{"feature": "reviewed"}},
			"cards":  {Name: "cards", Paths: []string{"app/cards/**"}},
		}
		c.KindFlows = map[string]string{"bug": "simple"}
		c.Checks["e2e"] = domain.CheckDef{Name: "e2e", Run: "true", RunsOn: "local", Areas: []string{"mobile"}}
	}))
	if err := os.MkdirAll(filepath.Join(e.cfg.Root, "mobile"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.cfg.Root, "mobile", "barnraise.yaml"), []byte("about: The app\nstack: react-native\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.ready("simple", "Login screen", "mobile")
	e.ready("simple", "Cards list", "cards")

	v, err := e.svc.Parts(e.ctx, "demo", owner())
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Parts) != 2 || v.Parts[0].Name != "mobile" {
		t.Fatalf("parts %+v: the part with its own folder comes first", v.Parts)
	}
	m := v.Parts[0]
	if m.Flows["feature"] != "reviewed" || m.Flows["bug"] != "simple" || len(m.FlowSteps["reviewed"].Steps) != 3 {
		t.Fatalf("flows %+v steps %+v", m.Flows, m.FlowSteps)
	}
	if !slices.Contains(m.Checks, "e2e") || !slices.Contains(m.Checks, "main") || slices.Contains(v.Parts[1].Checks, "e2e") {
		t.Fatalf("checks: mobile %v, cards %v", m.Checks, v.Parts[1].Checks)
	}
	if m.ConfigFile != "mobile/barnraise.yaml" || m.Config != "about: The app\nstack: react-native\n" {
		t.Fatalf("config %q %q", m.ConfigFile, m.Config)
	}
	if m.Open != 1 || m.Columns[ColBacklog] != 1 || v.Parts[1].Open != 1 {
		t.Fatalf("work: mobile %+v, cards %+v", m.Columns, v.Parts[1].Columns)
	}
	// A reader who may not administer the project sees the parts, not the files.
	reader := Actor{Kind: ActorToken, Name: "viewer", Scopes: []string{string(ScopeItemsRead)}}
	if rv, err := e.svc.Parts(e.ctx, "demo", reader); err != nil || rv.Parts[0].Config != "" || rv.Config != "" {
		t.Fatalf("a reader got the configuration: %v %+v", err, rv)
	}
}
