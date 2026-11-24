package git

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRealMergirafResolvesNeighbouringEdits runs only where mergiraf is
// installed: two branches change neighbouring functions of one Go file,
// which a line merge reports as a conflict and a structural merge does not.
func TestRealMergirafResolvesNeighbouringEdits(t *testing.T) {
	if _, err := exec.LookPath("mergiraf"); err != nil {
		t.Skip("mergiraf is not installed")
	}
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	g(t, base, "init", "-q", "-b", "main", root)
	src := "package p\n\nfunc A() int { return 1 }\nfunc B() int { return 2 }\n"
	write(t, root, "p.go", src)
	g(t, root, "add", "-A")
	g(t, root, "commit", "-q", "-m", "base")
	g(t, root, "branch", "item")
	write(t, root, "p.go", strings.Replace(src, "return 1", "return 10", 1))
	g(t, root, "commit", "-q", "-am", "main")
	r := New(root, "", "main")
	wt := filepath.Join(base, "wt")
	if err := r.AddWorktree(ctx, wt, "item"); err != nil {
		t.Fatal(err)
	}
	write(t, wt, "p.go", strings.Replace(src, "return 2", "return 20", 1))
	g(t, wt, "commit", "-q", "-am", "item")

	rep, err := r.SetupMerge(ctx, MergeSetup{Structural: "auto"})
	if err != nil || rep.Structural == "" || rep.Patterns == 0 {
		t.Fatalf("setup: %+v %v", rep, err)
	}
	if _, err := r.RebaseReusing(ctx, wt, "main"); err != nil {
		t.Fatalf("rebase with mergiraf: %v", err)
	}
	got := read(t, filepath.Join(wt, "p.go"))
	if !strings.Contains(got, "return 10") || !strings.Contains(got, "return 20") || strings.Contains(got, "<<<<<<<") {
		t.Fatalf("merged file:\n%s", got)
	}
}
