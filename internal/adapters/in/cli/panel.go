package cli

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/leandrodaf/barnraise/internal/daemoncfg"
	"github.com/leandrodaf/barnraise/internal/paths"
)

// panelAddress is the panel of this machine's daemon, with the owner's token
// so the browser is signed in at once; the panel takes the token out of the
// address bar and the history as soon as it loads.
func panelAddress() (string, error) {
	df, err := daemoncfg.Load()
	if err != nil {
		return "", err
	}
	if df.Listen == "" || df.Listen == "off" {
		return "", fmt.Errorf("the daemon has no web address (listen: off in %s)", paths.DaemonConfig())
	}
	host, port, err := net.SplitHostPort(df.Listen)
	if err != nil {
		return "", fmt.Errorf("listen %q in %s: %w", df.Listen, paths.DaemonConfig(), err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	tok, err := os.ReadFile(paths.OwnerTokenFile())
	if err != nil || strings.TrimSpace(string(tok)) == "" {
		return "", errors.New("no owner token yet: start the daemon once with `barnraise up`")
	}
	return "http://" + net.JoinHostPort(host, port) + "/?token=" + url.QueryEscape(strings.TrimSpace(string(tok))), nil
}

func (e *env) panelCmd() *cobra.Command {
	var print bool
	c := &cobra.Command{Use: "panel", Short: "Open the web panel in the browser, already signed in", Args: cobra.NoArgs,
		Long: "Opens this machine's panel in the default browser with the owner's token, so there is nothing to paste.\n" +
			"--print writes the address instead (it holds the token: do not share it).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			u, err := panelAddress()
			if err != nil {
				return err
			}
			if print {
				fmt.Fprintln(e.out, u)
				return nil
			}
			if err := openBrowser(u); err != nil {
				return fmt.Errorf("could not open the browser (%v): run `barnraise panel --print` and open the address yourself", err)
			}
			fmt.Fprintln(e.out, "✔ The panel is open in the browser.")
			return nil
		}}
	c.Flags().BoolVar(&print, "print", false, "write the address (with the token) instead of opening it")
	return c
}

// openBrowser opens u in the owner's browser. In WSL that is the Windows
// one: wslview when wslu is there, else explorer.exe, which hands an address
// to the default browser.
func openBrowser(u string) error {
	var name string
	switch {
	case runtime.GOOS == "darwin":
		name = "open"
	case inWSL():
		name = "explorer.exe"
		if _, err := exec.LookPath("wslview"); err == nil {
			name = "wslview"
		}
	default:
		name = "xdg-open"
	}
	return exec.Command(name, u).Start()
}

var inWSL = sync.OnceValue(func() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" || os.Getenv("WSL_INTEROP") != "" {
		return true
	}
	b, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	return strings.Contains(strings.ToLower(string(b)), "microsoft")
})
