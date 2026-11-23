package provcommon

import (
	"os"
	"sort"
	"strings"
)

// BaseEnvKeys are inherited from the daemon's environment when set; the rest is dropped.
var BaseEnvKeys = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LC_ALL", "LC_CTYPE", "TERM", "TZ",
	"TMPDIR", "XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
}

// identityKeys belong to the user that runs the process; under sudo the target user's apply.
var identityKeys = map[string]bool{"HOME": true, "USER": true, "LOGNAME": true, "PATH": true, "SHELL": true, "TMPDIR": true,
	"XDG_RUNTIME_DIR": true, "XDG_CONFIG_HOME": true, "XDG_CACHE_HOME": true, "XDG_DATA_HOME": true, "XDG_STATE_HOME": true}

// Env is an ordered set of environment variables.
type Env struct {
	keys []string
	vals map[string]string
}

// MinimalEnv starts from the allowed keys of the current environment plus extra key names.
func MinimalEnv(extraKeys ...string) *Env {
	e := &Env{vals: map[string]string{}}
	for _, k := range append(append([]string(nil), BaseEnvKeys...), extraKeys...) {
		if v, ok := os.LookupEnv(k); ok {
			e.Set(k, v)
		}
	}
	return e
}

// Set adds or replaces a variable.
func (e *Env) Set(k, v string) {
	if e.vals == nil {
		e.vals = map[string]string{}
	}
	if _, ok := e.vals[k]; !ok {
		e.keys = append(e.keys, k)
	}
	e.vals[k] = v
}

// Merge sets every pair of m, in key order.
func (e *Env) Merge(m map[string]string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		e.Set(k, m[k])
	}
}

// Get returns a value.
func (e *Env) Get(k string) (string, bool) {
	v, ok := e.vals[k]
	return v, ok
}

// List returns KEY=VALUE pairs for exec.Cmd.Env.
func (e *Env) List() []string {
	out := make([]string, 0, len(e.keys))
	for _, k := range e.keys {
		out = append(out, k+"="+e.vals[k])
	}
	return out
}

// SudoArgs wraps name and args to run as another Linux user with sudo -n, keeping
// the variables of env that do not describe the calling user. It returns the new
// program and arguments.
func SudoArgs(user string, env *Env, name string, args []string) (string, []string) {
	var keep []string
	for _, k := range env.keys {
		if !identityKeys[k] {
			keep = append(keep, k)
		}
	}
	out := []string{"-n", "-u", user}
	if len(keep) > 0 {
		out = append(out, "--preserve-env="+strings.Join(keep, ","))
	}
	out = append(out, "--", name)
	out = append(out, args...)
	return "sudo", out
}
