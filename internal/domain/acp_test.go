package domain

import (
	"strings"
	"testing"
)

func TestACPProviders(t *testing.T) {
	ok := &ProjectConfig{Name: "p", ACPProviders: map[string]ACPProviderDef{
		"myagent": {Command: "my-agent", Args: []string{"--acp"}, Env: map[string]string{"MY_KEY": "keychain:my"}},
	}}
	if errs := ok.validateACPProviders(); len(errs) != 0 {
		t.Fatal(errs)
	}
	for name, c := range map[string]struct {
		def  ACPProviderDef
		api  bool
		want string
	}{
		"claude":  {def: ACPProviderDef{Command: "x"}, want: "taken by a provider of Barnraise"},
		"acp":     {def: ACPProviderDef{Command: "x"}, want: "taken by a provider of Barnraise"},
		"My-Bot":  {def: ACPProviderDef{Command: "x"}, want: "lower-case letters"},
		"nocmd":   {want: "needs a command"},
		"both":    {def: ACPProviderDef{Command: "x"}, api: true, want: "key-based provider too"},
		"badenv":  {def: ACPProviderDef{Command: "x", Env: map[string]string{"1X": "v"}}, want: "is not a variable name"},
		"ownsenv": {def: ACPProviderDef{Command: "x", Env: map[string]string{"BARNRAISE_TOKEN": "v"}}, want: "set by Barnraise"},
	} {
		cfg := &ProjectConfig{Name: "p", ACPProviders: map[string]ACPProviderDef{name: c.def}}
		if c.api {
			cfg.APIProviders = map[string]APIProviderDef{name: {BaseURL: "https://x.example"}}
		}
		if got := joinErrs(cfg.Validate()); !strings.Contains(got, c.want) {
			t.Errorf("%s: want %q, got %q", name, c.want, got)
		}
	}
}
