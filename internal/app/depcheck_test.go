package app

import (
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

func withDependencyCheck(c *domain.ProjectConfig) { c.AddDependencyCheck() }

// The built-in dependencies check rides with the last check step: it runs
// when the change touches a manifest, gets the commit the branch forked
// from, and a new vulnerability sends the item back with the finding.
func TestDependencyCheckRunsBeforeIntegration(t *testing.T) {
	e := newEnv(t, withGit(), withCfg(withDependencyCheck))
	e.checks.Script(domain.DependencyCheck, testkit.CheckOutcome{Passed: false, Summary: "1 known vulnerability at or above high: go.mod: golang.org/x/net 0.7.0: GHSA-x"})
	it := e.ready("simple", "Add the HTTP client", "x")
	e.start(it.ID)
	e.workspace(it.ID, map[string]string{"go.mod": "module shop\n\nrequire golang.org/x/net v0.7.0\n"})
	e.mustDeliver(it.ID, domain.Delivery{})
	e.drive(it.ID)
	if got := ranChecks(e); !strings.Contains(got, domain.DependencyCheck) {
		t.Fatalf("ran %q", got)
	}
	for _, s := range e.checks.Specs() {
		if s.Def.Name == domain.DependencyCheck && (s.Base == "" || s.Def.RunsOn != domain.RunsOnDependencies) {
			t.Fatalf("spec %+v", s)
		}
	}
	got := e.wantStatus(it.ID, domain.StatusInProgress, "implement")
	if len(got.Rework) != 1 || !strings.Contains(got.Rework[0].Reason, "check dependencies failed: 1 known vulnerability") {
		t.Fatalf("rework %+v", got.Rework)
	}
	// The step's end names the step and its outcome for hooks.
	found := false
	for _, ev := range e.store.Events() {
		if ev.Type != domain.EvItemStepFinished || ev.Subject != it.ID {
			continue
		}
		pl, _ := ev.DecodePayload()
		if pl.Extra["step"] == "check" && pl.Extra["outcome"] == string(domain.OutcomeFail) {
			found = true
		}
	}
	if !found {
		t.Fatal("item.step.finished does not say which step ended and how")
	}
}

func TestDependencyCheckIsSkippedWithoutManifests(t *testing.T) {
	e := newEnv(t, withGit(), withCfg(func(c *domain.ProjectConfig) {
		c.AddDependencyCheck()
		c.Areas = map[string]domain.AreaDef{"app": {Name: "app", Paths: []string{"src/**"}}}
	}))
	it := e.ready("simple", "Fix a label", "x")
	e.start(it.ID)
	e.workspace(it.ID, map[string]string{"src/label.go": "package src\n"})
	e.mustDeliver(it.ID, domain.Delivery{})
	e.drive(it.ID)
	if got := ranChecks(e); strings.Contains(got, domain.DependencyCheck) {
		t.Fatalf("ran %q", got)
	}
	e.wantStatus(it.ID, domain.StatusQueued, "integrate")
}

func TestWithStepOutcomeKeepsWhatIsThere(t *testing.T) {
	pl := withStepOutcome(domain.Payload{Extra: map[string]any{"step": "x"}}, "check", "")
	if pl.Extra["step"] != "x" || pl.Extra["outcome"] != nil {
		t.Fatalf("%+v", pl.Extra)
	}
}
