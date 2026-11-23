package app

import (
	"strings"
	"testing"
)

func TestFilterDiff(t *testing.T) {
	d := "diff --git a/hello.txt b/hello.txt\n+hi\ndiff --git a/plan.md b/plan.md\n+# plan\ndiff --git a/criteria.json b/criteria.json\n+{}\ndiff --git a/x.go b/x.go\n+x\n"
	out := filterDiff(d, ItemFiles)
	if strings.Contains(out, "plan") || strings.Contains(out, "criteria") || !strings.Contains(out, "hello.txt") || !strings.Contains(out, "x.go") {
		t.Fatal(out)
	}
}
