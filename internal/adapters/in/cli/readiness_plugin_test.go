package cli

import (
	"context"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

type fakeFile struct{ name string }

func (f fakeFile) Name() string       { return f.name }
func (f fakeFile) Size() int64        { return 1 }
func (f fakeFile) Mode() fs.FileMode  { return 0o755 }
func (f fakeFile) ModTime() time.Time { return time.Time{} }
func (f fakeFile) IsDir() bool        { return false }
func (f fakeFile) Sys() any           { return nil }

// withPlugins is a ready project that also declares plugins.
func withPlugins(plugins map[string]domain.PluginDef) *domain.ProjectConfig {
	cfg := nobiraLike()
	cfg.Plugins = plugins
	return cfg
}

func pluginReadiness(tools map[string]bool, files map[string]bool) readiness {
	r := fakeMachine{gh: true, tools: tools}.readiness()
	r.stat = func(p string) (os.FileInfo, error) {
		if files[p] {
			return fakeFile{name: p}, nil
		}
		return nil, os.ErrNotExist
	}
	return r
}

func TestReadinessFlagsMissingPluginProgram(t *testing.T) {
	r := pluginReadiness(map[string]bool{"make": true, "golangci-lint": true}, nil)
	got := r.problems(context.Background(), withPlugins(map[string]domain.PluginDef{
		"jira":  {Command: "jira-bridge"},
		"farm":  {Command: "tools/farm-plugin"},
		"slack": {Command: ""},
	}))
	if len(got) != 2 {
		t.Fatalf("problems = %d, want 2:\n%s", len(got), strings.Join(got, "\n"))
	}
	for i, want := range []string{
		"plugin farm runs tools/farm-plugin, which does not exist: the daemon starts plugins",
		"plugin jira runs jira-bridge, which is not on the PATH: the daemon starts plugins with the PATH of the shell that starts it",
	} {
		if !strings.HasPrefix(got[i], want) {
			t.Errorf("problem %d = %q, want it to start with %q", i, got[i], want)
		}
	}
}

func TestReadinessPluginsOnPath(t *testing.T) {
	r := pluginReadiness(
		map[string]bool{"make": true, "golangci-lint": true, "jira-bridge": true},
		map[string]bool{"/repo/tools/farm-plugin": true},
	)
	got := r.problems(context.Background(), withPlugins(map[string]domain.PluginDef{
		"jira": {Command: "jira-bridge"},
		"farm": {Command: "tools/farm-plugin"},
	}))
	if len(got) != 0 {
		t.Fatalf("ready machine has problems: %v", got)
	}
}

// The fake machine of the older tests sets no stat: readiness falls back to os.Stat.
func TestReadinessPluginNilStat(t *testing.T) {
	r := fakeMachine{gh: true, tools: map[string]bool{"make": true, "golangci-lint": true}}.readiness()
	got := r.problems(context.Background(), withPlugins(map[string]domain.PluginDef{"x": {Command: "/surely/not/here-barnraise"}}))
	if len(got) != 1 || !strings.Contains(got[0], "plugin x runs /surely/not/here-barnraise, which does not exist") {
		t.Fatalf("problems: %v", got)
	}
}
