package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func TestFilterDiff(t *testing.T) {
	d := "diff --git a/hello.txt b/hello.txt\n+hi\ndiff --git a/plan.md b/plan.md\n+# plan\ndiff --git a/criteria.json b/criteria.json\n+{}\ndiff --git a/x.go b/x.go\n+x\n"
	out := filterDiff(d, ItemFiles)
	if strings.Contains(out, "plan") || strings.Contains(out, "criteria") || !strings.Contains(out, "hello.txt") || !strings.Contains(out, "x.go") {
		t.Fatal(out)
	}
}

// The conventions name the commit header's limit as a number: it must be
// the one deliver checks.
func TestConventionsNameTheHeaderLimit(t *testing.T) {
	if !strings.Contains(barnraiseConventions, fmt.Sprintf("fits in %d characters", domain.HeaderMax)) {
		t.Fatalf("the conventions do not say the header limit of %d", domain.HeaderMax)
	}
}
