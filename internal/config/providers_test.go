package config

import (
	"slices"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func TestAPIProvidersCompile(t *testing.T) {
	yml := `providers:
  deepseek:
    base_url: https://api.deepseek.com
  vllm:
    base_url: http://127.0.0.1:8000/v1
models:
  classes:
    cheap: [openrouter/deepseek/deepseek-chat, "openrouter/qwen/qwen3-coder:free@low", claude/haiku]
`
	cfg, err := Compile(t.TempDir(), []byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	if errs := cfg.Validate(); len(errs) > 0 {
		t.Fatal(errs)
	}
	if got := cfg.APIProviders["openrouter"].BaseURL; got != "https://openrouter.ai/api/v1" {
		t.Fatalf("built-in openrouter = %q", got)
	}
	if cfg.APIProviders["deepseek"].BaseURL != "https://api.deepseek.com" || cfg.APIProviders["vllm"].BaseURL != "http://127.0.0.1:8000/v1" {
		t.Fatalf("providers = %+v", cfg.APIProviders)
	}
	want := []domain.ModelChoice{
		{Provider: "openrouter", Model: "deepseek/deepseek-chat"},
		{Provider: "openrouter", Model: "qwen/qwen3-coder:free", Effort: "low"},
		{Provider: "claude", Model: "haiku"},
	}
	if got := cfg.Models.Classes["cheap"]; !slices.Equal(got, want) {
		t.Fatalf("cheap = %+v", got)
	}
	if hosts := APIHosts(cfg); !slices.Equal(hosts, []string{"127.0.0.1", "api.deepseek.com", "openrouter.ai"}) {
		t.Fatalf("hosts = %v", hosts)
	}
	out, err := Explain(cfg)
	if err != nil || !strings.Contains(out, "base_url: https://api.deepseek.com") {
		t.Fatalf("explain: %v\n%s", err, out)
	}
}

func TestAPIProvidersOverrideTheBuiltIn(t *testing.T) {
	cfg, err := Compile(t.TempDir(), []byte("providers:\n  openrouter:\n    base_url: https://eu.openrouter.ai/api/v1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.APIProviders["openrouter"].BaseURL; got != "https://eu.openrouter.ai/api/v1" {
		t.Fatalf("openrouter = %q", got)
	}
}

func TestAPIProvidersValidate(t *testing.T) {
	// Names and missing URLs are reported with the rest of the configuration.
	for yml, want := range map[string]string{
		"providers:\n  codex:\n    base_url: https://x.example/v1\n":     "taken by the provider with its own CLI",
		"providers:\n  Deep-Seek:\n    base_url: https://x.example/v1\n": "lower-case letters and digits",
		"providers:\n  deepseek: {}\n":                                   "needs a base_url",
	} {
		cfg, err := Compile(t.TempDir(), []byte(yml))
		if err != nil {
			t.Fatal(err)
		}
		var msgs []string
		for _, e := range cfg.Validate() {
			msgs = append(msgs, e.Error())
		}
		if !strings.Contains(strings.Join(msgs, "\n"), want) {
			t.Errorf("%s: want %q, got %q", yml, want, msgs)
		}
	}
	// An endpoint the key must not travel to stops the compile.
	for yml, want := range map[string]string{
		"providers:\n  deepseek:\n    base_url: ftp://x.example\n":              "is not an http(s) URL",
		"providers:\n  deepseek:\n    base_url: https://\n":                     "is not an http(s) URL",
		"providers:\n  deepseek:\n    base_url: http://api.deepseek.com\n":      "must use https",
		"providers:\n  deepseek:\n    base_url: https://k:s@api.deepseek.com\n": "must not carry credentials",
	} {
		if _, err := Compile(t.TempDir(), []byte(yml)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", yml, want, err)
		}
	}
	for _, ok := range []string{"http://localhost:8000/v1", "http://[::1]:8000/v1", "http://127.0.0.1:1234"} {
		if _, err := Compile(t.TempDir(), []byte("providers:\n  local:\n    base_url: \""+ok+"\"\n")); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
}

func TestParseModelChoiceWithVendor(t *testing.T) {
	for in, want := range map[string]domain.ModelChoice{
		"openrouter/deepseek/deepseek-chat":          {Provider: "openrouter", Model: "deepseek/deepseek-chat"},
		"openrouter/anthropic/claude-haiku-4.5@high": {Provider: "openrouter", Model: "anthropic/claude-haiku-4.5", Effort: "high"},
		"deepseek/deepseek-reasoner":                 {Provider: "deepseek", Model: "deepseek-reasoner"},
		" openrouter/meta-llama/llama-4-scout:free ": {Provider: "openrouter", Model: "meta-llama/llama-4-scout:free"},
	} {
		got, err := ParseModelChoice(in)
		if err != nil || got != want {
			t.Errorf("%q = %+v, %v", in, got, err)
		}
	}
	if _, err := ParseModelChoice("openrouter/"); err == nil {
		t.Fatal("a provider without a model must fail")
	}
}
