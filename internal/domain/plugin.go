package domain

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Plugin kinds an external program may implement (docs/plugins.md).
const (
	PluginTracker  = "tracker"
	PluginCheck    = "check"
	PluginNotifier = "notifier"
	PluginHook     = "hook"
)

// PluginDef is an external plugin: a program the owner configured, which
// the daemon starts and talks to over JSON-RPC on its standard input and
// output. Agents never configure plugins: barnraise.yaml and .barnraise/**
// are denied to them, and so is a plugin program inside the repository.
type PluginDef struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	// Env lists names of the daemon's environment variables passed through
	// to the program. Values never go into barnraise.yaml.
	Env []string `json:"env,omitempty"`
	// Secrets maps a variable name to a secret reference (keychain:…, env:…)
	// resolved when the program starts.
	Secrets map[string]string `json:"secrets,omitempty"`
	// Timeout bounds each call (default 30s); check.run uses the check's own timeout.
	Timeout time.Duration `json:"timeout"`
	// Events, for a hook plugin, are the event types it receives; a trailing
	// * matches a prefix (item.step.*). None: it receives no events.
	Events []string `json:"events,omitempty"`
	// Options are the plugin's own settings, sent in initialize.
	Options map[string]any `json:"options,omitempty"`
}

// PluginRef returns the plugin name of a "plugin:<name>" reference.
func PluginRef(s string) (string, bool) {
	name, ok := strings.CutPrefix(strings.TrimSpace(s), "plugin:")
	return name, ok
}

// PluginFile returns the repository-relative path of a plugin program that
// lives in the repository ("" for a program on the PATH or outside it).
func PluginFile(command string) string {
	c := strings.TrimSpace(command)
	if c == "" || path.IsAbs(c) || strings.HasPrefix(c, "~") || !strings.Contains(c, "/") {
		return ""
	}
	p := path.Clean(c)
	if p == ".." || strings.HasPrefix(p, "../") {
		return ""
	}
	return p
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (c *ProjectConfig) validatePlugins() []error {
	var errs []error
	names := make([]string, 0, len(c.Plugins))
	for n := range c.Plugins {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		d := c.Plugins[name]
		if Slug(name, 40) != name {
			errs = append(errs, fmt.Errorf("plugin name %q must be lower case letters, digits and dashes", name))
		}
		if strings.TrimSpace(d.Command) == "" {
			errs = append(errs, fmt.Errorf("plugin %s has no command", name))
		}
		for _, e := range d.Env {
			if !envNameRe.MatchString(e) {
				errs = append(errs, fmt.Errorf("plugin %s: env lists %q, which is not a variable name (list names only; values stay out of barnraise.yaml)", name, e))
			}
		}
		for k, ref := range d.Secrets {
			if !envNameRe.MatchString(k) {
				errs = append(errs, fmt.Errorf("plugin %s: secret %q is not a variable name", name, k))
			}
			if !strings.HasPrefix(ref, "keychain:") && !strings.HasPrefix(ref, "env:") {
				errs = append(errs, fmt.Errorf("plugin %s: secret %s must be a reference (keychain:<key> or env:<NAME>), never the value itself", name, k))
			}
		}
		if d.Timeout < 0 {
			errs = append(errs, fmt.Errorf("plugin %s: timeout must be positive", name))
		}
	}
	known := func(what, ref string) {
		if name, ok := PluginRef(ref); ok {
			if _, ok := c.Plugins[name]; !ok {
				errs = append(errs, fmt.Errorf("%s uses plugin %q, which is not declared in plugins", what, name))
			}
		}
	}
	known("the tracker", c.Tracker.Type)
	checks := make([]string, 0, len(c.Checks))
	for n := range c.Checks {
		checks = append(checks, n)
	}
	sort.Strings(checks)
	for _, n := range checks {
		known("check "+n, c.Checks[n].RunsOn)
	}
	for i, n := range c.Notifications {
		known(fmt.Sprintf("notification %d", i+1), n.Type)
	}
	return errs
}
