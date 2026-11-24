package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWorktreeSeedStripAndStores(t *testing.T) {
	cfg, err := Compile(t.TempDir(), []byte(`worktree:
  seed: [web/node_modules, ./mobile/node_modules/]
  strip: [web/node_modules, mobile/android/app/build]
  strip_after: 3h
  stores: { pnpm: npm_config_store_dir, gradle-cache: GRADLE_USER_HOME }
`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"web/node_modules", "mobile/node_modules"}; !reflect.DeepEqual(cfg.WorktreeSeed, want) {
		t.Fatalf("seed %v, want %v (paths cleaned)", cfg.WorktreeSeed, want)
	}
	if len(cfg.WorktreeStrip) != 2 || cfg.WorktreeStripAfter != 3*time.Hour {
		t.Fatalf("strip %v after %s", cfg.WorktreeStrip, cfg.WorktreeStripAfter)
	}
	if cfg.WorktreeStores["pnpm"] != "npm_config_store_dir" || cfg.WorktreeStores["gradle-cache"] != "GRADLE_USER_HOME" {
		t.Fatalf("stores %v", cfg.WorktreeStores)
	}
	// Nothing asked: nothing seeded, stripped or shared, and an hour of patience by default.
	plain, err := Compile(t.TempDir(), []byte("worktree: { setup: make install }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.WorktreeSeed)+len(plain.WorktreeStrip)+len(plain.WorktreeStores) != 0 || plain.WorktreeStripAfter != time.Hour {
		t.Fatalf("defaults: %v %v %v %s", plain.WorktreeSeed, plain.WorktreeStrip, plain.WorktreeStores, plain.WorktreeStripAfter)
	}
	bad := map[string]string{
		"worktree: { seed: [/etc] }":                              "inside the repository",
		"worktree: { seed: [../other] }":                          "inside the repository",
		"worktree: { strip: [a/../../b] }":                        "inside the repository",
		"worktree: { strip: ['.'] }":                              "whole worktree",
		"worktree: { strip: [''] }":                               "whole worktree",
		"worktree: { strip: [.git/objects] }":                     "belongs to git or to Barnraise",
		"worktree: { seed: [.barnraise-session/home] }":           "belongs to git or to Barnraise",
		"worktree: { strip_after: soon }":                         "worktree.strip_after",
		"worktree: { stores: { 'Pnpm Store': X } }":               "not a store name",
		"worktree: { stores: { pnpm: 'npm config' } }":            "not an environment variable",
		"worktree: { stores: { pnpm: BARNRAISE_TOKEN } }":         "not an environment variable",
		"worktree: { stores: { '../escape': GRADLE_USER_HOME } }": "not a store name",
	}
	for yml, want := range bad {
		if _, err := Compile(t.TempDir(), []byte(yml+"\n")); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %v, want one about %q", yml, err, want)
		}
	}
}

func TestWorktreeIsolationCachesAndHooksFromYAML(t *testing.T) {
	yml := `project: demo
template: blank
worktree:
  include: [.env]
  port_base: 5000
  port_range: 10
  setup: make install
  isolate:
    compose: "on"
    env:
      DB_NAME: "app_{{.Item}}"
      DATABASE_URL: '{{dbURL (env "DATABASE_URL") (print "app_" .Item)}}'
    teardown: dropdb --if-exists "$DB_NAME"
    teardown_timeout: 2m
  build_cache: [target/, .next/cache]
  build_cache_fallback: copy
  hooks:
    post_create: make db
    pre_session: make warm
    post_integrate: make publish
    pre_remove: make clean
    timeout: 3m
`
	cfg, err := Compile(t.TempDir(), []byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	if errs := cfg.Validate(); len(errs) != 0 {
		t.Fatal(errs)
	}
	wo := cfg.Worktree
	if cfg.PortBase != 5000 || wo.PortRange != 10 || cfg.WorktreeSetup != "make install" || len(cfg.EnvFiles) != 1 {
		t.Fatalf("worktree %+v / %d %q", wo, cfg.PortBase, cfg.WorktreeSetup)
	}
	if wo.Isolate.Compose != "on" || wo.Isolate.Env["DB_NAME"] != "app_{{.Item}}" || !strings.Contains(wo.Isolate.Teardown, "dropdb") || wo.Isolate.TeardownTimeout != 2*time.Minute {
		t.Fatalf("isolate %+v", wo.Isolate)
	}
	if strings.Join(wo.BuildCache, ",") != "target,.next/cache" || !wo.BuildCacheCopy {
		t.Fatalf("build cache %v %v", wo.BuildCache, wo.BuildCacheCopy)
	}
	h := wo.Hooks
	if h.PostCreate != "make db" || h.PreSession != "make warm" || h.PostIntegrate != "make publish" || h.PreRemove != "make clean" || h.Timeout != 3*time.Minute {
		t.Fatalf("hooks %+v", h)
	}
	out, err := Explain(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"port_range: 10", "build_cache_fallback: copy", "pre_session: make warm", "compose: \"on\""} {
		if !strings.Contains(out, want) {
			t.Errorf("explain lacks %q:\n%s", want, out)
		}
	}
}

// A project that sets none of the new keys keeps its old behaviour.
func TestWorktreeDefaultsKeepOldProjects(t *testing.T) {
	cfg, err := Compile(t.TempDir(), []byte("project: demo\ntemplate: blank\nworktree:\n  port_base: 4200\n"))
	if err != nil {
		t.Fatal(err)
	}
	wo := cfg.Worktree
	if wo.PortRange != 1 || wo.Isolate.ComposeMode() != "auto" || len(wo.Isolate.Env) != 0 || wo.Isolate.Teardown != "" ||
		len(wo.BuildCache) != 0 || wo.BuildCacheCopy || wo.Hooks.PostCreate != "" || wo.Hooks.Timeout != 10*time.Minute || wo.Isolate.TeardownTimeout != 5*time.Minute {
		t.Fatalf("defaults %+v", wo)
	}
}

func TestWorktreeOptionErrors(t *testing.T) {
	for name, body := range map[string]string{
		"fallback":         "build_cache_fallback: always",
		"cache outside":    "build_cache: [../up]",
		"teardown timeout": "isolate: { teardown_timeout: soon }",
		"hooks timeout":    "hooks: { timeout: soon }",
		"reserved env":     "isolate: { env: { BARNRAISE_TOKEN: x } }",
		"bad template":     "isolate: { env: { X: \"{{.Nope}}\" } }",
		"compose":          "isolate: { compose: sometimes }",
	} {
		_, err := Compile(t.TempDir(), []byte("project: demo\ntemplate: blank\nworktree:\n  "+body+"\n"))
		if err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
