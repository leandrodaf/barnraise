package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"
)

// App is what the launcher page calls. Every answer is a plain struct; the
// page then opens the daemon's own panel, so the data never goes through here.
type App struct {
	ctx context.Context
}

func NewApp() *App { return &App{} }

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// Local is what the app knows about this machine's daemon. On Windows the
// daemon runs in WSL (it is a Linux program): Host is then "wsl" and Distro
// the Linux distribution it lives in.
type Local struct {
	Running   bool   `json:"running"`
	URL       string `json:"url"`
	Machine   string `json:"machine"`
	Version   string `json:"version"`
	Binary    string `json:"binary"`  // the barnraise program found, "" when none
	Service   bool   `json:"service"` // a systemd user service is installed
	HasToken  bool   `json:"has_token"`
	Problem   string `json:"problem,omitempty"`
	PanelURL  string `json:"panel_url,omitempty"` // with the token, when running
	DevServer bool   `json:"dev_server"`
	// CanInstall: the program is missing and the app can install it.
	CanInstall bool `json:"can_install"`
	// Windows only: the daemon's home is WSL.
	Host     string `json:"host,omitempty"`   // "wsl" on Windows
	Distro   string `json:"distro,omitempty"` // the WSL distribution used
	NeedsWSL bool   `json:"needs_wsl"`        // WSL or a distribution is missing
	// AppVersion is this app's build; Version above is the daemon's.
	AppVersion string `json:"app_version"`
	Platform   string `json:"platform"`
}

// Status looks for the daemon of this machine (on Windows: the one in WSL).
func (a *App) Status() Local {
	l := localStatus()
	l.AppVersion, l.Platform = appVersion, runtime.GOOS
	return l
}

// startWait is how long StartDaemon waits for the daemon to answer.
const startWait = 90 * time.Second

// StartDaemon starts this machine's daemon as its own process and waits
// until it answers with its panel. Closing the app does not stop it.
func (a *App) StartDaemon() (Local, error) {
	// One that already answers is never started twice, token or not.
	if s := a.Status(); s.Running {
		return s, nil
	}
	if err := localStart(); err != nil {
		return a.Status(), err
	}
	// The daemon answers only after it loaded every project: a large one
	// takes ten seconds or more, so the wait is generous. It writes the
	// owner token as it starts, so the panel address follows the answer.
	deadline := time.Now().Add(startWait)
	for time.Now().Before(deadline) {
		if s := a.Status(); s.Running && s.PanelURL != "" {
			return s, nil
		}
		time.Sleep(time.Second)
	}
	if s := a.Status(); s.Running {
		return s, nil
	}
	return a.Status(), fmt.Errorf("the daemon did not answer in %s. Its last lines:\n%s", startWait, localLogTail())
}

// Install puts the barnraise program on this machine (on Windows: in WSL)
// with the project's install.sh, from the latest release.
func (a *App) Install() (Local, error) {
	out, err := localInstall()
	if err != nil {
		return a.Status(), fmt.Errorf("%v\n%s", err, lastLines(out, 12))
	}
	return a.Status(), nil
}

// InstallWSL asks Windows to install WSL with its default distribution
// (Windows asks the owner for administrator rights). Elsewhere it is a no-op.
func (a *App) InstallWSL() error { return localInstallWSL() }

// installScript is the one-line install the README documents.
const installScript = "curl -fsSL --proto '=https' --tlsv1.2 https://raw.githubusercontent.com/leandrodaf/barnraise/main/install.sh | sh"

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// Remote checks another machine's daemon and returns its panel address.
func (a *App) Remote(base, token string) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(base), "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("the address must look like https://machine:7420")
	}
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		return "", errors.New("another machine is reached over https only: the token would travel in the clear")
	}
	if _, err := health(u.String()); err != nil {
		return "", fmt.Errorf("%s does not answer as a Barnraise daemon: %v", u, err)
	}
	rememberRemote(u)
	p, _ := panelURL(u.String(), strings.TrimSpace(token))
	return p, nil
}

type healthz struct {
	Machine string `json:"machine"`
	Version string `json:"version"`
}

func health(base string) (healthz, error) {
	var h healthz
	c := &http.Client{Timeout: 2 * time.Second}
	res, err := c.Get(base + "/healthz")
	if err != nil {
		return h, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return h, fmt.Errorf("status %d", res.StatusCode)
	}
	return h, decodeJSON(res, &h)
}

// panelURL is the daemon's panel with the token; BARNRAISE_PANEL_DEV points
// it at the panel's dev server instead (pnpm dev in web/), which talks to the
// same daemon.
func panelURL(base, token string) (string, bool) {
	dev := os.Getenv("BARNRAISE_PANEL_DEV")
	if dev != "" {
		base = strings.TrimRight(dev, "/")
	}
	if token == "" {
		return base + "/", dev != ""
	}
	return base + "/?token=" + url.QueryEscape(token), dev != ""
}

func loopback(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
