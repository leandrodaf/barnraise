//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// On Windows the daemon runs in WSL; see wsl.go for how the app drives it.

const (
	createNoWindow        = 0x08000000 // no console window flashes up
	createNewProcessGroup = 0x00000200 // the daemon's wsl.exe outlives the app
)

func wslExe() (string, error) {
	p, err := exec.LookPath("wsl.exe")
	if err != nil {
		return "", errors.New("WSL is not installed on this Windows")
	}
	return p, nil
}

func wslCmd(ctx context.Context, args ...string) (*exec.Cmd, error) {
	exe, err := wslExe()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Env = append(os.Environ(), "WSL_UTF8=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd, nil
}

// inDistro runs a sh script in the distribution as its default user, with a
// login shell so the PATH is the one the owner's terminal has.
func inDistro(distro string, timeout time.Duration, script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd, err := wslCmd(ctx, "-d", distro, "-e", "sh", "-lc", script)
	if err != nil {
		return "", err
	}
	out, err := cmd.CombinedOutput()
	return decodeWSL(out), err
}

func distro() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd, err := wslCmd(ctx, "--list", "--verbose")
	if err != nil {
		return "", err
	}
	out, err := cmd.Output()
	ds := parseDistros(decodeWSL(out))
	if err != nil && len(ds) == 0 {
		return "", errNoDistro
	}
	return pickDistro(ds, strings.TrimSpace(os.Getenv("BARNRAISE_WSL_DISTRO")))
}

func localStatus() Local {
	l := Local{Host: "wsl"}
	d, err := distro()
	if err != nil {
		l.NeedsWSL = true
		l.Problem = err.Error()
		return l
	}
	l.Distro = d
	// The first call boots WSL when it is down: that takes some seconds.
	out, err := inDistro(d, 60*time.Second, wslProbe)
	p := parseProbe(out)
	if err != nil && p.Bin == "" && !strings.Contains(out, "uid=") {
		l.Problem = fmt.Sprintf("WSL (%s) did not answer: %v %s", d, err, strings.TrimSpace(lastLines(out, 3)))
		return l
	}
	if p.Root {
		l.Problem = fmt.Sprintf("%s runs as root: open %s once from the Start menu to create your Linux user, then come back", d, d)
		return l
	}
	l.Binary, l.Service = p.Bin, p.Service
	l.CanInstall = p.Bin == "" && p.DL
	if p.Bin == "" && !p.DL {
		l.Problem = fmt.Sprintf("barnraise is not installed in %s, and it has neither curl nor wget to install it (sudo apt install curl)", d)
	}
	listen := p.Listen
	if listen == "" {
		listen = "127.0.0.1:7420"
	}
	if listen == "off" {
		l.Problem = "the daemon in WSL has no TCP address (listen: off in ~/.config/barnraise/daemon.yaml)"
		return l
	}
	l.URL = "http://" + loopback(listen)
	l.HasToken = p.Token != ""
	h, err := health(l.URL)
	if err != nil {
		if p.Alive {
			l.Problem = fmt.Sprintf("the daemon runs in %s, but Windows does not reach it at %s: turn localhostForwarding on (or networkingMode=mirrored) in %%UserProfile%%\\.wslconfig and run wsl --shutdown", d, l.URL)
		}
		return l
	}
	l.Running, l.Machine, l.Version = true, h.Machine, h.Version
	if l.HasToken {
		l.PanelURL, l.DevServer = panelURL(l.URL, p.Token)
	}
	return l
}

// localStart starts the daemon in WSL through its systemd user service when
// there is one, otherwise `barnraise daemon` itself. Either way a wsl.exe of
// its own, with no window and in its own process group, stays behind: it
// keeps the distribution up while the daemon runs and outlives the app.
func localStart() error {
	d, err := distro()
	if err != nil {
		return err
	}
	p := parseProbe(must(inDistro(d, 60*time.Second, wslProbe)))
	if p.Bin == "" && !p.Service {
		return fmt.Errorf("barnraise is not installed in %s", d)
	}
	script := wslRunDaemon
	if p.Service {
		if out, err := inDistro(d, 60*time.Second, wslStartService); err != nil {
			return fmt.Errorf("systemctl --user start barnraise in %s: %v: %s", d, err, strings.TrimSpace(out))
		}
		script = wslKeepService
	}
	cmd, err := wslCmd(context.Background(), "-d", d, "-e", "sh", "-lc", script)
	if err != nil {
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow | createNewProcessGroup}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func must(s string, _ error) string { return s }

func localLogTail() string {
	d, err := distro()
	if err != nil {
		return err.Error()
	}
	out, _ := inDistro(d, 30*time.Second, wslLogTail)
	return lastLines(out, 15)
}

func localInstall() (string, error) {
	d, err := distro()
	if err != nil {
		return "", err
	}
	return inDistro(d, 5*time.Minute, installScript)
}

// localInstallWSL asks for administrator rights and runs `wsl --install`,
// which adds WSL and Ubuntu; Windows then asks for a restart.
func localInstallWSL() error {
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		return errors.New("PowerShell was not found: run `wsl --install` in a terminal opened as administrator")
	}
	cmd := exec.Command(ps, "-NoProfile", "-WindowStyle", "Hidden", "-Command", "Start-Process -FilePath wsl.exe -ArgumentList '--install' -Verb RunAs")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("wsl --install: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
