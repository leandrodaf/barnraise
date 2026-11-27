package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/config"
)

func TestAutoConfig(t *testing.T) {
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n\ngo 1.22\n"), 0o644))
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	must(exec.Command("git", "-C", root, "remote", "add", "origin", "git@github.com:someone/thing.git").Run())
	wrote, err := AutoConfig(root, AutoOptions{Language: "pt-BR"})
	if err != nil || !wrote {
		t.Fatalf("%v %v", wrote, err)
	}
	b, _ := os.ReadFile(filepath.Join(root, config.FileName))
	for _, want := range []string{"language: pt-BR", "go test ./...", "type: github", "repo: someone/thing", "approves: owner"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("barnraise.yaml lacks %q:\n%s", want, b)
		}
	}
	if _, err := config.Load(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, config.Dir)); err != nil {
		t.Fatal(".barnraise/ was not written")
	}
	// The owner's own file is never rewritten.
	must(os.WriteFile(filepath.Join(root, config.FileName), []byte("template: blank\n"), 0o644))
	if wrote, err := AutoConfig(root, AutoOptions{}); err != nil || wrote {
		t.Fatalf("%v %v", wrote, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, config.FileName)); string(b) != "template: blank\n" {
		t.Fatalf("rewritten: %s", b)
	}
}
