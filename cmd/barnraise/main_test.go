package main

import (
	"os"
	"testing"

	"github.com/leandrodaf/barnraise/internal/adapters/in/cli"
	"github.com/leandrodaf/barnraise/internal/adapters/out/landlock"
)

// TestMain lets the test binary stand in for barnraise where the daemon
// runs its own binary again: Landlock confinement (ADR-0031) wraps setup
// and checks in `<binary> sandbox-exec …`, and in these tests the binary is
// this one. Without it a confined check would start the whole test suite.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == landlock.ExecCommand {
		os.Exit(cli.Main(os.Args[1:], cli.Hooks{Version: version, RunDaemon: runDaemon}))
	}
	os.Exit(m.Run())
}
