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
