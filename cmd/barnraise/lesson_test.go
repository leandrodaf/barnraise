package main

import (
	"strings"
	"testing"
)

// TestLessonCLI drives `barnraise lesson` against a running daemon.
func TestLessonCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a daemon")
	}
	h := startHarness(t, demoYAML)
	out, code := cliRun(t, h.root, "lesson", "list")
	if code != 0 || !strings.Contains(out, "No lessons yet.") {
		t.Fatalf("list: %d %s", code, out)
	}
	out, code = cliRun(t, h.root, "--json", "lesson", "list", "--area", "wallet")
	if code != 0 || !strings.Contains(out, `"lessons": []`) {
		t.Fatalf("list json: %d %s", code, out)
	}
	if _, code := cliRun(t, h.root, "lesson", "delete", "L-00000000"); code == 0 {
		t.Fatal("deleting a missing lesson succeeds")
	}
}
