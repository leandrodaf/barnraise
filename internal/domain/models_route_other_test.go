package domain

import (
	"errors"
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
