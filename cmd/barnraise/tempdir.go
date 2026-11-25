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

// The daemon's temporary folder lives in its data folder: what sessions,
// the advisor and checks write there (Claude Code keeps its tool outputs in
// $TMPDIR/claude-<uid>) lands on the data disk, not on a small /tmp that,
// once full, breaks every command of every session. The sandboxes give
// back $TMPDIR read-write, so nothing else changes.

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
	dir := filepath.Join(dataDir, "tmp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return before, func() {}
	}
	_ = os.Setenv("TMPDIR", dir)
	return before, restore
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
