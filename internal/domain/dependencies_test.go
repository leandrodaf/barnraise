package domain

import (
	"reflect"
	"strings"
	"testing"
)

func TestSeverities(t *testing.T) {
	for s, want := range map[string]int{"LOW": 1, "moderate": 2, "Medium": 2, " high ": 3, "critical": 4, "": 0, "unknown": 0} {
		if got := SeverityRank(s); got != want {
			t.Errorf("SeverityRank(%q) = %d, want %d", s, got, want)
		}
	}
	for score, want := range map[float64]string{9.8: "critical", 9: "critical", 7.5: "high", 7: "high", 6.9: "medium", 4: "medium", 3.9: "low", 0.1: "low", 0: ""} {
		if got := SeverityOfScore(score); got != want {
			t.Errorf("SeverityOfScore(%v) = %q, want %q", score, got, want)
		}
	}
}

func TestDependencyPolicy(t *testing.T) {
	var p DependencyPolicy // defaults: fail at high, unknown counts as high
	if !p.Fails("high") || !p.Fails("critical") || p.Fails("medium") || !p.Fails("") {
		t.Fatal("default threshold")
	}
	p = DependencyPolicy{FailAt: "critical", Unknown: "low"}
	if p.Fails("high") || !p.Fails("critical") || p.Fails("") {
		t.Fatal("critical threshold, unknown as low")
	}
	p.Ignore = []string{" ghsa-aaaa-bbbb-cccc "}
	if !p.Ignored("CVE-1", "GHSA-aaaa-bbbb-cccc") || p.Ignored("CVE-1") {
		t.Fatal("ignore matches any id or alias, case-insensitively")
	}
	if errs := (DependencyPolicy{FailAt: "severe", Unknown: "meh", Scanner: "trivy"}).Validate(); len(errs) != 3 {
		t.Fatalf("errors = %v", errs)
	}
	if errs := (DependencyPolicy{FailAt: "medium", Scanner: "api"}).Validate(); len(errs) != 0 {
		t.Fatal(errs)
	}
}

func depConfig() *ProjectConfig {
	c := &ProjectConfig{Name: "shop", Approves: "owner", Capacity: Capacity{Goal: 80, Bugs: 15, Improvements: 5},
		Tracker: TrackerDef{Type: "local"},
		Areas:   map[string]AreaDef{"A-api": {Name: "A-api", Paths: []string{"api/**"}}},
		Checks: map[string]CheckDef{
			"lint": {Name: "lint", Run: "make lint", RunsOn: "local", Areas: []string{"A-api"}},
			"unit": {Name: "unit", Run: "make test", RunsOn: "local", Areas: []string{"A-api"}},
		},
		Flows: map[string]*Flow{
			"feature": {Name: "feature", Steps: []StepDef{
				{Name: "implement", Type: StepAgent, Role: "implementer"},
				{Name: "lint", Type: StepCheck, Run: "checks.lint"},
				{Name: "check", Type: StepCheck, Run: "checks.unit"},
				{Name: "integrate", Type: StepMergeQueue, Run: "merge_queue"},
			}},
		},
	}
	c.AddDependencyCheck()
	return c
}

func TestDependencyCheckRidesWithTheLastCheckStep(t *testing.T) {
	c := depConfig()
	d, ok := c.Checks[DependencyCheck]
	if !ok || d.RunsOn != RunsOnDependencies || len(d.Paths) != len(DependencyManifests) {
		t.Fatalf("the check is added: %+v", d)
	}
	f := c.Flows["feature"]
	if got := c.AutoChecks(f.Steps[1], []string{"lint"}); !reflect.DeepEqual(got, []string{"lint"}) {
		t.Fatalf("not the last check step: %v", got)
	}
	if got := c.AutoChecks(f.Steps[2], []string{"unit"}); !reflect.DeepEqual(got, []string{"unit", DependencyCheck}) {
		t.Fatalf("last check step: %v", got)
	}
	if got := c.AutoChecks(f.Steps[2], []string{"unit", DependencyCheck}); len(got) != 2 {
		t.Fatalf("named already: %v", got)
	}
	if got := c.AutoChecks(f.Steps[3], nil); got != nil {
		t.Fatalf("not a check step: %v", got)
	}
	for _, e := range c.Validate() {
		if strings.Contains(e.Error(), DependencyCheck) {
			t.Fatalf("the built-in check is valid: %v", e)
		}
	}

	// The project's own check of that name wins, and `dependencies: off` removes it.
	own := depConfig()
	own.Checks[DependencyCheck] = CheckDef{Name: DependencyCheck, Run: "osv-scanner -r .", RunsOn: "local"}
	own.AddDependencyCheck()
	if own.Checks[DependencyCheck].RunsOn != "local" || !reflect.DeepEqual(own.AutoChecks(own.Flows["feature"].Steps[2], []string{"unit"}), []string{"unit"}) {
		t.Fatal("the project's own dependencies check is not replaced nor added to steps")
	}
	off := &ProjectConfig{Dependencies: DependencyPolicy{Off: true}}
	off.AddDependencyCheck()
	if len(off.Checks) != 0 {
		t.Fatal("off")
	}
	empty := &ProjectConfig{}
	empty.AddDependencyCheck()
	if _, ok := empty.Checks[DependencyCheck]; !ok {
		t.Fatal("added to a config without checks")
	}
}

func TestDependencyCheckSelection(t *testing.T) {
	c := depConfig()
	names := []string{DependencyCheck, "lint", "unit"}
	run := func(touched ...string) map[string]bool {
		out := map[string]bool{}
		for _, d := range c.SelectChecks(names, touched, nil) {
			out[d.Name] = d.Run
		}
		return out
	}
	if got := run("api/user.go"); got[DependencyCheck] || !got["unit"] {
		t.Fatalf("source change: %v", got)
	}
	// go.mod belongs to no area: every check runs, as before the built-in
	// check existed (its paths do not claim the file).
	if got := run("go.mod"); !got[DependencyCheck] || !got["unit"] || !got["lint"] {
		t.Fatalf("manifest outside areas: %v", got)
	}
	c.Areas["A-api"] = AreaDef{Name: "A-api", Paths: []string{"api/**"}}
	if got := run("api/go.mod"); !got[DependencyCheck] || !got["unit"] {
		t.Fatalf("manifest in an area: %v", got)
	}
	if !IsDependencyManifest("web/requirements-dev.txt") || !IsDependencyManifest("go.sum") || IsDependencyManifest("main.go") {
		t.Fatal("manifest globs")
	}
}

func TestBuiltinRunsOnIsValid(t *testing.T) {
	c := depConfig()
	c.Checks["structure"] = CheckDef{Name: "structure", RunsOn: RunsOnASTGrep}
	c.Checks["odd"] = CheckDef{Name: "odd", RunsOn: "builtin:trivy"}
	c.Dependencies.FailAt = "extreme"
	var msgs []string
	for _, e := range c.Validate() {
		msgs = append(msgs, e.Error())
	}
	all := strings.Join(msgs, "\n")
	if strings.Contains(all, "structure") || !strings.Contains(all, `check odd: runs_on "builtin:trivy" must be`) || !strings.Contains(all, "dependencies.fail_at") {
		t.Fatalf("%s", all)
	}
}
