package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/ports"
)

func TestBranchWorktreeCommitPush(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	r1 := New(f.c1, "origin", "main")
	if err := r1.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	main0, err := r1.MainHead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const branch = "barnraise/I-1"
	if err := r1.EnsureBranch(ctx, branch, main0); err != nil {
		t.Fatal(err)
	}
	if got := g(t, f.bare, "rev-parse", "refs/heads/"+branch); got != main0 {
		t.Fatalf("remote branch at %s", got)
	}
	if err := r1.EnsureBranch(ctx, branch, main0); err != nil {
		t.Fatalf("ensure twice: %v", err)
	}

	wt := filepath.Join(t.TempDir(), "wt", "I-1")
	if err := r1.AddWorktree(ctx, wt, branch); err != nil {
		t.Fatal(err)
	}
	if err := r1.AddWorktree(ctx, wt, branch); err != nil {
		t.Fatalf("add worktree twice: %v", err)
	}
	write(t, wt, "b.txt", "new\n")
	sha, changed, err := r1.CommitAll(ctx, wt, "step 1\n\nBarnraise-Item: I-1\n")
	if err != nil || !changed {
		t.Fatalf("commit = %s %v %v", sha, changed, err)
	}
	if _, changed, err := r1.CommitAll(ctx, wt, "nothing"); err != nil || changed {
		t.Fatalf("empty commit = %v %v", changed, err)
	}
	if h, _ := r1.Head(ctx, wt); h != sha {
		t.Fatalf("head = %s, want %s", h, sha)
	}
	if err := r1.Push(ctx, wt, branch); err != nil {
		t.Fatal(err)
	}
	if got := g(t, f.bare, "rev-parse", "refs/heads/"+branch); got != sha {
		t.Fatalf("remote branch at %s, want %s", got, sha)
	}

	// Another machine takes over: it gets the pushed branch.
	r2 := New(f.c2, "origin", "main")
	ok, err := r2.FetchBranch(ctx, branch)
	if err != nil || !ok {
		t.Fatalf("fetch branch = %v %v", ok, err)
	}
	if err := r2.EnsureBranch(ctx, branch, main0); err != nil {
		t.Fatal(err)
	}
	if got := g(t, f.c2, "rev-parse", "refs/heads/"+branch); got != sha {
		t.Fatalf("taken-over branch at %s, want %s", got, sha)
	}
	if ok, _ := r2.FetchBranch(ctx, "barnraise/none"); ok {
		t.Fatal("missing branch found")
	}

	// Machine 2 pushes a step; machine 1 pushing a stale history is refused.
	wt2 := filepath.Join(t.TempDir(), "wt2")
	if err := r2.AddWorktree(ctx, wt2, branch); err != nil {
		t.Fatal(err)
	}
	write(t, wt2, "c.txt", "c\n")
	if _, _, err := r2.CommitAll(ctx, wt2, "step 2"); err != nil {
		t.Fatal(err)
	}
	if err := r2.Push(ctx, wt2, branch); err != nil {
		t.Fatal(err)
	}
	write(t, wt, "d.txt", "d\n")
	if _, _, err := r1.CommitAll(ctx, wt, "stale step"); err != nil {
		t.Fatal(err)
	}
	if err := r1.Push(ctx, wt, branch); !errors.Is(err, ErrPushRejected) {
		t.Fatalf("stale push = %v, want ErrPushRejected", err)
	}

	if err := r1.RemoveWorktree(ctx, wt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatal("worktree still on disk")
	}
	if err := r1.DeleteBranch(ctx, branch); err != nil {
		t.Fatal(err)
	}
	if out := g(t, f.bare, "branch", "--list", branch); out != "" {
		t.Fatalf("remote branch not deleted: %q", out)
	}
	if out := g(t, f.c1, "branch", "--list", branch); out != "" {
		t.Fatalf("local branch not deleted: %q", out)
	}
	if err := r1.DeleteBranch(ctx, branch); err != nil {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestRebase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	r := New(f.c1, "origin", "main")
	main0, _ := r.MainHead(ctx)
	if err := r.EnsureBranch(ctx, "b", main0); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	if err := r.AddWorktree(ctx, wt, "b"); err != nil {
		t.Fatal(err)
	}
	write(t, wt, "a.txt", "one\nTWO from item\nthree\n")
	before, _, err := r.CommitAll(ctx, wt, "item edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Push(ctx, wt, "b"); err != nil {
		t.Fatal(err)
	}

	// main changes the same line: conflict.
	write(t, f.c1, "a.txt", "one\nTWO from main\nthree\n")
	g(t, f.c1, "commit", "-q", "-am", "main edit")
	conflicting := g(t, f.c1, "rev-parse", "HEAD")
	err = r.Rebase(ctx, wt, conflicting)
	if !errors.Is(err, ports.ErrConflict) || !strings.Contains(err.Error(), "a.txt") {
		t.Fatalf("rebase = %v, want ErrConflict on a.txt", err)
	}
	if h, _ := r.Head(ctx, wt); h != before {
		t.Fatal("head moved after aborted rebase")
	}
	if st := g(t, wt, "status", "--porcelain"); st != "" {
		t.Fatalf("worktree dirty after abort: %q", st)
	}

	// A non-conflicting main change rebases fine, and the push after the
	// rebase (a rewrite) is accepted by the lease.
	g(t, f.c1, "reset", "-q", "--hard", main0)
	write(t, f.c1, "z.txt", "z\n")
	g(t, f.c1, "add", "-A")
	g(t, f.c1, "commit", "-q", "-m", "other main change")
	onto := g(t, f.c1, "rev-parse", "HEAD")
	if err := r.Rebase(ctx, wt, onto); err != nil {
		t.Fatal(err)
	}
	after, _ := r.Head(ctx, wt)
	if after == before {
		t.Fatal("rebase did nothing")
	}
	if err := r.Push(ctx, wt, "b"); err != nil {
		t.Fatalf("push after rebase: %v", err)
	}
	if got := g(t, f.bare, "rev-parse", "refs/heads/b"); got != after {
		t.Fatal("remote not updated after rebase push")
	}
}

func TestHistoryQueries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := newLocal(t)
	r := New(dir, "", "")
	base, _ := r.MainHead(ctx)
	if err := r.EnsureBranch(ctx, "b", base); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	if err := r.AddWorktree(ctx, wt, "b"); err != nil {
		t.Fatal(err)
	}
	write(t, wt, "a.txt", "one\nchanged\nthree\n")
	c1, _, err := r.CommitAll(ctx, wt, "Change line two\n\nWhy it changed.\n\nBarnraise-Item: I-142\nBarnraise-Step: implement\n")
	if err != nil {
		t.Fatal(err)
	}
	write(t, wt, "dir/new.txt", "x\n")
	c2, _, err := r.CommitAll(ctx, wt, "Add file")
	if err != nil {
		t.Fatal(err)
	}

	logs, err := r.Log(ctx, "-1", c1)
	if err != nil || len(logs) != 1 {
		t.Fatalf("log = %v %v", logs, err)
	}
	lc := logs[0]
	if lc.SHA != c1 || lc.Subject != "Change line two" || !strings.HasPrefix(lc.Body, "Why it changed.") {
		t.Fatalf("log entry = %+v", lc)
	}
	if lc.Trailers["Barnraise-Item"] != "I-142" || lc.Trailers["Barnraise-Step"] != "implement" {
		t.Fatalf("trailers = %v", lc.Trailers)
	}
	if !slices.Equal(lc.Files, []string{"a.txt"}) || lc.Author != fallbackName+" <"+fallbackEmail+">" || lc.Time.IsZero() {
		t.Fatalf("log entry = %+v", lc)
	}
	all, err := r.Log(ctx, base+".."+c2)
	if err != nil || len(all) != 2 || all[0].SHA != c2 || len(all[0].Trailers) != 0 {
		t.Fatalf("range log = %+v %v", all, err)
	}

	files, err := r.ChangedFiles(ctx, base, c2)
	if err != nil || !slices.Equal(files, []string{"a.txt", "dir/new.txt"}) {
		t.Fatalf("changed = %v %v", files, err)
	}
	if !r.ExistsAt(ctx, c2, "dir/new.txt") || r.ExistsAt(ctx, base, "dir/new.txt") {
		t.Fatal("ExistsAt wrong")
	}
	if b, err := r.Show(ctx, c1, "a.txt"); err != nil || string(b) != "one\nchanged\nthree\n" {
		t.Fatalf("show = %q %v", b, err)
	}
	full, err := r.Diff(ctx, base, c2, 0)
	if err != nil || !strings.Contains(full, "+changed") {
		t.Fatalf("diff = %q %v", full, err)
	}
	cut, _ := r.Diff(ctx, base, c2, 20)
	if !strings.HasPrefix(cut, full[:20]) || !strings.Contains(cut, "truncated") {
		t.Fatalf("truncated diff = %q", cut)
	}

	squash, err := r.SquashOnto(ctx, wt, base, "I-142: squashed")
	if err != nil {
		t.Fatal(err)
	}
	if p := g(t, dir, "rev-parse", squash+"^"); p != base {
		t.Fatalf("squash parent = %s", p)
	}
	if a, b := g(t, dir, "rev-parse", squash+"^{tree}"), g(t, dir, "rev-parse", c2+"^{tree}"); a != b {
		t.Fatal("squash tree differs from HEAD")
	}

	// Blame reads HEAD of the root: integrate the branch into main first.
	a := NewArbiter(r, nil)
	if err := a.Integrate(ctx, base, c2); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Blame(ctx, "a.txt", 2); err != nil || got != c1 {
		t.Fatalf("blame line 2 = %s %v, want %s", got, err, c1)
	}
	if got, _ := r.Blame(ctx, "a.txt", 1); got != base {
		t.Fatalf("blame line 1 = %s, want %s", got, base)
	}
	if _, err := os.Stat(filepath.Join(dir, "dir", "new.txt")); err != nil {
		t.Fatal("checked-out main not updated by integrate")
	}
}

func TestParseTrailersFallback(t *testing.T) {
	m := parseTrailers(lastParagraph("text\n\nBarnraise-Item: I-9\nSigned-off-by: A <a@b>"))
	if m["Barnraise-Item"] != "I-9" || m["Signed-off-by"] != "A <a@b>" {
		t.Fatalf("trailers = %v", m)
	}
	if lastParagraph("just text: not a trailer\nmore words here") != "" {
		t.Fatal("prose taken as trailers")
	}
}
