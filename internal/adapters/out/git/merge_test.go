package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// conflictRepo is a local repository whose main and an item worktree both
// changed the middle line of f.txt.
type conflictRepo struct {
	repo *Repo
	root string
	wt   string // worktree on branch item
}

func newConflictRepo(t *testing.T, mainLine, itemLine string) *conflictRepo {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	g(t, base, "init", "-q", "-b", "main", root)
	write(t, root, "f.txt", "a\nb\nc\n")
	g(t, root, "add", "-A")
	g(t, root, "commit", "-q", "-m", "base")
	g(t, root, "branch", "item")
	write(t, root, "f.txt", "a\n"+mainLine+"\nc\n")
	g(t, root, "commit", "-q", "-am", "main")
	r := New(root, "", "main")
	wt := filepath.Join(base, "wt")
	if err := r.AddWorktree(context.Background(), wt, "item"); err != nil {
		t.Fatal(err)
	}
	write(t, wt, "f.txt", "a\n"+itemLine+"\nc\n")
	g(t, wt, "commit", "-q", "-am", "item")
	return &conflictRepo{repo: r, root: root, wt: wt}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// resolveOnce resolves the conflict in dir by hand (as an agent would) and
// puts the branch back where it was, so the next rebase meets it again.
func resolveOnce(t *testing.T, r *Repo, dir, content string) {
	t.Helper()
	ctx := context.Background()
	orig, _ := r.Head(ctx, dir)
	if _, err := run(ctx, dir, nil, "-c", "user.name=T", "-c", "user.email=t@e", "rebase", "main"); err == nil {
		t.Fatal("expected a conflict")
	}
	write(t, dir, "f.txt", content)
	g(t, dir, "add", "f.txt")
	// Left to Barnraise's commit: the resolution is recorded there too.
	if _, _, err := r.CommitAll(ctx, dir, "resolve"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(ctx, dir, nil, "-c", "user.name=T", "-c", "user.email=t@e", "rebase", "--continue"); err != nil {
		t.Fatal(err)
	}
	g(t, dir, "reset", "-q", "--hard", orig)
}

func TestRebaseWithoutRerereConflicts(t *testing.T) {
	c := newConflictRepo(t, "MAIN", "ITEM")
	_, err := c.repo.RebaseReusing(context.Background(), c.wt, "main")
	if !errors.Is(err, ports.ErrConflict) || !strings.Contains(err.Error(), "f.txt") {
		t.Fatalf("err %v", err)
	}
	if c.repo.rebaseInProgress(context.Background(), c.wt) {
		t.Fatal("the rebase is aborted")
	}
}

func TestRebaseReusesResolutionAcrossWorktrees(t *testing.T) {
	for _, auto := range []bool{true, false} {
		t.Run(map[bool]string{true: "autoupdate", false: "no-autoupdate"}[auto], func(t *testing.T) {
			ctx := context.Background()
			c := newConflictRepo(t, "MAIN", "ITEM")
			if !auto {
				g(t, c.root, "config", "rerere.autoupdate", "false") // the user decided
			}
			rep, err := c.repo.SetupMerge(ctx, MergeSetup{Rerere: true, Structural: "off"})
			if err != nil || !rep.Rerere {
				t.Fatalf("%+v %v", rep, err)
			}
			if v := g(t, c.root, "config", "--get", "rerere.autoupdate"); v != map[bool]string{true: "true", false: "false"}[auto] {
				t.Fatalf("the user's rerere.autoupdate is kept: %s", v)
			}
			resolveOnce(t, c.repo, c.wt, "a\nBOTH\nc\n")
			// Another worktree of the same repository (the merge queue's) meets the same conflict.
			other := filepath.Join(t.TempDir(), "other")
			if err := c.repo.AddDetachedWorktree(ctx, other, "item"); err != nil {
				t.Fatal(err)
			}
			out, err := c.repo.RebaseReusing(ctx, other, "main")
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Reapplied) != 1 || out.Reapplied[0] != "f.txt" {
				t.Fatalf("reapplied %+v", out)
			}
			if got := read(t, filepath.Join(other, "f.txt")); got != "a\nBOTH\nc\n" {
				t.Fatalf("content %q", got)
			}
			if c.repo.rebaseInProgress(ctx, other) {
				t.Fatal("the rebase finished")
			}
		})
	}
}

func TestRebaseSkipsCommitEmptiedByResolution(t *testing.T) {
	ctx := context.Background()
	c := newConflictRepo(t, "MAIN", "ITEM")
	if _, err := c.repo.SetupMerge(ctx, MergeSetup{Rerere: true, Structural: "off"}); err != nil {
		t.Fatal(err)
	}
	resolveOnce(t, c.repo, c.wt, "a\nMAIN\nc\n") // main's side wins: the item's commit adds nothing
	main, _, _ := c.repo.resolve(ctx, "", "main")
	out, err := c.repo.RebaseReusing(ctx, c.wt, "main")
	if err != nil || len(out.Reapplied) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	if head, _ := c.repo.Head(ctx, c.wt); head != main {
		t.Fatalf("head %s, main %s", head, main)
	}
}

func TestRebasePartlyReusedStillConflicts(t *testing.T) {
	ctx := context.Background()
	c := newConflictRepo(t, "MAIN", "ITEM")
	if _, err := c.repo.SetupMerge(ctx, MergeSetup{Rerere: true, Structural: "off"}); err != nil {
		t.Fatal(err)
	}
	resolveOnce(t, c.repo, c.wt, "a\nBOTH\nc\n")
	// A second file now conflicts too, with no recorded resolution.
	write(t, c.root, "g.txt", "main\n")
	g(t, c.root, "add", "g.txt")
	g(t, c.root, "commit", "-q", "-m", "main g")
	write(t, c.wt, "g.txt", "item\n")
	g(t, c.wt, "add", "g.txt")
	g(t, c.wt, "commit", "-q", "--amend", "--no-edit")
	_, err := c.repo.RebaseReusing(ctx, c.wt, "main")
	if !errors.Is(err, ports.ErrConflict) || !strings.Contains(err.Error(), "g.txt") || strings.Contains(err.Error(), "f.txt") {
		t.Fatalf("err %v", err)
	}
}

func TestSetupRerereRespectsAndRemoves(t *testing.T) {
	ctx := context.Background()
	c := newConflictRepo(t, "MAIN", "ITEM")
	g(t, c.root, "config", "rerere.enabled", "false")
	rep, err := c.repo.SetupMerge(ctx, MergeSetup{Rerere: true, Structural: "off"})
	if err != nil || rep.Rerere || !strings.Contains(rep.RerereNote, "false") {
		t.Fatalf("the user's false is kept: %+v %v", rep, err)
	}
	g(t, c.root, "config", "--unset", "rerere.enabled")
	if rep, _ := c.repo.SetupMerge(ctx, MergeSetup{Rerere: true, Structural: "off"}); !rep.Rerere {
		t.Fatalf("%+v", rep)
	}
	rep, err = c.repo.SetupMerge(ctx, MergeSetup{Rerere: false, Structural: "off"})
	if err != nil || rep.Rerere {
		t.Fatalf("off removes what Barnraise set: %+v %v", rep, err)
	}
	if _, set, _ := c.repo.configGet(ctx, "--get", "rerere.autoupdate"); set {
		t.Fatal("autoupdate removed")
	}
	g(t, c.root, "config", "rerere.enabled", "true")
	if rep, _ := c.repo.SetupMerge(ctx, MergeSetup{Rerere: false, Structural: "off"}); !rep.Rerere || rep.RerereNote == "" {
		t.Fatalf("the user's own true stays: %+v", rep)
	}
}

// fakeStructural writes a program that answers like a structural merge
// driver: it lists *.txt and resolves every merge with "MERGED".
func fakeStructural(t *testing.T, listing string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake driver")
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"languages) printf '" + listing + "' ;;\n" +
		"merge) shift; [ \"$1\" = --git ] || exit 9; printf 'a\\nMERGED\\nc\\n' > \"$3\"; echo \"$@\" >> \"" + filepath.Join(dir, "calls") + "\" ;;\n" +
		"*) exit 2 ;;\n" +
		"esac\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStructuralDriverResolvesRebase(t *testing.T) {
	ctx := context.Background()
	c := newConflictRepo(t, "MAIN", "ITEM")
	write(t, c.root, ".gitattributes", "*.lock merge=ours\n")
	common, _ := c.repo.CommonDir(ctx)
	write(t, common, "info/attributes", "*.bin binary\n")
	bin := fakeStructural(t, `*.txt merge=mergiraf\n*.lock merge=mergiraf\nbad pattern merge=mergiraf\n$(x) merge=mergiraf\ngo.mod merge=mergiraf\n`)
	rep, err := c.repo.SetupMerge(ctx, MergeSetup{Rerere: false, Structural: "auto", StructuralBin: bin})
	if err != nil || rep.Structural == "" || rep.Patterns != 2 {
		t.Fatalf("%+v %v", rep, err)
	}
	attrs := read(t, filepath.Join(common, "info", "attributes"))
	if !strings.HasPrefix(attrs, "*.bin binary\n") || !strings.Contains(attrs, "*.txt merge="+StructuralDriver) ||
		!strings.Contains(attrs, "go.mod merge="+StructuralDriver) || strings.Contains(attrs, "*.lock merge="+StructuralDriver) ||
		!strings.Contains(attrs, "*.lock merge=ours\n") || strings.Contains(attrs, "$(x)") {
		t.Fatalf("attributes:\n%s", attrs)
	}
	// Idempotent.
	if _, err := c.repo.SetupMerge(ctx, MergeSetup{Structural: "auto", StructuralBin: bin}); err != nil {
		t.Fatal(err)
	}
	if again := read(t, filepath.Join(common, "info", "attributes")); again != attrs {
		t.Fatalf("second setup changed the file:\n%s", again)
	}
	// The worktree's rebase goes through the driver instead of conflicting.
	if _, err := c.repo.RebaseReusing(ctx, c.wt, "main"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(c.wt, "f.txt")); got != "a\nMERGED\nc\n" {
		t.Fatalf("content %q", got)
	}
	if calls := read(t, filepath.Join(filepath.Dir(bin), "calls")); !strings.Contains(calls, "-p f.txt") {
		t.Fatalf("driver calls %q", calls)
	}
	if out := g(t, c.root, "status", "--porcelain"); strings.Contains(out, ".gitattributes") && strings.Contains(out, "info") {
		t.Fatalf("nothing tracked changes: %s", out)
	}

	// Off (or missing): the block and the driver go, the user's line stays.
	if _, err := c.repo.SetupMerge(ctx, MergeSetup{Structural: "off"}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(common, "info", "attributes")); got != "*.bin binary\n" {
		t.Fatalf("after off:\n%q", got)
	}
	if _, set, _ := c.repo.configGet(ctx, "--get", "merge."+StructuralDriver+".driver"); set {
		t.Fatal("driver config removed")
	}
}

func TestStructuralDriverGoneFallsBackToLineMerge(t *testing.T) {
	ctx := context.Background()
	c := newConflictRepo(t, "MAIN", "ITEM")
	bin := fakeStructural(t, `*.txt merge=mergiraf\n`)
	if rep, err := c.repo.SetupMerge(ctx, MergeSetup{Structural: "auto", StructuralBin: bin}); err != nil || rep.Structural == "" {
		t.Fatalf("%+v %v", rep, err)
	}
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	// An agent's own rebase, where the program is not there: a normal conflict with markers.
	if _, err := run(ctx, c.wt, nil, "-c", "user.name=T", "-c", "user.email=t@e", "rebase", "main"); err == nil {
		t.Fatal("expected a conflict")
	}
	got := read(t, filepath.Join(c.wt, "f.txt"))
	if !strings.Contains(got, "<<<<<<<") || !strings.Contains(got, "MAIN") || !strings.Contains(got, "ITEM") {
		t.Fatalf("markers expected:\n%s", got)
	}
	g(t, c.wt, "rebase", "--abort")
}

func TestStructuralMissingOrSilent(t *testing.T) {
	ctx := context.Background()
	c := newConflictRepo(t, "MAIN", "ITEM")
	missing := func(string) (string, error) { return "", errors.New("not found") }
	rep, err := c.repo.SetupMerge(ctx, MergeSetup{Structural: "auto", LookPath: missing})
	if err != nil || rep.Structural != "" || !strings.Contains(rep.Note, "mergiraf is not installed") {
		t.Fatalf("%+v %v", rep, err)
	}
	silent := fakeStructural(t, `nothing here\n`)
	rep, err = c.repo.SetupMerge(ctx, MergeSetup{Structural: "auto", StructuralBin: silent})
	if err != nil || rep.Structural != "" || !strings.Contains(rep.Note, "did not list") {
		t.Fatalf("%+v %v", rep, err)
	}
	common, _ := c.repo.CommonDir(ctx)
	if _, err := os.Stat(filepath.Join(common, "info", "attributes")); err == nil {
		if got := read(t, filepath.Join(common, "info", "attributes")); strings.Contains(got, "barnraise") {
			t.Fatalf("no block: %s", got)
		}
	}
	// Without a structural driver a conflict stays a conflict.
	if _, err := c.repo.RebaseReusing(ctx, c.wt, "main"); !errors.Is(err, ports.ErrConflict) {
		t.Fatal(err)
	}
}

func TestReplaceBlockAndQuote(t *testing.T) {
	if got := replaceBlock("", nil); got != "" {
		t.Fatalf("%q", got)
	}
	got := replaceBlock("x\n\n", []string{"*.go merge=d"})
	if got != "x\n\n"+attrBegin+"\n*.go merge=d\n"+attrEnd+"\n" {
		t.Fatalf("%q", got)
	}
	if replaceBlock(got, []string{"*.go merge=d"}) != got {
		t.Fatal("stable")
	}
	if replaceBlock(got, nil) != "x\n" {
		t.Fatal("removed")
	}
	if q := shellQuote("/a b/it's"); q != `'/a b/it'\''s'` {
		t.Fatal(q)
	}
}

// A project rule for one file wins over Barnraise's pattern for its kind:
// info/attributes beats .gitattributes whatever the patterns, so the rule is
// written again after Barnraise's.
func TestProjectMergeRuleWinsOverStructuralPattern(t *testing.T) {
	ctx := context.Background()
	c := newConflictRepo(t, "MAIN", "ITEM")
	write(t, c.root, ".gitattributes", "docs/log.md merge=union\n")
	bin := fakeStructural(t, `*.md merge=mergiraf\n`)
	if _, err := c.repo.SetupMerge(ctx, MergeSetup{Structural: "auto", StructuralBin: bin}); err != nil {
		t.Fatal(err)
	}
	if got := g(t, c.root, "check-attr", "merge", "docs/log.md"); !strings.HasSuffix(got, "merge: union") {
		t.Fatalf("docs/log.md: %q", got)
	}
	if got := g(t, c.root, "check-attr", "merge", "README.md"); !strings.HasSuffix(got, "merge: "+StructuralDriver) {
		t.Fatalf("README.md: %q", got)
	}
}
