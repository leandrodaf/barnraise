package git

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// A rebase Barnraise starts for an agent stops only at the conflicts the
// agent must resolve: Barnraise's own files take main's side and the item's
// memory keeps the branch's. After the agent edits the file, the rebase
// finishes; one left stopped can be undone, back to the branch as it was.
func TestStoppedRebaseForAnAgent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	r := New(f.c1, "origin", "main")
	write(t, f.c1, ".barnraise/map.md", "map 0\n")
	write(t, f.c1, "plan.md", "plan 0\n")
	g(t, f.c1, "add", "-A")
	g(t, f.c1, "commit", "-q", "-m", "barnraise files")
	main0 := g(t, f.c1, "rev-parse", "HEAD")
	if err := r.EnsureBranch(ctx, "b", main0); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	if err := r.AddWorktree(ctx, wt, "b"); err != nil {
		t.Fatal(err)
	}
	write(t, wt, "a.txt", "one\nTWO from item\nthree\n")
	write(t, wt, ".barnraise/map.md", "map from the item\n")
	write(t, wt, "plan.md", "plan of the item\n")
	before, _, err := r.CommitAll(ctx, wt, "item edit")
	if err != nil {
		t.Fatal(err)
	}

	write(t, f.c1, "a.txt", "one\nTWO from main\nthree\n")
	write(t, f.c1, ".barnraise/map.md", "map 1\n")
	write(t, f.c1, "plan.md", "plan on main\n")
	g(t, f.c1, "commit", "-q", "-am", "main edit")
	onto := g(t, f.c1, "rev-parse", "HEAD")
	sides := ports.RebaseSides{Onto: []string{".barnraise/"}, Own: []string{"plan.md"}}

	left, err := r.StartRebase(ctx, wt, onto, sides)
	if err != nil || !slices.Equal(left, []string{"a.txt"}) {
		t.Fatalf("start = %v %v, want stopped at a.txt", left, err)
	}
	if !r.Unfinished(ctx, wt) {
		t.Fatal("the rebase did not stay stopped")
	}
	if left, err := r.ContinueRebase(ctx, wt, sides); err != nil || !slices.Equal(left, []string{"a.txt"}) {
		t.Fatalf("continue with markers left = %v %v", left, err)
	}
	write(t, wt, "a.txt", "one\nTWO from main and from item\nthree\n")
	if left, err := r.ContinueRebase(ctx, wt, sides); err != nil || len(left) != 0 {
		t.Fatalf("continue = %v %v", left, err)
	}
	if r.Unfinished(ctx, wt) {
		t.Fatal("the rebase did not finish")
	}
	if base := g(t, wt, "merge-base", "HEAD", onto); base != onto {
		t.Fatal("the branch is not on main")
	}
	for file, want := range map[string]string{".barnraise/map.md": "map 1\n", "plan.md": "plan of the item\n", "a.txt": "one\nTWO from main and from item\nthree\n"} {
		if got, _ := os.ReadFile(filepath.Join(wt, file)); string(got) != want {
			t.Fatalf("%s = %q, want %q", file, got, want)
		}
	}
	if st := g(t, wt, "status", "--porcelain"); st != "" {
		t.Fatalf("worktree dirty: %q", st)
	}

	// Stopped and never resolved: undone, the branch as it was.
	g(t, wt, "reset", "-q", "--hard", before)
	if left, err := r.StartRebase(ctx, wt, onto, sides); err != nil || len(left) != 1 {
		t.Fatalf("start again = %v %v", left, err)
	}
	if err := r.AbortUnfinished(ctx, wt); err != nil {
		t.Fatal(err)
	}
	if r.Unfinished(ctx, wt) {
		t.Fatal("still unfinished after the abort")
	}
	if h, _ := r.Head(ctx, wt); h != before {
		t.Fatal("head moved after the abort")
	}
}
