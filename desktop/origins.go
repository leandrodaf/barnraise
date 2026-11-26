package main

import (
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/leandrodaf/barnraise/internal/paths"
)

// The panel a daemon serves runs in this window under the daemon's own
// origin, not the app's. Wails takes messages from a page (to open a link
// in the system's browser, for one) only from the origins it allows: this
// machine's daemon on any port, and each other machine's daemon this app
// connected to before, kept in a file since the list is read at start.

// loopbackOrigins are this machine's daemon, whatever its port.
const loopbackOrigins = "http://127.0.0.1:*,http://localhost:*"

func remotesFile() string { return filepath.Join(paths.ConfigDir(), "desktop-remotes") }

// allowedOrigins is the list Wails takes as BindingsAllowedOrigins.
func allowedOrigins() string {
	out := []string{loopbackOrigins}
	if b, err := os.ReadFile(remotesFile()); err == nil {
		for line := range strings.SplitSeq(string(b), "\n") {
			if o := strings.TrimSpace(line); o != "" && !strings.ContainsAny(o, ", *") {
				out = append(out, o)
			}
		}
	}
	return strings.Join(out, ",")
}

// rememberRemote keeps the origin of another machine's daemon, so its panel
// may open links from the next start of the app on.
func rememberRemote(base *url.URL) {
	origin := base.Scheme + "://" + base.Host
	b, _ := os.ReadFile(remotesFile())
	lines := strings.Fields(string(b))
	if slices.Contains(lines, origin) {
		return
	}
	if err := os.MkdirAll(filepath.Dir(remotesFile()), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(remotesFile(), []byte(strings.Join(append(lines, origin), "\n")+"\n"), 0o600)
}
