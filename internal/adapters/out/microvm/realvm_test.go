package microvm

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// TestRealSmolVM boots a real microVM; it runs only with
// BARNRAISE_REAL_SMOLVM=1 on a machine with smolvm and /dev/kvm.
func TestRealSmolVM(t *testing.T) {
	if os.Getenv("BARNRAISE_REAL_SMOLVM") != "1" {
		t.Skip("set BARNRAISE_REAL_SMOLVM=1 to boot a real microVM")
	}
	c := New(Options{Image: "alpine"})
	if s := c.Support(); !s.Available {
		t.Fatalf("support: %s", s.String())
	}
	work := t.TempDir()
	store := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "in.txt"), []byte("from-host"), 0o644); err != nil {
		t.Fatal(err)
	}
	conf := domain.Confinement{Profile: "check", Work: []string{work}, ReadWrite: []string{store}, Network: domain.NetLocal, Env: []string{"BARNRAISE_VM_PROBE"}}
	name, args, err := c.Wrap(conf, "sh", []string{"-c", "cat in.txt && echo \"probe=$BARNRAISE_VM_PROBE\" && pwd && touch out.txt && touch " + store + "/cached && uname -r"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "BARNRAISE_VM_PROBE=ok")
	out, err := cmd.CombinedOutput()
	t.Logf("%s %s\n%s", name, strings.Join(args, " "), out)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "from-host") || !strings.Contains(s, "probe=ok") || !strings.Contains(s, work) {
		t.Fatalf("output %q", s)
	}
	if _, err := os.Stat(filepath.Join(work, "out.txt")); err != nil {
		t.Fatal("the worktree is writable from the VM")
	}
	if _, err := os.Stat(filepath.Join(store, "cached")); err != nil {
		t.Fatal("the shared store is writable from the VM")
	}
	// The denied command never reaches the VM.
	conf.DenyCommands = []string{`git\s+push`}
	if _, _, err := c.Wrap(conf, "git", []string{"push"}); err == nil {
		t.Fatal("denied command wrapped")
	}
}
