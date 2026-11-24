package config

import (
	"slices"
	"strings"
	"testing"
)

func TestACPProvidersCompile(t *testing.T) {
	yml := `providers:
  myagent:
    type: acp
    command: my-agent
    args: [--acp, --quiet]
    env:
      MY_AGENT_KEY: keychain:my-agent
  openrouter:
    type: acp
    command: or-agent
  deepseek:
    base_url: https://api.deepseek.com/v1
`
	cfg, err := Compile(t.TempDir(), []byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	d, ok := cfg.ACPProviders["myagent"]
	if !ok || d.Command != "my-agent" || !slices.Equal(d.Args, []string{"--acp", "--quiet"}) || d.Env["MY_AGENT_KEY"] != "keychain:my-agent" {
		t.Fatalf("myagent = %+v", d)
	}
	if _, ok := cfg.APIProviders["myagent"]; ok {
		t.Fatal("an ACP agent is not a key-based provider")
	}
	if _, ok := cfg.APIProviders["openrouter"]; ok {
		t.Fatal("an ACP agent of the same name replaces the built-in endpoint")
	}
	if cfg.APIProviders["deepseek"].BaseURL == "" {
		t.Fatal("key-based providers still compile")
	}
	if errs := cfg.Validate(); len(errs) != 0 {
		t.Fatal(errs)
	}
	out, err := Explain(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "acp_providers") || !strings.Contains(out, "MY_AGENT_KEY") || strings.Contains(out, "keychain:my-agent") {
		t.Fatalf("explain shows the names of the variables, not their values:\n%s", out)
	}
}

func TestACPProvidersRefused(t *testing.T) {
	for yml, want := range map[string]string{
		"providers:\n  x:\n    type: grpc\n    command: y\n":                         `type "grpc" is not known`,
		"providers:\n  x:\n    command: y\n":                                         "add `type: acp`",
		"providers:\n  x:\n    type: acp\n    command: y\n    base_url: https://z\n": "has no base_url",
	} {
		if _, err := Compile(t.TempDir(), []byte(yml)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", yml, want, err)
		}
	}
	cfg, err := Compile(t.TempDir(), []byte("providers:\n  x:\n    type: acp\n"))
	if err != nil {
		t.Fatal(err)
	}
	var msgs []string
	for _, e := range cfg.Validate() {
		msgs = append(msgs, e.Error())
	}
	if !strings.Contains(strings.Join(msgs, "\n"), "needs a command") {
		t.Fatalf("validate: %v", msgs)
	}
}
