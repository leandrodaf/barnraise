package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitx "github.com/leandrodaf/barnraise/internal/adapters/out/git"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

// recordResolution resolves, in dir, the conflict of rebasing on main with
// content for file, the way an agent would, and puts dir back where it was:
// git keeps the resolution (rerere) and the merge queue meets the conflict
// again.
func recordResolution(t *testing.T, dir, file, content string) {
	t.Helper()
	orig := testkit.RunGit(t, dir, "rev-parse", "HEAD")
	testkit.RunGit(t, dir, "fetch", "-q", "origin")
	if out, err := testkit.GitOutput(dir, "rebase", "origin/main"); err == nil {
		t.Fatalf("expected a conflict: %s", out)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	testkit.RunGit(t, dir, "add", file)
	testkit.RunGit(t, dir, "-c", "core.editor=true", "rebase", "--continue")
	testkit.RunGit(t, dir, "reset", "-q", "--hard", orig)
}

func TestMergeQueueReappliesRecordedResolution(t *testing.T) {
	e := newEnv(t, withGit())
	if rep, err := e.repo.Git.SetupMerge(e.ctx, gitx.MergeSetup{Rerere: true, Structural: "off"}); err != nil || !rep.Rerere {
		t.Fatalf("setup %+v %v", rep, err)
	}
	it := e.toQueue("Readme", map[string]string{"README.md": "# demo by the item\n"})
	e.repo.PushToMain(t, "someone else", map[string]string{"README.md": "# demo by someone else\n"})
	dir := e.svc.WorktreeDir("demo", it.ID)
	recordResolution(t, dir, "README.md", "# demo by both\n")

	// First the combined result fails: the reason points at the reapplied file.
	e.checks.Script("main", testkit.CheckOutcome{Passed: false, Summary: "broken"}, testkit.Pass)
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	got := e.wantStatus(it.ID, domain.StatusInProgress, "implement")
	if r := got.Rework[0].Reason; !strings.Contains(r, "README.md") || !strings.Contains(r, "git rerere forget") {
		t.Fatalf("reason %q", r)
	}
	if !strings.Contains(e.types(it.ID), domain.EvConflictsReapplied) {
		t.Fatalf("events %s", e.types(it.ID))
	}

	// Back to the queue: git resolves the same conflict again, every local check runs, it lands.
	e.clock.Advance(time.Second)
	e.mustDeliver(it.ID, domain.Delivery{})
	e.drive(it.ID)
	e.wantStatus(it.ID, domain.StatusQueued, "integrate")
	before := len(e.checks.Specs())
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	e.wantStatus(it.ID, domain.StatusIntegrated, "")
	specs := e.checks.Specs()[before:]
	if len(specs) == 0 {
		t.Fatal("the checks run on a reapplied resolution")
	}
	for _, s := range specs {
		if s.Touched != nil {
			t.Fatalf("every check runs, not a selection: %+v", s)
		}
	}
	readme := testkit.RunGit(t, e.repo.Root, "--git-dir", e.repo.Remote, "show", "main:README.md")
	if readme != "# demo by both" {
		t.Fatalf("main README %q", readme)
	}
}

func TestReappliedHint(t *testing.T) {
	if reappliedHint(nil) != "" {
		t.Fatal("no files, no hint")
	}
}
