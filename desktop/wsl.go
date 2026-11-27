package main

import (
	"bytes"
	"errors"
	"strings"
)

// The daemon is a Linux program. On Windows it lives in WSL, and the app
// (a native Windows program) drives it through wsl.exe: it reads the
// daemon's address and owner token from the distribution's own files,
// starts it there, and reaches its panel through the localhost forwarding
// WSL gives Windows. This file holds the parts that need no Windows, so they
// are tested everywhere; local_windows.go runs them.

// wslDistro is one line of `wsl.exe --list --verbose`.
type wslDistro struct {
	Name    string
	Default bool
	Version string
}

// decodeWSL turns wsl.exe's own output into text: without WSL_UTF8 it
// writes UTF-16LE, which reads as ASCII with a NUL after every letter.
func decodeWSL(b []byte) string {
	b = bytes.TrimPrefix(b, []byte{0xff, 0xfe})
	if bytes.IndexByte(b, 0) >= 0 {
		b = bytes.ReplaceAll(b, []byte{0}, nil)
	}
	return strings.ReplaceAll(string(b), "\r", "")
}

// parseDistros reads `wsl.exe --list --verbose`: a header line (in the
// system's language), then one line per distribution, the default one
// marked with an asterisk.
func parseDistros(out string) []wslDistro {
	var ds []wslDistro
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if i == 0 {
			continue // NAME STATE VERSION, translated
		}
		line = strings.TrimSpace(line)
		def := strings.HasPrefix(line, "*")
		f := strings.Fields(strings.TrimPrefix(line, "*"))
		if len(f) == 0 {
			continue
		}
		d := wslDistro{Name: f[0], Default: def}
		if len(f) > 2 {
			d.Version = f[len(f)-1]
		}
		ds = append(ds, d)
	}
	return ds
}

// helperDistro tells the distributions other tools install for themselves:
// nobody works in them, so the daemon never goes there.
func helperDistro(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "docker-desktop") || strings.HasPrefix(n, "rancher-desktop") || strings.HasPrefix(n, "podman-machine")
}

// errNoDistro: WSL is there without a Linux distribution to run in.
var errNoDistro = errors.New("WSL has no Linux distribution yet")

// pickDistro is the distribution the daemon lives in: the one asked for,
// else the default one, else the first that is not a helper.
func pickDistro(ds []wslDistro, want string) (string, error) {
	if want != "" {
		for _, d := range ds {
			if strings.EqualFold(d.Name, want) {
				return d.Name, nil
			}
		}
		return "", errors.New("the WSL distribution " + want + " (BARNRAISE_WSL_DISTRO) is not installed")
	}
	for _, d := range ds {
		if d.Default && !helperDistro(d.Name) {
			return d.Name, nil
		}
	}
	for _, d := range ds {
		if !helperDistro(d.Name) {
			return d.Name, nil
		}
	}
	return "", errNoDistro
}

// wslFind sets b to the barnraise program in the distribution: on the
// login PATH, else where install.sh puts it.
const wslFind = `b=$(command -v barnraise 2>/dev/null); if [ -z "$b" ] && [ -x "$HOME/.local/bin/barnraise" ]; then b="$HOME/.local/bin/barnraise"; fi; `

// wslDirs sets c and d to the daemon's config and data folders, as
// internal/paths resolves them on Linux.
const wslDirs = `c="${BARNRAISE_CONFIG_DIR:-${XDG_CONFIG_HOME:-$HOME/.config}/barnraise}"; d="${BARNRAISE_DATA_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/barnraise}"; `

// wslProbe prints, one key=value a line, what the app needs to know. It
// reads files instead of asking barnraise, so any version answers. The
// process pattern is written [b]arnraise so it never matches this script.
const wslProbe = wslFind + wslDirs +
	`echo "bin=$b"; echo "uid=$(id -u)"; ` +
	`if command -v systemctl >/dev/null 2>&1 && systemctl --user cat barnraise >/dev/null 2>&1; then echo service=1; fi; ` +
	`echo "listen=$(sed -n 's/^listen:[[:space:]]*//p' "$c/daemon.yaml" 2>/dev/null | head -n 1)"; ` +
	`echo "token=$(cat "$c/owner.token" 2>/dev/null)"; ` +
	`if pgrep -u "$(id -u)" -f '[b]arnraise daemon' >/dev/null 2>&1; then echo alive=1; fi; ` +
	`if command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1; then echo dl=1; fi`

// wslRunDaemon runs the daemon in the foreground of the wsl.exe that starts
// it: that process keeps the distribution up while the daemon runs (WSL
// shuts an idle distribution down), and it outlives the app.
const wslRunDaemon = wslFind + wslDirs +
	`[ -n "$b" ] || { echo "barnraise is not installed in WSL" >&2; exit 127; }; mkdir -p "$d"; exec "$b" daemon >>"$d/daemon.log" 2>&1`

// wslStartService starts the systemd user service; wslKeepService then
// keeps the distribution up for as long as the service runs.
const (
	wslStartService = `systemctl --user start barnraise`
	wslKeepService  = `while systemctl --user is-active -q barnraise; do sleep 30; done`
)

// wslLogTail prints the daemon's last lines, from the service or its file.
const wslLogTail = wslDirs +
	`if command -v systemctl >/dev/null 2>&1 && systemctl --user cat barnraise >/dev/null 2>&1; then journalctl --user -u barnraise -n 15 --no-pager -o cat 2>&1; else tail -n 15 "$d/daemon.log" 2>&1; fi`

// probe is what wslProbe printed.
type probe struct {
	Bin, Listen, Token string
	Root               bool
	Service, Alive, DL bool
}

func parseProbe(out string) probe {
	var p probe
	for line := range strings.SplitSeq(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimRight(line, "\r"), "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "bin":
			p.Bin = v
		case "uid":
			p.Root = v == "0"
		case "service":
			p.Service = v == "1"
		case "listen":
			p.Listen = strings.Trim(v, `"'`)
		case "token":
			p.Token = v
		case "alive":
			p.Alive = v == "1"
		case "dl":
			p.DL = v == "1"
		}
	}
	return p
}
