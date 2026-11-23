package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

const pluginYAML = `project: demo
template: blank
tracker:
  type: plugin:youtrack
  columns: { ready: Todo, integrated: Done }
plugins:
  youtrack:
    command: .barnraise/plugins/youtrack.py
    env: [YOUTRACK_URL]
    secrets: { YOUTRACK_TOKEN: "keychain:youtrack" }
    options: { project: SHOP, board: 3 }
  farm:
    command: tools/farm-plugin
    timeout: 2m
    events: ["item.step.*", item.integrated]
checks:
  devices: { runs_on: "plugin:farm", timeout: 45m }
notifications:
  - type: plugin:youtrack
    target: "#ops"
`

func TestPluginsFromYAML(t *testing.T) {
	root := t.TempDir()
	write(t, root, FileName, pluginYAML)
	write(t, root, ".barnraise/plugins/youtrack.py", "#!/bin/sh\n")
	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	yt := cfg.Plugins["youtrack"]
	if yt.Command != ".barnraise/plugins/youtrack.py" || yt.Env[0] != "YOUTRACK_URL" || yt.Secrets["YOUTRACK_TOKEN"] != "keychain:youtrack" || yt.Timeout != 30*time.Second || yt.Options["project"] != "SHOP" {
		t.Fatalf("youtrack = %+v", yt)
	}
	farm := cfg.Plugins["farm"]
	if farm.Timeout != 2*time.Minute || len(farm.Events) != 2 {
		t.Fatalf("farm = %+v", farm)
	}
	if cfg.Tracker.Type != "plugin:youtrack" || cfg.CodeHost.Type != "git" || cfg.Checks["devices"].RunsOn != "plugin:farm" {
		t.Fatalf("references: %+v %+v", cfg.Tracker, cfg.Checks["devices"])
	}
	for _, err := range cfg.Validate() {
		if strings.Contains(err.Error(), "plugin") {
			t.Fatal(err)
		}
	}
	// The plugin programs in the repository are denied to agents.
	for _, f := range []string{"tools/farm-plugin", ".barnraise/plugins/youtrack.py"} {
		if d := cfg.Policy.Decide(domain.ToolCall{Tool: "Write", Paths: []string{f}, Write: true}, nil); d.Allow {
			t.Errorf("an agent may write %s", f)
		}
	}
	problems, err := Check(root)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(problems, "\n")
	if !strings.Contains(all, "plugin farm runs tools/farm-plugin, which does not exist") || strings.Contains(all, "youtrack.py") {
		t.Fatalf("problems:\n%s", all)
	}
	out, err := Explain(cfg)
	if err != nil || !strings.Contains(out, "plugins:") || !strings.Contains(out, "youtrack.py") {
		t.Fatalf("explain:\n%s %v", out, err)
	}
}

func TestPluginProgramProblems(t *testing.T) {
	root := t.TempDir()
	if s := pluginProgramProblem(root, "x", "no-such-program-barnraise"); !strings.Contains(s, "not on the PATH") {
		t.Fatal(s)
	}
	if s := pluginProgramProblem(root, "x", "sh"); s != "" {
		t.Fatal(s)
	}
	if s := pluginProgramProblem(root, "x", ""); s != "" {
		t.Fatal(s)
	}
	if s := pluginProgramProblem(root, "x", filepath.Join(root, "missing")); !strings.Contains(s, "does not exist") {
		t.Fatal(s)
	}
	if s := pluginProgramProblem(root, "x", "~/surely-not-here-barnraise"); !strings.Contains(s, "does not exist") {
		t.Fatal(s)
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if s := pluginProgramProblem(root, "x", "./dir"); !strings.Contains(s, "does not exist") {
		t.Fatal(s)
	}
}

func TestPluginTimeoutAndPartFiles(t *testing.T) {
	if _, err := Compile(t.TempDir(), []byte("project: demo\ntemplate: blank\nplugins:\n  x: { command: x, timeout: soon }\n")); err == nil || !strings.Contains(err.Error(), "plugins.x.timeout") {
		t.Fatalf("err = %v", err)
	}
	root := t.TempDir()
	write(t, root, FileName, "project: demo\ntemplate: blank\n")
	write(t, root, "backend/"+FileName, "plugins:\n  x: { command: x }\n")
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "plugins belong in the barnraise.yaml at the root") {
		t.Fatalf("err = %v", err)
	}
}
