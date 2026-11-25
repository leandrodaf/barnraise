package main

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// The daemon's temporary folder is /var/tmp/barnraise-<uid>: what sessions,
// the advisor and checks write there (Claude Code keeps its tool outputs in
// $TMPDIR/claude-<uid>) lands on the system disk, not on a small /tmp (often
// a RAM disk) that, once full, breaks every command of every session. The
// sandboxes open /var/tmp read-write, and every folder above it can be
// searched (Claude Code refuses a temporary folder it cannot reach, as one
// inside the data folder the sandbox hides). Without /var/tmp it is
// <data dir>/tmp.

// tempKeep is how long a temporary leftover is kept after its last change.
const tempKeep = 24 * time.Hour

// useDataTemp points TMPDIR at <data dir>/tmp and returns the folder the
// machine used before, to clean what earlier sessions left there, and how
// to put TMPDIR back when the daemon stops.
func useDataTemp(dataDir string) (before string, restore func()) {
	before = os.TempDir()
	old, had := os.LookupEnv("TMPDIR")
	restore = func() {
		if had {
			_ = os.Setenv("TMPDIR", old)
		} else {
			_ = os.Unsetenv("TMPDIR")
		}
	}
	dir := tempHome(dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return before, func() {}
	}
	_ = os.Chmod(dir, 0o700)
	_ = os.Setenv("TMPDIR", dir)
	return before, restore
}

// varTmp is the system's temporary folder kept across reboots.
var varTmp = "/var/tmp"

// tempHome is the daemon's temporary folder.
func tempHome(dataDir string) string {
	if fi, err := os.Stat(varTmp); err == nil && fi.IsDir() {
		return filepath.Join(varTmp, "barnraise-"+strconv.Itoa(os.Getuid()))
	}
	return filepath.Join(dataDir, "tmp")
}

// trimTemp removes, from each folder, the entries nothing changed in for
// longer than keep (the newest change anywhere inside counts).
func trimTemp(log *slog.Logger, now time.Time, keep time.Duration, dirs ...string) {
	for _, dir := range dirs {
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		removed := 0
		for _, e := range ents {
			p := filepath.Join(dir, e.Name())
			if now.Sub(newest(p)) > keep {
				if os.RemoveAll(p) == nil {
					removed++
				}
			}
		}
		if removed > 0 {
			log.Info("temporary leftovers removed", "dir", dir, "entries", removed)
		}
	}
}

// newest is the latest change of a file or of anything under a folder.
func newest(path string) time.Time {
	var t time.Time
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(t) {
			t = info.ModTime()
		}
		return nil
	})
	return t
}

// keepTempTrimmed trims the temporary folders at start and every hour: the
// daemon's own, and Claude Code's folder for this user in the temporary
// folder the machine used before.
func keepTempTrimmed(ctx context.Context, log *slog.Logger, before string) {
	dirs := []string{os.TempDir(), filepath.Join(before, "claude-"+strconv.Itoa(os.Getuid()))}
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		trimTemp(log, time.Now(), tempKeep, dirs...)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
