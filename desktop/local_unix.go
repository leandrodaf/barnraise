//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/leandrodaf/barnraise/internal/daemoncfg"
	"github.com/leandrodaf/barnraise/internal/paths"
)

// On Linux and macOS the daemon is a process of this machine: its config,
// token and log are files the app reads directly.

func localStatus() Local {
	var l Local
	l.Binary = findBinary()
	l.Service = serviceInstalled()
	l.CanInstall = l.Binary == "" && hasDownloader()
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

// localStart starts the daemon through the systemd user service when there
// is one, otherwise `barnraise daemon` in its own session with its log in
// the data folder, so it outlives the app.
func localStart() error {
	if serviceInstalled() {
		if out, err := exec.Command("systemctl", "--user", "start", "barnraise").CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl --user start barnraise: %v: %s", err, out)
		}
		return nil
	}
	bin := findBinary()
	if bin == "" {
		return errors.New("the barnraise program was not found next to the app, on the PATH or in ~/.local/bin")
	}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(daemonLog(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(bin, "daemon")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // its own session: it outlives the app
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func daemonLog() string { return filepath.Join(paths.DataDir(), "daemon.log") }

func localLogTail() string {
	if serviceInstalled() {
		out, _ := exec.Command("journalctl", "--user", "-u", "barnraise", "-n", "15", "--no-pager", "-o", "cat").CombinedOutput()
		return lastLines(string(out), 15)
	}
	b, err := os.ReadFile(daemonLog())
	if err != nil {
		return "(no log at " + daemonLog() + ")"
	}
	return lastLines(string(b), 15)
}

func localInstall() (string, error) {
	out, err := exec.Command("sh", "-c", installScript).CombinedOutput()
	return string(out), err
}

func localInstallWSL() error { return errors.New("WSL is a Windows feature") }

func hasDownloader() bool {
	for _, n := range []string{"curl", "wget"} {
		if _, err := exec.LookPath(n); err == nil {
			return true
		}
	}
	return false
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
