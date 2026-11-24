package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// rawWorktreeOpts are the keys of `worktree:` beyond include, port_base,
// safety_commit and setup: per-item isolation, build caches, phase hooks.
type rawWorktreeOpts struct {
	PortRange int `yaml:"port_range"`
	Isolate   struct {
		Compose         string            `yaml:"compose"`
		Env             map[string]string `yaml:"env"`
		Teardown        string            `yaml:"teardown"`
		TeardownTimeout string            `yaml:"teardown_timeout"`
	} `yaml:"isolate"`
	BuildCache         []string `yaml:"build_cache"`
	BuildCacheFallback string   `yaml:"build_cache_fallback"`
	Hooks              struct {
		PostCreate    string `yaml:"post_create"`
		PreSession    string `yaml:"pre_session"`
		PostIntegrate string `yaml:"post_integrate"`
		PreRemove     string `yaml:"pre_remove"`
		Timeout       string `yaml:"timeout"`
	} `yaml:"hooks"`
}

func (r rawWorktreeOpts) convert() (domain.WorktreeOpts, error) {
	o := domain.WorktreeOpts{
		PortRange: r.PortRange,
		Isolate: domain.WorktreeIsolation{Compose: strings.TrimSpace(r.Isolate.Compose), Env: r.Isolate.Env,
			Teardown: strings.TrimSpace(r.Isolate.Teardown), TeardownTimeout: 5 * time.Minute},
		Hooks: domain.WorktreeHooks{PostCreate: strings.TrimSpace(r.Hooks.PostCreate), PreSession: strings.TrimSpace(r.Hooks.PreSession),
			PostIntegrate: strings.TrimSpace(r.Hooks.PostIntegrate), PreRemove: strings.TrimSpace(r.Hooks.PreRemove), Timeout: 10 * time.Minute},
	}
	if o.PortRange == 0 {
		o.PortRange = 1
	}
	if v := r.Isolate.TeardownTimeout; v != "" {
		d, err := domain.ParseDuration(v)
		if err != nil {
			return o, fmt.Errorf("worktree.isolate.teardown_timeout: %w", err)
		}
		o.Isolate.TeardownTimeout = d
	}
	if v := r.Hooks.Timeout; v != "" {
		d, err := domain.ParseDuration(v)
		if err != nil {
			return o, fmt.Errorf("worktree.hooks.timeout: %w", err)
		}
		o.Hooks.Timeout = d
	}
	switch r.BuildCacheFallback {
	case "", "skip":
	case "copy":
		o.BuildCacheCopy = true
	default:
		return o, fmt.Errorf("worktree.build_cache_fallback must be skip or copy, not %q", r.BuildCacheFallback)
	}
	for _, d := range r.BuildCache {
		clean, err := domain.CleanBuildCacheDir(d)
		if err != nil {
			return o, err
		}
		o.BuildCache = append(o.BuildCache, clean)
	}
	if err := o.Validate(); err != nil {
		return o, err
	}
	return o, nil
}

// addWorktreeView adds the keys of worktree.go to the effective
// configuration that `barnraise config` prints.
func addWorktreeView(w map[string]any, cfg *domain.ProjectConfig) {
	wo := cfg.Worktree
	w["port_range"] = wo.PortRange
	w["isolate"] = map[string]any{"compose": wo.Isolate.ComposeMode(), "env": wo.Isolate.Env, "teardown": wo.Isolate.Teardown,
		"teardown_timeout": wo.Isolate.TeardownTimeout.String()}
	fallback := "skip"
	if wo.BuildCacheCopy {
		fallback = "copy"
	}
	w["build_cache"], w["build_cache_fallback"] = wo.BuildCache, fallback
	w["hooks"] = map[string]any{"post_create": wo.Hooks.PostCreate, "pre_session": wo.Hooks.PreSession,
		"post_integrate": wo.Hooks.PostIntegrate, "pre_remove": wo.Hooks.PreRemove, "timeout": wo.Hooks.Timeout.String()}
}
