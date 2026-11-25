package domain

import "testing"

func TestFindDiagrams(t *testing.T) {
	md := "# Plan\n\n```bash\n# not a heading\n```\n\n## Before and after\n\n### Before\n```mermaid\nflowchart LR\n  A --> B\n```\n\n### After\n~~~~ Mermaid\nflowchart LR\n  A --> C\n  C --> B\n~~~~\n\n```go\nx := 1\n```\n"
	ds := FindDiagrams(md)
	if len(ds) != 2 {
		t.Fatalf("got %d diagrams: %+v", len(ds), ds)
	}
	if ds[0].Title != "Before" || ds[0].Source != "flowchart LR\n  A --> B" {
		t.Errorf("first = %+v", ds[0])
	}
	if ds[1].Title != "After" || ds[1].Source != "flowchart LR\n  A --> C\n  C --> B" {
		t.Errorf("second = %+v", ds[1])
	}
	if got := FindDiagrams("```mermaid\nflowchart LR\n"); len(got) != 0 {
		t.Errorf("an unclosed block is no diagram: %+v", got)
	}
}

func TestMarkdownSection(t *testing.T) {
	md := "# Plan\n\n## Risks\nThe cache may serve old prices.\n\n### Detail\n```sh\n# comment\n```\n\n## Steps\n1. Do it\n"
	if got := MarkdownSection(md, "risks"); got != "The cache may serve old prices.\n\n### Detail\n```sh\n# comment\n```" {
		t.Errorf("risks = %q", got)
	}
	if got := MarkdownSection(md, "Steps"); got != "1. Do it" {
		t.Errorf("steps = %q", got)
	}
	if got := MarkdownSection(md, "Missing"); got != "" {
		t.Errorf("missing = %q", got)
	}
}
