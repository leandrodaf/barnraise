package outfilter

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// DelegateFromEnv is the session's outfilter.delegate (auto when unset).
func DelegateFromEnv() string {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv(domain.EnvOutfilterDelegate))); domain.ValidDelegate(v) {
		return v
	}
	return domain.DelegateAuto
}

// LookRTK returns the path of the rtk binary, or "" when it is not installed.
func LookRTK() string {
	p, err := exec.LookPath("rtk")
	if err != nil {
		return ""
	}
	return p
}

// neverDelegated are commands whose output is the point (or is nothing):
// they run as they are.
var neverDelegated = map[string]bool{
	"barnraise": true, "rtk": true, "cd": true, "echo": true, "printf": true, "true": true, "false": true,
	"export": true, "sleep": true, "test": true, "[": true, "mkdir": true, "rm": true, "cp": true, "mv": true,
	"touch": true, "chmod": true, "ln": true, "sh": true, "bash": true, "zsh": true, "env": true, "pwd": true,
}

// Delegable reports whether a command may go to rtk in this mode: rtk is
// installed, the command is simple (no shell syntax or quotes, so its words
// are its arguments), and either it has no native filter (auto) or the
// owner chose rtk for everything it handles (rtk).
func Delegable(mode, tool, command string) bool {
	if mode != domain.DelegateAuto && mode != domain.DelegateRTK {
		return false
	}
	if mode == domain.DelegateAuto && tool != ToolGeneric {
		return false
	}
	if strings.ContainsAny(command, "|;&><`$\"'\\(){}*?~") {
		return false
	}
	f := strings.Fields(command)
	i := firstWord(f)
	if i != 0 || len(f) == 0 {
		return false // VAR=value prefixes are shell syntax for rtk
	}
	return !neverDelegated[filepath.Base(f[0])]
}

// rtkFor returns the rtk binary to run command through, or "".
func (o Options) rtkFor(tool, command string) string {
	if o.Raw || !Delegable(o.Delegate, tool, command) {
		return ""
	}
	if o.RTK != "" {
		return o.RTK
	}
	return LookRTK()
}

// rtkRefused reports whether rtk rejected the command line itself (an
// unknown subcommand: a usage error before anything ran).
func rtkRefused(k *keptOutput) bool {
	if k.exit != 2 || k.total > 8<<10 {
		return false
	}
	s := strings.ToLower(string(k.tail.Bytes()))
	for _, sig := range []string{"unrecognized subcommand", "unexpected argument", "usage: rtk", "unknown command", "no such subcommand"} {
		if strings.Contains(s, sig) {
			return true
		}
	}
	return false
}
