package outfilter

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// TestRealRTK runs only where rtk is installed: a command with no native
// filter goes through rtk, and one rtk does not know still runs once.
func TestRealRTK(t *testing.T) {
	rtk, err := exec.LookPath("rtk")
	if err != nil {
		t.Skip("rtk is not installed")
	}
	dir := t.TempDir()
	o := Options{Store: Store{Dir: dir}, Delegate: "rtk", RTK: rtk}
	res, err := RunCommand(context.Background(), []string{"ls", "-la", "/usr/bin"}, o)
	if err != nil || res.Exit != 0 || res.Text == "" {
		t.Fatalf("ls through rtk: %+v %v", res, err)
	}
	t.Logf("ls via rtk: shortened=%v delegated=%q bytes=%d", res.Shortened, res.Record.Delegated, len(res.Text))
	res, err = RunCommand(context.Background(), []string{"printf", "hello-once"}, o)
	if err != nil || res.Exit != 0 || strings.Count(res.Text, "hello-once") != 1 {
		t.Fatalf("unknown command through rtk: %+v %v", res, err)
	}
}
