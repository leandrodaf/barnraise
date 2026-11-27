package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSubcommand(t *testing.T) {
	for args, want := range map[string][]string{
		"fetch":     {"fetch", "--prune", "origin"},
		"push":      {"-c", "core.hooksPath=/dev/null", "push", "origin", "x"},
		"ls-remote": {"-C", "/tmp/x", "ls-remote", "origin"},
		"status":    {"--git-dir", "/x", "status"},
		"":          {"-c", "a=b"},
	} {
		if got := subcommand(want); got != args {
			t.Errorf("subcommand(%q) = %q, want %q", want, got, args)
		}
	}
}

// A remote that never answers must not hang the caller: the merge queue
// holds its lock across a fetch, and one dead connection stopped every
// merge of the project for hours.
func TestNetworkCommandsTimeOut(t *testing.T) {
	dir := t.TempDir()
	hang := filepath.Join(dir, "hang.sh")
	if err := os.WriteFile(hang, []byte("#!/bin/sh\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := run(context.Background(), "", nil, "init", "-q", dir); err != nil {
		t.Fatal(err)
	}
	old := networkTimeout
	networkTimeout = 300 * time.Millisecond
	t.Cleanup(func() { networkTimeout = old })
	start := time.Now()
	_, err := runEnv(context.Background(), dir, nil, []string{"GIT_SSH_COMMAND=" + hang}, "fetch", "ssh://nowhere.invalid/x.git")
	if err == nil {
		t.Fatal("fetch from a hanging remote succeeded")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want a deadline", err)
	}
	if took := time.Since(start); took > waitDelay+5*time.Second {
		t.Errorf("fetch took %s", took)
	}
}
