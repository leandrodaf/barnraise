package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/testkit"
)

// The advisor reads main as the remote has it now: what someone pushed since
// the owner's last pull is there, and the owner's checkout is left as it is.
func TestMainWorkspaceIsTheRemotesMain(t *testing.T) {
	e := newEnv(t, withGit(), withFiles(map[string]string{"concept/game-design.md": "old design\n"}))
	// Someone else pushes a new design to main; the owner's checkout lags.
	other := filepath.Join(t.TempDir(), "other")
	testkit.RunGit(t, filepath.Dir(other), "clone", "-q", e.repo.Remote, other)
	testkit.WriteFile(t, other, "concept/game-design.md", "new design: the Atlas holds every plan\n")
	testkit.RunGit(t, other, "-c", "user.email=r@x", "-c", "user.name=R", "commit", "-qam", "design")
	testkit.RunGit(t, other, "push", "-q", "origin", "main")

	dir, err := e.svc.mainWorkspace(e.ctx, e.p)
	if err != nil {
		t.Fatal(err)
	}
	if dir == e.root {
		t.Fatal("the advisor must not read the owner's checkout")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "concept/game-design.md"))
	if !strings.Contains(string(b), "the Atlas holds every plan") {
		t.Fatalf("the advisor reads %q, not main as pushed", b)
	}
	if own, _ := os.ReadFile(filepath.Join(e.root, "concept/game-design.md")); string(own) != "old design\n" {
		t.Fatalf("the owner's checkout changed: %q", own)
	}
	// Again: the same workspace, moved to what main is then.
	if again, err := e.svc.mainWorkspace(e.ctx, e.p); err != nil || again != dir {
		t.Fatalf("again: %s %v", again, err)
	}
}
