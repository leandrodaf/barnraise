package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTrimTemp(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := filepath.Join(dir, "old-session")
	busy := filepath.Join(dir, "busy-session")
	for _, d := range []string{old, busy} {
		if err := os.MkdirAll(filepath.Join(d, "tasks"), 0o755); err != nil {
			t.Fatal(err)
		}
		f := filepath.Join(d, "tasks", "out.txt")
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(f, now.Add(-48*time.Hour), now.Add(-48*time.Hour))
		_ = os.Chtimes(filepath.Join(d, "tasks"), now.Add(-48*time.Hour), now.Add(-48*time.Hour))
		_ = os.Chtimes(d, now.Add(-48*time.Hour), now.Add(-48*time.Hour))
	}
	// Something inside the busy one changed a minute ago: it stays.
	if err := os.WriteFile(filepath.Join(busy, "tasks", "new.txt"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	trimTemp(slog.New(slog.NewTextHandler(io.Discard, nil)), now, 24*time.Hour, dir, filepath.Join(dir, "missing"))
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("an old leftover stayed: %v", err)
	}
	if _, err := os.Stat(busy); err != nil {
		t.Errorf("a folder in use was removed: %v", err)
	}
}

func TestUseDataTemp(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	data := t.TempDir()
	before, restore := useDataTemp(data)
	if before != "/tmp" || os.TempDir() != filepath.Join(data, "tmp") {
		t.Fatalf("before %q, now %q", before, os.TempDir())
	}
	restore()
	if os.TempDir() != "/tmp" {
		t.Fatalf("not put back: %q", os.TempDir())
	}
	if fi, err := os.Stat(filepath.Join(data, "tmp")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("%v %v", fi, err)
	}
}
