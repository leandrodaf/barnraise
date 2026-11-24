package microvm

import (
	"context"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func TestInstalledToolsReachTheVMReadOnly(t *testing.T) {
	tools := t.TempDir()
	c := New(Options{Image: "img", LookPath: func(string) (string, error) { return "/bin/smolvm", nil }, KVM: func() bool { return true },
		Version: func(context.Context, string) (string, error) { return "smolvm 1.37.0", nil }})
	_, args, err := c.Wrap(domain.Confinement{Profile: "check", Work: []string{t.TempDir()}, Tools: []string{tools}}, "true", nil)
	if err != nil {
		t.Fatal(err)
	}
	if line := strings.Join(args, " "); !strings.Contains(line, "--volume "+tools+":"+tools+":ro") {
		t.Fatalf("tools not mounted read-only: %s", line)
	}
}
