package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

// sendBackForConflict records the rework round the merge queue writes when
// the item does not rebase on main.
func (e *env) sendBackForConflict(id string) {
	e.t.Helper()
	if _, err := e.svc.mutateItem(e.ctx, e.p, id, domain.EvItemUpdated, "test", func(it *domain.Item) error {
		it.Rework = append(it.Rework, domain.ReworkRecord{From: "integrate", To: "implement", By: "merge-queue",
			Reason: "merge queue: rebase on main conflicts: rebase conflict: a.txt", Outcome: "fail"})
		return nil
	}, domain.Payload{}); err != nil {
		e.t.Fatal(err)
	}
}

// The merge queue sent the item back for a conflict. Barnraise rebases the
// branch itself and stops at the file the agent must resolve; .barnraise/
// takes main's side. The agent only edits the file, and the step's commit
// finishes the rebase, without the empty files the sandbox mounts.
func TestBarnraiseRebasesForTheAgent(t *testing.T) {
	e := newEnv(t, withGit(), withFiles(map[string]string{"a.txt": "one\ntwo\nthree\n", ".barnraise/map.md": "map 0\n"}))
	it := e.ready("simple", "conflicting", "x")
	e.start(it.ID)
	dir := e.workspace(it.ID, map[string]string{"a.txt": "one\nTWO from item\nthree\n"})
	if _, err := e.svc.commitStep(e.ctx, e.p, it.ID, "item work"); err != nil {
		t.Fatal(err)
	}
	main := e.repo.PushToMain(t, "main edit", map[string]string{"a.txt": "one\nTWO from main\nthree\n", ".barnraise/map.md": "map 1\n"})
	e.sendBackForConflict(it.ID)

	left := e.svc.prepareRebase(e.ctx, e.p, e.item(it.ID), "implement", dir)
	if !slices.Equal(left, []string{"a.txt"}) {
		t.Fatalf("stopped at %v, want a.txt", left)
	}
	if b := rebaseBrief(left); !strings.Contains(b, "a.txt") || !strings.Contains(b, "Do not run git rebase") {
		t.Fatalf("brief %q", b)
	}
	// A safety commit while the session works leaves the rebase alone.
	if e.svc.settleUnfinished(e.ctx, e.p, it.ID, dir, true) {
		t.Fatal("a live session's rebase was settled")
	}
	testkit.WriteFile(t, dir, "a.txt", "one\nTWO from main and from item\nthree\n")
	for _, f := range []string{".bashrc", ".mcp.json", ".claude/agents"} {
		testkit.WriteFile(t, dir, f, "")
		if err := os.Chmod(filepath.Join(dir, f), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.commitStep(e.ctx, e.p, it.ID, "chore: done step implement"); err != nil {
		t.Fatal(err)
	}
	if base := testkit.RunGit(t, dir, "merge-base", "HEAD", main); base != main {
		t.Fatal("the branch is not on main")
	}
	if st := testkit.RunGit(t, dir, "status", "--porcelain"); st != "" {
		t.Fatalf("worktree dirty: %q", st)
	}
	tracked := testkit.RunGit(t, dir, "ls-files")
	for _, f := range []string{".bashrc", ".mcp.json", ".claude/agents"} {
		if strings.Contains(tracked, f) || exists(filepath.Join(dir, f)) {
			t.Fatalf("%s rode with the commit or stayed behind", f)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(dir, ".barnraise/map.md")); string(got) != "map 1\n" {
		t.Fatalf(".barnraise/map.md = %q, want main's", got)
	}
	// Already on main: the next session has nothing to rebase.
	if left := e.svc.prepareRebase(e.ctx, e.p, e.item(it.ID), "implement", dir); len(left) != 0 {
		t.Fatalf("rebased again: %v", left)
	}
}

// A rebase the agent left unresolved is undone before the step's commit:
// the branch stays as it was, and no conflict marker lands on it.
func TestUnresolvedRebaseIsUndoneBeforeTheCommit(t *testing.T) {
	e := newEnv(t, withGit(), withFiles(map[string]string{"a.txt": "one\ntwo\nthree\n"}))
	it := e.ready("simple", "left halfway", "x")
	e.start(it.ID)
	dir := e.workspace(it.ID, map[string]string{"a.txt": "one\nTWO from item\nthree\n"})
	before, err := e.svc.commitStep(e.ctx, e.p, it.ID, "item work")
	if err != nil {
		t.Fatal(err)
	}
	e.repo.PushToMain(t, "main edit", map[string]string{"a.txt": "one\nTWO from main\nthree\n"})
	e.sendBackForConflict(it.ID)
	if left := e.svc.prepareRebase(e.ctx, e.p, e.item(it.ID), "implement", dir); len(left) != 1 {
		t.Fatalf("stopped at %v", left)
	}
	// Another step is not the one sent back: nothing is prepared for it.
	if left := e.svc.prepareRebase(e.ctx, e.p, e.item(it.ID), "review", dir); len(left) != 1 {
		t.Fatalf("the stopped rebase is still reported: %v", left)
	}
	sha, err := e.svc.commitStep(e.ctx, e.p, it.ID, "chore: blocked step implement")
	if err != nil {
		t.Fatal(err)
	}
	if sha != before {
		t.Fatalf("head %s, want the branch as it was (%s)", sha, before)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "a.txt")); strings.Contains(string(got), "<<<<<<<") {
		t.Fatal("conflict markers left in the worktree")
	}
}
