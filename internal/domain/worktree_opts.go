package domain

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"
)

// WorktreeOpts are the per-item isolation, the build caches and the phase
// hooks of item worktrees (`worktree:` in barnraise.yaml, next to include,
// port_base, setup). The zero value keeps the behaviour of a project that
// sets none of them: one port per item, no extra environment, no cache, no
// hook.
type WorktreeOpts struct {
	// PortRange is how many consecutive ports each item gets, starting at
	// its PORT. 0 or 1 is one port, the historic behaviour.
	PortRange int               `json:"port_range,omitempty"`
	Isolate   WorktreeIsolation `json:"isolate"`
	// BuildCache are directories (relative to the repository) with build
	// artifacts that a new worktree receives copy-on-write from the last
	// integrated worktree or, before there is one, from the main checkout.
	BuildCache []string `json:"build_cache,omitempty"`
	// BuildCacheCopy makes a filesystem without copy-on-write copy the
	// files in full; by default those directories are skipped there.
	BuildCacheCopy bool          `json:"build_cache_copy,omitempty"`
	Hooks          WorktreeHooks `json:"hooks"`
}

// WorktreeIsolation gives each item worktree its own services.
type WorktreeIsolation struct {
	// Compose is auto (default: a compose file at the worktree root means
	// compose was used there), on (always tear the item's compose project
	// down) or off (never).
	Compose string `json:"compose,omitempty"`
	// Env are extra variables, by name, rendered from Go templates with
	// IsolationVars ({{.Item}}, {{.Port}}, ...): a database name per item, a
	// DATABASE_URL rewritten to point at it.
	Env map[string]string `json:"env,omitempty"`
	// Teardown is a command run in the worktree when the item ends
	// (integrated or closed), before its compose project goes down.
	Teardown        string        `json:"teardown,omitempty"`
	TeardownTimeout time.Duration `json:"teardown_timeout,omitempty"`
}

// ComposeMode is the effective compose setting.
func (w WorktreeIsolation) ComposeMode() string {
	if w.Compose == "" {
		return "auto"
	}
	return w.Compose
}

// Worktree phases with a hook.
const (
	HookPostCreate    = "post_create"
	HookPreSession    = "pre_session"
	HookPostIntegrate = "post_integrate"
	HookPreRemove     = "pre_remove"
)

// WorktreeHooks are commands run in an item worktree at a phase of its life.
type WorktreeHooks struct {
	PostCreate    string        `json:"post_create,omitempty"`
	PreSession    string        `json:"pre_session,omitempty"`
	PostIntegrate string        `json:"post_integrate,omitempty"`
	PreRemove     string        `json:"pre_remove,omitempty"`
	Timeout       time.Duration `json:"timeout,omitempty"`
}

// Command is the hook of a phase, or "".
func (h WorktreeHooks) Command(phase string) string {
	switch phase {
	case HookPostCreate:
		return h.PostCreate
	case HookPreSession:
		return h.PreSession
	case HookPostIntegrate:
		return h.PostIntegrate
	case HookPreRemove:
		return h.PreRemove
	}
	return ""
}

// IsolationVars are what an isolation template can use.
type IsolationVars struct {
	Project string // project name
	Item    string // item short id, lowercase letters and digits: i42 (i42b for a second attempt)
	ItemID  string // item id: I-42
	Compose string // the compose project name
	Dir     string // the worktree
	Port    int    // first port of the item
	Ports   []int  // every port of the item
}

// SplitWorktreeKey separates an item id from the attempt suffix of a
// worktree key ("I-42~b" is the second attempt of I-42).
func SplitWorktreeKey(key string) (itemID, attempt string) {
	id, att, _ := strings.Cut(key, "~")
	return id, att
}

// ItemSlug is the short, lowercase identity of a worktree key that fits
// in a database, container or compose name: "I-42" -> "i42", "I-42~b" -> "i42b".
func ItemSlug(key string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(key) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ComposeProjectName is the docker compose project of a worktree key:
// project name and item short id, in the characters compose accepts.
func ComposeProjectName(project, key string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(project) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-_")
	if name == "" {
		name = "barnraise"
	}
	return name + "-" + ItemSlug(key)
}

// ItemPortRange is the first port and the number of ports of an item: the
// item's number picks one of the slots of size ports above base. With size
// 1 it is base + number%1000, the historic per-item port.
func ItemPortRange(base, size int, key string) (int, int) {
	if size < 1 {
		size = 1
	}
	id, _ := SplitWorktreeKey(key)
	_, n, err := ParseID(id)
	if err != nil {
		return base, size
	}
	slots := 1000
	if base+slots*size > 65536 {
		slots = (65536 - base) / size
	}
	if slots < 1 {
		slots = 1
	}
	return base + (n%slots)*size, size
}

// Identity is the isolation of one worktree key.
func (o WorktreeOpts) Identity(project, key string, portBase int, dir string) IsolationVars {
	first, n := ItemPortRange(portBase, o.PortRange, key)
	ports := make([]int, n)
	for i := range ports {
		ports[i] = first + i
	}
	id, _ := SplitWorktreeKey(key)
	return IsolationVars{Project: project, Item: ItemSlug(key), ItemID: id, Compose: ComposeProjectName(project, key), Dir: dir, Port: first, Ports: ports}
}

// BaseEnv is the environment every process of an item worktree gets,
// whatever the configuration: its port, its port range and its compose project.
func (v IsolationVars) BaseEnv() map[string]string {
	env := map[string]string{
		"PORT":                 strconv.Itoa(v.Port),
		"COMPOSE_PROJECT_NAME": v.Compose,
		"BARNRAISE_ITEM_SLUG":  v.Item,
	}
	if len(v.Ports) > 0 {
		env["BARNRAISE_PORTS"] = fmt.Sprintf("%d-%d", v.Ports[0], v.Ports[len(v.Ports)-1])
	}
	return env
}

// Render renders the configured variables over the base environment. files
// are the variables of the worktree's included env files, read by the
// template function env.
func (w WorktreeIsolation) Render(v IsolationVars, files map[string]string) (map[string]string, error) {
	env := v.BaseEnv()
	for _, name := range sortedNames(w.Env) {
		val, err := renderIsolation(name, w.Env[name], v, files)
		if err != nil {
			return nil, err
		}
		env[name] = val
	}
	return env, nil
}

func sortedNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func renderIsolation(name, text string, v IsolationVars, files map[string]string) (string, error) {
	funcs := template.FuncMap{
		// env reads a variable of the included env files (.env): the value
		// the worktree would have had, to rewrite.
		"env": func(k string) string { return files[k] },
		// replace old new s, for pipes: {{env "DATABASE_URL" | replace "/app" "/app_i42"}}.
		"replace": func(old, repl, s string) string { return strings.ReplaceAll(s, old, repl) },
		// dbURL points a database URL at another database name.
		"dbURL": DatabaseURLWithName,
	}
	t, err := template.New(name).Option("missingkey=error").Funcs(funcs).Parse(text)
	if err != nil {
		return "", fmt.Errorf("worktree.isolate.env.%s: %w", name, err)
	}
	var b bytes.Buffer
	if err := t.Execute(&b, v); err != nil {
		return "", fmt.Errorf("worktree.isolate.env.%s: %w", name, err)
	}
	return b.String(), nil
}

// DatabaseURLWithName replaces the database of a URL (its path) with name,
// keeping the rest: postgres://u@h:5432/app?sslmode=disable with app_i42
// gives postgres://u@h:5432/app_i42?sslmode=disable. Something that is not
// a URL with a scheme is returned as name.
func DatabaseURLWithName(raw, name string) string {
	scheme, rest, ok := strings.Cut(raw, ":")
	if !ok || !urlScheme.MatchString(scheme) {
		return name
	}
	// The query and fragment stay as they are.
	tail := ""
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest, tail = rest[:i], rest[i:]
	}
	if auth, ok := strings.CutPrefix(rest, "//"); ok {
		host, _, _ := strings.Cut(auth, "/")
		return scheme + "://" + host + "/" + name + tail
	}
	// scheme:path (sqlite:data/app.db): the file's name, its extension kept.
	dir, ext := path.Dir(rest), path.Ext(rest)
	if dir == "." {
		return scheme + ":" + name + ext + tail
	}
	return scheme + ":" + path.Join(dir, name+ext) + tail
}

var urlScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*$`)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// isolationReserved are names an isolation template may not set: Barnraise's own,
// the ones the base environment already sets, and those that change how
// every program runs or where its traffic goes.
func isolationReserved(name string) bool {
	u := strings.ToUpper(name)
	switch u {
	case "PORT", "COMPOSE_PROJECT_NAME", "PATH", "HOME", "USER", "SHELL", "TMPDIR", "NO_PROXY", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY":
		return true
	}
	return strings.HasPrefix(u, "BARNRAISE_") || strings.HasPrefix(u, "LD_") || strings.HasPrefix(u, "DYLD_")
}

// Validate reports a configuration error of the worktree options.
func (o WorktreeOpts) Validate() error {
	var errs []error
	if o.PortRange < 0 || o.PortRange > 100 {
		errs = append(errs, fmt.Errorf("worktree.port_range must be between 1 and 100, not %d", o.PortRange))
	}
	switch o.Isolate.Compose {
	case "", "auto", "on", "off":
	default:
		errs = append(errs, fmt.Errorf("worktree.isolate.compose must be auto, on or off, not %q", o.Isolate.Compose))
	}
	sample := o.Identity("project", "I-1", 4100, "/worktree")
	for _, name := range sortedNames(o.Isolate.Env) {
		if !envName.MatchString(name) {
			errs = append(errs, fmt.Errorf("worktree.isolate.env: %q is not a variable name", name))
			continue
		}
		if isolationReserved(name) {
			errs = append(errs, fmt.Errorf("worktree.isolate.env: %s is set by Barnraise or changes every program, it cannot be set here", name))
			continue
		}
		if _, err := renderIsolation(name, o.Isolate.Env[name], sample, nil); err != nil {
			errs = append(errs, err)
		}
	}
	seen := map[string]bool{}
	for _, d := range o.BuildCache {
		clean, err := CleanBuildCacheDir(d)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if seen[clean] {
			errs = append(errs, fmt.Errorf("worktree.build_cache: %s is listed twice", clean))
		}
		seen[clean] = true
	}
	return errors.Join(errs...)
}

// CleanBuildCacheDir is a build cache directory relative to the
// repository, in slash form, or an error when it points outside it, at the
// whole worktree or at what git or Barnraise keep there.
func CleanBuildCacheDir(d string) (string, error) {
	raw := strings.TrimSpace(strings.ReplaceAll(d, "\\", "/"))
	clean := path.Clean(raw)
	switch {
	case raw == "", clean == ".", clean == "/":
		return "", fmt.Errorf("worktree.build_cache: %q is not a directory inside the repository", d)
	case path.IsAbs(clean), clean == "..", strings.HasPrefix(clean, "../"):
		return "", fmt.Errorf("worktree.build_cache: %q must be relative to the repository and inside it", d)
	}
	first, _, _ := strings.Cut(clean, "/")
	if first == ".git" || first == ".barnraise-session" {
		return "", fmt.Errorf("worktree.build_cache: %q is kept by git or Barnraise", d)
	}
	return clean, nil
}
