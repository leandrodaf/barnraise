package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/leandrodaf/barnraise/internal/daemoncfg"
	"github.com/leandrodaf/barnraise/internal/paths"
)

// App is what the launcher page calls. Every answer is a plain struct; the
// page then opens the daemon's own panel, so the data never goes through here.
type App struct {
	ctx context.Context
}

func NewApp() *App { return &App{} }

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// Local is what the app knows about this machine's daemon.
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
	// AppVersion is this app's build; Version above is the daemon's.
	AppVersion string `json:"app_version"`
}

// Status looks for the daemon of this machine.
func (a *App) Status() Local {
	l := Local{AppVersion: appVersion}
	l.Binary = findBinary()
	l.Service = serviceInstalled()
	df, err := daemoncfg.Load()
	if err != nil {
		l.Problem = err.Error()
		return l
	}
	if df.Listen == "" || df.Listen == "off" {
		l.Problem = "the daemon has no TCP address (listen: off in " + paths.DaemonConfig() + ")"
		return l
	}
	l.URL = "http://" + loopback(df.Listen)
	tok, _ := os.ReadFile(paths.OwnerTokenFile())
	l.HasToken = len(strings.TrimSpace(string(tok))) > 0
	h, err := health(l.URL)
	if err != nil {
		return l
	}
	l.Running, l.Machine, l.Version = true, h.Machine, h.Version
	if l.HasToken {
		l.PanelURL, l.DevServer = panelURL(l.URL, strings.TrimSpace(string(tok)))
	}
	return l
}

// StartDaemon starts this machine's daemon as its own process: through the
// systemd user service when there is one, otherwise `barnraise daemon` in
// the background with its log in the data folder. It waits until it answers.
func (a *App) StartDaemon() (Local, error) {
	if s := a.Status(); s.Running {
		return s, nil
	}
	switch {
	case serviceInstalled():
		if out, err := exec.Command("systemctl", "--user", "start", "barnraise").CombinedOutput(); err != nil {
			return Local{}, fmt.Errorf("systemctl --user start barnraise: %v: %s", err, out)
		}
	default:
		bin := findBinary()
		if bin == "" {
			return Local{}, errors.New("the barnraise program was not found next to the app, on the PATH or in ~/.local/bin")
		}
		if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
			return Local{}, err
		}
		logf, err := os.OpenFile(filepath.Join(paths.DataDir(), "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return Local{}, err
		}
		defer logf.Close()
		cmd := exec.Command(bin, "daemon")
		cmd.Stdout, cmd.Stderr = logf, logf
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // its own session: it outlives the app
		if err := cmd.Start(); err != nil {
			return Local{}, err
		}
		_ = cmd.Process.Release()
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if s := a.Status(); s.Running {
			return s, nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return Local{}, errors.New("the daemon did not answer in 20 seconds; see " + filepath.Join(paths.DataDir(), "daemon.log"))
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

// findBinary looks next to the app first (the installer puts it there), then
// on the PATH, then in ~/.local/bin.
func findBinary() string {
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "barnraise")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath("barnraise"); err == nil {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".local", "bin", "barnraise")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func serviceInstalled() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	return exec.Command("systemctl", "--user", "cat", "barnraise").Run() == nil
}
