package tailnet

import (
	"os"
	"os/exec"
	"path/filepath"
)

// cliName is the default name of the Tailscale command.
const cliName = "tailscale"

// cliPaths are the places the Tailscale installers put the command when
// it may not be on the PATH:
//
//   - macOS: the App Store and standalone apps ship the command inside the
//     app bundle and put nothing on the PATH; Homebrew uses /opt/homebrew
//     (Apple silicon) or /usr/local (Intel), which a daemon started by
//     launchd or by a desktop app does not have on its PATH.
//   - Linux: the packages use /usr/bin, the snap /snap/bin.
var cliPaths = []string{
	"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
	"~/Applications/Tailscale.app/Contents/MacOS/Tailscale",
	"/opt/homebrew/bin/tailscale",
	"/usr/local/bin/tailscale",
	"/usr/bin/tailscale",
	"/usr/sbin/tailscale",
	"/snap/bin/tailscale",
}

// LookCLI finds the Tailscale command: bin on the PATH (or bin itself
// when it is a path), then, for the default name, the places the
// installers put it (cliPaths). It returns exec.LookPath's error when
// nothing is found.
func LookCLI(bin string) (string, error) {
	return lookCLI(bin, exec.LookPath, cliPaths)
}

func lookCLI(bin string, look func(string) (string, error), known []string) (string, error) {
	if bin == "" {
		bin = cliName
	}
	p, err := look(bin)
	if err == nil || bin != cliName {
		return p, err
	}
	home, _ := os.UserHomeDir()
	for _, c := range known {
		if rest, ok := cutHome(c); ok {
			if home == "" {
				continue
			}
			c = filepath.Join(home, rest)
		}
		if executable(c) {
			return c, nil
		}
	}
	return "", err
}

func cutHome(p string) (string, bool) {
	if len(p) >= 2 && p[:2] == "~/" {
		return p[2:], true
	}
	return "", false
}

// executable reports whether p is a regular file someone may execute.
func executable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}
