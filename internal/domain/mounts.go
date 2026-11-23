package domain

import (
	"path"
	"strings"
)

// ForbiddenMount says why a host path must never be mounted into an agent's
// container, or "" when it may be. home is the owner's home ("" when not
// known yet, as in barnraise check: then "~/" paths are checked as given).
//
// The engine's socket is the worst case: whoever reaches it runs
// `docker run -v /:/host` and owns the machine, past every other layer
// (ai-jail issue #88, fixed in 1.16 by making it opt-in). The others hold
// what problem 5 of the design is about: keys, tokens and the owner's files.
func ForbiddenMount(p, home string) string {
	p = strings.TrimSuffix(strings.TrimSpace(p), ":ro")
	if rest, ok := strings.CutPrefix(p, "~/"); ok && home != "" {
		p = path.Join(home, rest)
	} else if p == "~" && home != "" {
		p = home
	}
	clean := path.Clean(p)
	base := path.Base(clean)
	if strings.HasSuffix(base, ".sock") && (strings.Contains(base, "docker") || strings.Contains(base, "podman") || strings.Contains(base, "containerd")) {
		return "the container engine's socket gives whoever reaches it root on the machine"
	}
	switch clean {
	case "/", "/home", "/root", "/etc", "/var", "/run", "/var/run", "/proc", "/sys", "/dev", "/boot", "/usr", "~", ".":
		return "a system folder or the whole disk"
	}
	if home != "" && clean == path.Clean(home) {
		return "the owner's whole home"
	}
	if strings.HasPrefix(clean, "/home/") && strings.Count(clean, "/") == 2 {
		return "a whole home folder"
	}
	rel := ""
	if home != "" {
		if r, ok := strings.CutPrefix(clean, path.Clean(home)+"/"); ok {
			rel = r
		}
	}
	if r, ok := strings.CutPrefix(clean, "~/"); ok {
		rel = r
	}
	if rel == "" {
		return ""
	}
	for _, secret := range secretHomePaths {
		if rel == secret || strings.HasPrefix(rel, secret+"/") || strings.HasPrefix(secret, rel+"/") {
			return "~/" + secret + " holds keys or tokens"
		}
	}
	return ""
}

// secretHomePaths are folders of a home that hold credentials. Mounting one
// of them, or a folder that contains one, is refused.
var secretHomePaths = []string{
	".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker", ".config", ".password-store",
	".local/share/keyrings", ".netrc", ".git-credentials", ".npmrc", ".pypirc", ".vault-token",
	".mozilla", ".thunderbird",
}
