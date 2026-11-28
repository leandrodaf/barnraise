package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

func TestMeasureDiff(t *testing.T) {
	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1,2 @@\n-x\n+y\n+z\n" +
		"diff --git a/art/hero.png b/art/hero.png\nBinary files differ\n" +
		"diff --git a/plan.md b/plan.md\n+++ b/plan.md\n+plan\n"
	got := measureDiff(diff, domain.MaxChange{Files: 1, Exclude: []string{"art/**"}})
	if got.Files != 1 || got.Lines != 3 {
		t.Fatalf("%+v", got)
	}
	if overMaxChange(ChangeSize{Files: 3, Lines: 10}, domain.MaxChange{Files: 2}) == "" || overMaxChange(ChangeSize{Files: 2}, domain.MaxChange{Files: 2}) != "" {
		t.Fatal("limit")
	}
}

// A delivery past items.max_change is refused with what to do; one within
// it goes on. Excluded files are not counted.
func TestDeliveryPastMaxChangeIsRefused(t *testing.T) {
	e := newEnv(t, withGit(), withCfg(func(c *domain.ProjectConfig) {
		c.MaxChange = domain.MaxChange{Files: 2, Exclude: []string{"art/**"}}
	}))
	it := e.ready("simple", "Big change", "")
	e.next("implementer", it.ID)
	e.workspace(it.ID, map[string]string{"a.go": "a\n", "b.go": "b\n", "c.go": "c\n", "art/x.png": "png"})
	_, err := e.deliver(it.ID, domain.Delivery{})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "3 files (the limit is 2)") || !strings.Contains(err.Error(), "propose") {
		t.Fatalf("over the limit: %v", err)
	}
	dir := e.svc.WorktreeDir("demo", it.ID)
	testkit.RunGit(t, dir, "rm", "-q", "c.go")
	if _, err := e.deliver(it.ID, domain.Delivery{}); err != nil {
		t.Fatalf("within the limit (art not counted): %v", err)
	}
}
