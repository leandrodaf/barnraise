package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestRouteSaysWhenOnlyTheReviewedModelIsLeft(t *testing.T) {
	r := ModelRouter{
		Classes:  map[string][]ModelChoice{"normal": {{Provider: "claude", Model: "haiku"}}, "two": {{Provider: "claude", Model: "haiku"}, {Provider: "claude", Model: "sonnet"}}},
		Families: map[string]string{"claude": "anthropic"},
	}
	_, err := r.Route(RouteRequest{Class: "normal", AvoidFamily: "anthropic", AvoidModel: "haiku"})
	if !errors.Is(err, ErrNoOtherModel) {
		t.Fatalf("one model, and it did the work under review: %v", err)
	}
	// Another model of the same family is an acceptable reviewer.
	got, err := r.Route(RouteRequest{Class: "two", AvoidFamily: "anthropic", AvoidModel: "haiku"})
	if err != nil || len(got) != 1 || got[0].Model != "sonnet" {
		t.Fatalf("fallback to another model: %v %v", got, err)
	}
	// Nothing usable at all is a different problem: accounts, not rules.
	_, err = r.Route(RouteRequest{Class: "normal", Usable: func(string) bool { return false }})
	if err == nil || errors.Is(err, ErrNoOtherModel) {
		t.Fatalf("no capacity: %v", err)
	}
}

func TestMCPServersAreAllowedPerRole(t *testing.T) {
	c := &ProjectConfig{Roles: map[string]RoleDef{"implementer": {}, "reviewer": {}}, MCP: map[string]MCPServerDef{
		"all":  {Command: "a"},
		"impl": {URL: "https://x/mcp", Type: "sse", Roles: []string{"implementer"}},
	}}
	if errs := c.validateMCP(); len(errs) != 0 {
		t.Fatal(errs)
	}
	if got := c.MCPFor("implementer"); len(got) != 2 {
		t.Fatalf("implementer %v", got)
	}
	if got := c.MCPFor("reviewer"); len(got) != 1 || got["all"].Command != "a" {
		t.Fatalf("reviewer %v", got)
	}
	for name, bad := range map[string]MCPServerDef{
		"barnraise": {Command: "x"},
		"both":      {Command: "x", URL: "https://x"},
		"neither":   {},
		"type":      {URL: "https://x", Type: "ws"},
		"role":      {Command: "x", Roles: []string{"ghost"}},
	} {
		c := &ProjectConfig{Roles: map[string]RoleDef{"implementer": {}}, MCP: map[string]MCPServerDef{name: bad}}
		if errs := c.validateMCP(); len(errs) != 1 {
			t.Errorf("%s: %v", name, errs)
		}
	}
}

func TestCheckAppliesOnlyToWhatTheChangeTouches(t *testing.T) {
	areas := map[string][]string{"backend": {"backend/**"}, "web": {"web/**"}, "mobile": {"mobile/**"}}
	areaOf := func(f string) string { return AreaOf(areas, f) }
	backend := CheckDef{Name: "backend", Scope: "touched", Areas: []string{"backend"}}
	web := CheckDef{Name: "web", Scope: "touched", Areas: []string{"web"}, Paths: []string{"backend/api/openapi.yaml"}}
	mobile := CheckDef{Name: "mobile", Scope: "touched", Areas: []string{"A-mobile"}}
	whole := CheckDef{Name: "lint", Scope: "touched"}
	always := CheckDef{Name: "smoke", Scope: "all", Areas: []string{"mobile"}}
	shared := CheckDef{Name: "contract", Scope: "touched", Paths: []string{"proto/**", "*.lock"}}

	for _, tc := range []struct {
		what    string
		touched []string
		areas   []string
		want    map[string]bool
	}{
		{"backend only", []string{"backend/handler.go"}, nil,
			map[string]bool{"backend": true, "web": false, "mobile": false, "lint": true, "smoke": true, "contract": false}},
		{"web only", []string{"web/src/App.tsx"}, nil,
			map[string]bool{"backend": false, "web": true, "mobile": false, "lint": true, "smoke": true, "contract": false}},
		{"web and mobile", []string{"web/types.ts", "mobile/types.ts"}, nil,
			map[string]bool{"backend": false, "web": true, "mobile": true, "lint": true, "smoke": true, "contract": false}},
		{"the API contract concerns the web check too", []string{"backend/api/openapi.yaml"}, nil,
			map[string]bool{"backend": true, "web": true, "mobile": false, "lint": true, "smoke": true, "contract": false}},
		{"shared files by path", []string{"proto/user.proto"}, nil,
			map[string]bool{"backend": false, "web": false, "mobile": false, "lint": true, "smoke": true, "contract": true}},
		{"a file of no area", []string{"README.md"}, nil,
			map[string]bool{"backend": false, "web": false, "mobile": false, "lint": true, "smoke": true, "contract": false}},
		{"the item's own area counts", []string{"README.md"}, []string{"A-mobile"},
			map[string]bool{"backend": false, "web": false, "mobile": true, "lint": true, "smoke": true, "contract": false}},
		{"nothing known: never skip blind", nil, nil,
			map[string]bool{"backend": true, "web": true, "mobile": true, "lint": true, "smoke": true, "contract": true}},
	} {
		for _, d := range []CheckDef{backend, web, mobile, whole, always, shared} {
			got, why := d.Applies(tc.touched, tc.areas, areaOf)
			if got != tc.want[d.Name] {
				t.Errorf("%s: check %s applies=%v, want %v (%s)", tc.what, d.Name, got, tc.want[d.Name], why)
			}
			if !got && why == "" {
				t.Errorf("%s: a skipped check must say why", d.Name)
			}
		}
	}
	// A check for an area that does not exist would never run: the config says so.
	c := &ProjectConfig{Areas: map[string]AreaDef{"backend": {}}, Checks: map[string]CheckDef{"x": {Areas: []string{"bakcend"}}}}
	found := false
	for _, err := range c.Validate() {
		if err != nil && strings.Contains(err.Error(), `"bakcend"`) {
			found = true
		}
	}
	if !found {
		t.Fatal("a misspelled area must be reported")
	}
}

// The selection of a repository like the one Barnraise was born for: a Go
// backend, a web app and a mobile app, with rules for shared and quiet files.
func TestSelectChecks(t *testing.T) {
	c := &ProjectConfig{
		Areas: map[string]AreaDef{"backend": {Paths: []string{"backend/**"}}, "web": {Paths: []string{"web/**"}}, "mobile": {Paths: []string{"mobile/**"}}},
		Checks: map[string]CheckDef{
			"personal-data":  {Name: "personal-data", Scope: "all", Order: -10},
			"backend-lint":   {Name: "backend-lint", Scope: "touched", Areas: []string{"backend"}, Order: 1},
			"backend-test":   {Name: "backend-test", Scope: "touched", Areas: []string{"backend"}, Order: 2},
			"backend-integr": {Name: "backend-integr", Scope: "touched", Areas: []string{"backend"}, Covers: []string{"backend-test"}, Order: 5},
			"web-test":       {Name: "web-test", Scope: "touched", Areas: []string{"web"}, Paths: []string{"backend/api/openapi.yaml"}, Order: 2},
			"mobile-maestro": {Name: "mobile-maestro", Scope: "touched", Areas: []string{"mobile"}, Order: 9},
		},
		Selection: SelectionDef{AllWhen: []string{"Makefile", "scripts/**", "**/go.mod"}, NoneWhen: []string{"BACKLOG.md", "docs/**"}},
	}
	var names []string
	for n := range c.Checks {
		names = append(names, n)
	}
	runs := func(touched []string, areas ...string) string {
		var out []string
		for _, d := range c.SelectChecks(names, touched, areas) {
			if d.Run {
				out = append(out, d.Name)
			} else if d.Why == "" {
				t.Errorf("%s skipped without a reason", d.Name)
			}
		}
		return strings.Join(out, " ")
	}
	const all = "personal-data backend-lint web-test backend-integr mobile-maestro" // backend-test is covered
	for _, tc := range []struct {
		what    string
		touched []string
		want    string
	}{
		{"backend code: lint, then integration (which covers the unit tests)", []string{"backend/internal/user.go"}, "personal-data backend-lint backend-integr"},
		{"web code", []string{"web/src/App.tsx"}, "personal-data web-test"},
		{"mobile code", []string{"mobile/src/Home.tsx"}, "personal-data mobile-maestro"},
		{"web and mobile", []string{"web/a.ts", "mobile/a.ts"}, "personal-data web-test mobile-maestro"},
		{"the API contract concerns the web app too", []string{"backend/api/openapi.yaml"}, "personal-data backend-lint web-test backend-integr"},
		{"the backlog needs nothing but what always runs", []string{"BACKLOG.md", "docs/guide.md"}, "personal-data"},
		{"quiet files next to code do not widen the run", []string{"BACKLOG.md", "web/a.ts"}, "personal-data web-test"},
		{"infrastructure runs everything", []string{"Makefile"}, all},
		{"a dependency file runs everything, even inside a part", []string{"backend/go.mod"}, all},
		{"an unknown path runs everything", []string{"tools/gen.py"}, all},
		{"nothing known runs everything", nil, all},
	} {
		if got := runs(tc.touched); got != tc.want {
			t.Errorf("%s:\n got  %s\n want %s", tc.what, got, tc.want)
		}
	}
	// The item's own area is used only when no file is known.
	if got := runs(nil, "mobile"); got != "personal-data mobile-maestro" {
		t.Errorf("by the item's area: %s", got)
	}
	if got := runs([]string{"web/a.ts"}, "mobile"); got != "personal-data web-test" {
		t.Errorf("the files win over the item's area: %s", got)
	}
	// Two checks that cover each other do not cancel each other out.
	c.Checks["a"] = CheckDef{Name: "a", Covers: []string{"b"}}
	c.Checks["b"] = CheckDef{Name: "b", Covers: []string{"a"}}
	ran := 0
	for _, d := range c.SelectChecks([]string{"a", "b"}, []string{"Makefile"}, nil) {
		if d.Run {
			ran++
		}
	}
	if ran != 2 {
		t.Errorf("mutual covers: %d ran", ran)
	}
	delete(c.Checks, "a")
	delete(c.Checks, "b")
	c.Checks["x"] = CheckDef{Name: "x", Covers: []string{"ghost"}}
	found := false
	for _, err := range c.Validate() {
		if strings.Contains(err.Error(), `covers "ghost"`) {
			found = true
		}
	}
	if !found {
		t.Error("covering a check that does not exist must be reported")
	}
}
