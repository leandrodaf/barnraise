package domain

import (
	"strings"
	"testing"
)

func validateText(c *ProjectConfig) string {
	var b strings.Builder
	for _, err := range c.Validate() {
		b.WriteString(err.Error())
		b.WriteByte('\n')
	}
	return b.String()
}

func TestPluginRefAndFile(t *testing.T) {
	if n, ok := PluginRef("plugin:youtrack"); !ok || n != "youtrack" {
		t.Fatal(n, ok)
	}
	if _, ok := PluginRef("github"); ok {
		t.Fatal("github is not a plugin")
	}
	for cmd, want := range map[string]string{
		".barnraise/plugins/yt.py": ".barnraise/plugins/yt.py",
		"./scripts/../tools/yt":    "tools/yt",
		"yt-plugin":                "",
		"/usr/local/bin/yt":        "",
		"~/bin/yt":                 "",
		"../outside/yt":            "",
		"":                         "",
	} {
		if got := PluginFile(cmd); got != want {
			t.Errorf("PluginFile(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestPluginValidation(t *testing.T) {
	ok := &ProjectConfig{
		Name: "shop", Approves: "owner",
		Tracker: TrackerDef{Type: "plugin:yt"},
		Plugins: map[string]PluginDef{
			"yt":   {Command: "yt-plugin", Env: []string{"YT_URL"}, Secrets: map[string]string{"YT_TOKEN": "keychain:yt"}},
			"farm": {Command: ".barnraise/plugins/farm"},
		},
		Checks:        map[string]CheckDef{"devices": {Name: "devices", RunsOn: "plugin:farm"}},
		Notifications: []NotifierDef{{Type: "plugin:yt"}},
	}
	if s := validateText(ok); strings.Contains(s, "plugin") || strings.Contains(s, "tracker") {
		t.Fatalf("a valid plugin setup is reported:\n%s", s)
	}
	bad := &ProjectConfig{
		Name: "shop", Approves: "owner",
		Tracker: TrackerDef{Type: "plugin:ghost"},
		Plugins: map[string]PluginDef{
			"Bad Name": {Command: "x"},
			"empty":    {},
			"leaky":    {Command: "x", Env: []string{"TOKEN=abc"}, Secrets: map[string]string{"TOKEN": "abc123", "2X": "env:X"}, Timeout: -1},
		},
		Checks:        map[string]CheckDef{"devices": {Name: "devices", RunsOn: "plugin:nope"}},
		Notifications: []NotifierDef{{Type: "plugin:gone"}},
	}
	s := validateText(bad)
	for _, want := range []string{
		`plugin name "Bad Name" must be lower case`,
		"plugin empty has no command",
		`env lists "TOKEN=abc", which is not a variable name`,
		"secret TOKEN must be a reference",
		`secret "2X" is not a variable name`,
		"plugin leaky: timeout must be positive",
		`the tracker uses plugin "ghost", which is not declared in plugins`,
		`check devices uses plugin "nope"`,
		`notification 1 uses plugin "gone"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	unknown := &ProjectConfig{Name: "shop", Approves: "owner", Tracker: TrackerDef{Type: "trello"}, Checks: map[string]CheckDef{"x": {RunsOn: "moon"}}}
	s = validateText(unknown)
	if !strings.Contains(s, "plugin:<name>") {
		t.Fatalf("the errors must name plugin:<name> as a choice:\n%s", s)
	}
}

func TestDefaultPolicyDeniesEveryProjectFile(t *testing.T) {
	p := DefaultPolicy()
	for _, f := range []string{"barnraise.yaml", "backend/barnraise.yaml", ".barnraise/plugins/yt.py"} {
		if d := p.Decide(ToolCall{Tool: "Write", Paths: []string{f}, Write: true}, nil); d.Allow {
			t.Errorf("an agent may write %s", f)
		}
	}
}

func TestPluginDefValidate(t *testing.T) {
	if errs := (PluginDef{Command: "zt-plugin", Env: []string{"ZT_NETWORK"}, Secrets: map[string]string{"ZT_TOKEN": "keychain:zerotier"}}).Validate("network_plugin"); len(errs) != 0 {
		t.Fatal(errs)
	}
	errs := (PluginDef{Secrets: map[string]string{"ZT_TOKEN": "abc"}, Timeout: -1}).Validate("network_plugin")
	var b strings.Builder
	for _, err := range errs {
		b.WriteString(err.Error() + "\n")
	}
	for _, want := range []string{"network_plugin has no command", "network_plugin: secret ZT_TOKEN must be a reference", "network_plugin: timeout must be positive"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q in:\n%s", want, b.String())
		}
	}
}
