package provcommon

import (
	"path/filepath"
	"strings"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// hostOnlyKeys describe the host, not the container: they are not passed in.
var hostOnlyKeys = map[string]bool{"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true, "TMPDIR": true,
	"XDG_RUNTIME_DIR": true, "XDG_CONFIG_HOME": true, "XDG_CACHE_HOME": true, "XDG_DATA_HOME": true, "XDG_STATE_HOME": true}

// ContainerArgs wraps name and args to run inside a container. Every path
// of the spec is mounted at the same path, so the files Barnraise wrote for
// the session (settings, MCP config, policy) and the hooks keep working
// unchanged. The variables of env are passed by name: their values travel
// in the engine's own environment, never on the command line.
func ContainerArgs(c *ports.ContainerSpec, env *Env, workdir, name string, args []string) (string, []string) {
	engine := c.Engine
	if engine == "" {
		engine = "docker"
	}
	out := []string{"run", "--rm", "-i", "--init", "--workdir", workdir}
	out = append(out, Hardening...)
	if c.Network != "" {
		out = append(out, "--network", c.Network)
	}
	if c.User != "" {
		out = append(out, "--user", c.User)
	}
	for _, m := range c.Mounts {
		path, mode := m, ""
		if p, ok := strings.CutSuffix(m, ":ro"); ok {
			path, mode = p, ":ro"
		}
		out = append(out, "--volume", path+":"+path+mode)
	}
	out = append(out, c.Extra...)
	if c.Home != "" {
		out = append(out, "--env", "HOME="+c.Home)
	}
	for _, k := range env.keys {
		if !hostOnlyKeys[k] {
			out = append(out, "--env", k)
		}
	}
	// Inside the container the CLI is found on the image's PATH.
	out = append(out, c.Image, filepath.Base(name))
	return engine, append(out, args...)
}

// Hardening is what every agent container runs with, on top of the engine's
// default seccomp profile: no Linux capabilities and no way to gain any
// (setuid binaries do nothing), the image read-only with scratch folders in
// memory, a cap on processes and open files, no core dumps, and a hostname
// that is not the machine's. The same layers ai-jail puts around an agent
// (rlimits, read-only /usr and /etc, tmpfs /tmp), in engine flags.
var Hardening = []string{
	"--cap-drop", "ALL",
	"--security-opt", "no-new-privileges",
	"--read-only",
	"--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=8g",
	"--tmpfs", "/var/tmp:rw,nosuid,nodev,size=1g",
	"--tmpfs", "/run:rw,nosuid,nodev,size=64m",
	"--pids-limit", "4096",
	"--ulimit", "nofile=65536:65536",
	"--ulimit", "core=0",
	"--hostname", "barnraise-agent",
}
