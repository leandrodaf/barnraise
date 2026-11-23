package domain

import (
	"strings"
	"testing"
)

func TestAPIProviders(t *testing.T) {
	c := &ProjectConfig{APIProviders: DefaultAPIProviders()}
	c.APIProviders["vllm"] = APIProviderDef{BaseURL: "http://localhost:8000/v1"}
	if errs := c.validateAPIProviders(); len(errs) != 0 {
		t.Fatal(errs)
	}
	if APIKeySecret("openrouter-1") != "account/openrouter-1/api-key" {
		t.Fatal(APIKeySecret("openrouter-1"))
	}
	for name, d := range map[string]APIProviderDef{
		"claude":     {BaseURL: "https://x.example"},
		"Open-Route": {BaseURL: "https://x.example"},
		"empty":      {},
	} {
		c := &ProjectConfig{APIProviders: map[string]APIProviderDef{name: d}}
		errs := c.validateAPIProviders()
		if len(errs) != 1 || !strings.Contains(errs[0].Error(), "providers") {
			t.Errorf("%s: %v", name, errs)
		}
	}
	// Validate reports them with the rest of the configuration.
	bad := &ProjectConfig{Name: "p", APIProviders: map[string]APIProviderDef{"x": {}}}
	if !strings.Contains(joinErrs(bad.Validate()), "providers.x needs a base_url") {
		t.Fatal(bad.Validate())
	}
}

func joinErrs(errs []error) string {
	var b strings.Builder
	for _, e := range errs {
		b.WriteString(e.Error() + "\n")
	}
	return b.String()
}

// A step may name an OpenRouter model directly; the vendor stays part of
// the model, and a provider without a ladder keeps its model on rework.
func TestRouteOpenRouterModels(t *testing.T) {
	r := DefaultModelRouter()
	got, err := r.Route(RouteRequest{FixedModel: "openrouter/deepseek/deepseek-chat", Tier: 2})
	if err != nil || got[0] != (ModelChoice{Provider: "openrouter", Model: "deepseek/deepseek-chat"}) {
		t.Fatalf("route = %v %v", got, err)
	}
	r.Classes["cheap"] = []ModelChoice{{Provider: "openrouter", Model: "qwen/qwen3-coder"}, {Provider: "claude", Model: "haiku"}}
	got, err = r.Route(RouteRequest{Class: "cheap", Usable: func(p string) bool { return p == "openrouter" }})
	if err != nil || len(got) != 1 || got[0].Model != "qwen/qwen3-coder" || r.Family("openrouter") != "mixed" {
		t.Fatalf("route = %v %v", got, err)
	}
}
