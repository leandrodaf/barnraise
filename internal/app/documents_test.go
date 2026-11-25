package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func docProject(t *testing.T, files map[string]string) *domain.ProjectConfig {
	t.Helper()
	root := t.TempDir()
	for name, text := range files {
		p := filepath.Join(root, filepath.FromSlash(domain.DocumentsDir), name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &domain.ProjectConfig{Name: "shop", Root: root, Documents: domain.DefaultDocuments()}
}

func TestBuiltInPRBodyCarriesThePlanDiagrams(t *testing.T) {
	cfg := docProject(t, nil)
	it := &domain.Item{ID: "I-7", Title: "Cache prices", Kind: "improvement", Area: "api", Step: "implement", Status: domain.StatusInProgress,
		Criteria: []domain.Criterion{{ID: "C1", Text: "WHEN asked twice, THE SYSTEM SHALL read once", Check: "unit", Met: true}},
		Preserve: []string{"prices are fresh"}}
	plan := "## Before and after\n### Before\n```mermaid\nflowchart LR\n  API --> Store\n```\n### After\n```mermaid\nflowchart LR\n  API --> Cache --> Store\n```\n"
	body, err := RenderDocument(cfg, domain.DocPR, itemDocData(it, plan))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Barnraise item **I-7** · kind improvement", " · area api", "### Criteria\n- [x] C1: WHEN asked twice",
		"### What must not change\n- prices are fresh", "### Before and after", "**Before**\n\n```mermaid\nflowchart LR\n  API --> Store\n```",
		"**After**\n\n```mermaid\nflowchart LR\n  API --> Cache --> Store\n```", "Step: `implement` · status: `in_progress`"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	noPlan, _ := RenderDocument(cfg, domain.DocPR, itemDocData(it, ""))
	if strings.Contains(noPlan, "Before and after") || strings.Contains(noPlan, "Tracker:") {
		t.Errorf("sections without content are left out:\n%s", noPlan)
	}
}

func TestProjectDocumentsWinByAreaThenKind(t *testing.T) {
	cfg := docProject(t, map[string]string{
		"pr.md":     "general {{.Item.ID}}",
		"pr.bug.md": "bug {{.Item.ID}}",
		"pr.web.md": "web {{.Item.ID}} {{section .Plan \"Risks\"}}",
	})
	cases := []struct{ area, kind, want string }{
		{"api", "feature", "general I-1\n"}, {"api", "bug", "bug I-1\n"}, {"web", "bug", "web I-1 slow\n"},
	}
	for _, c := range cases {
		got, err := RenderDocument(cfg, domain.DocPR, itemDocData(&domain.Item{ID: "I-1", Area: c.area, Kind: c.kind}, "## Risks\nslow\n"))
		if err != nil || got != c.want {
			t.Errorf("%s/%s = %q, %v; want %q", c.area, c.kind, got, err, c.want)
		}
	}
}

func TestABrokenProjectDocumentFallsBackOnTheBuiltIn(t *testing.T) {
	cfg := docProject(t, map[string]string{"pr.md": "{{.Item.Nope}}", "item.md": "{{if}}"})
	body, err := RenderDocument(cfg, domain.DocPR, itemDocData(&domain.Item{ID: "I-3"}, ""))
	if !errors.Is(err, ErrDocument) || !strings.Contains(body, "Barnraise item **I-3**") {
		t.Fatalf("body %q err %v", body, err)
	}
	res := CheckDocuments(cfg)
	if len(res) != 2 || res[".barnraise/documents/pr.md"] == nil || res[".barnraise/documents/item.md"] == nil {
		t.Errorf("check = %v", res)
	}
}

func TestPlanDocumentFollowsTheDiagramMode(t *testing.T) {
	cfg := docProject(t, nil)
	it := &domain.Item{ID: "I-9", Title: "Split the store"}
	for mode, want := range map[string]string{domain.DiagramsAlways: "Required.", domain.DiagramsAuto: "Only when the item changes", domain.DiagramsOff: ""} {
		cfg.Documents.Diagrams = mode
		got, err := RenderDocument(cfg, domain.DocPlan, itemDocData(it, ""))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, "# Plan for I-9: Split the store") || (want == "") != !strings.Contains(got, "Before and after") ||
			(want != "" && !strings.Contains(got, want)) {
			t.Errorf("%s:\n%s", mode, got)
		}
	}
}

func TestItemAndIssueDocuments(t *testing.T) {
	cfg := docProject(t, map[string]string{"issue.md": "{{.Default}}\nOwned by team {{.Item.Area}}"})
	body, err := RenderDocument(cfg, domain.DocItem, DocData{Proposal: DocProposal{ID: "P-2", Problem: "slow", Evidence: "p95", Impact: "minor", Goal: "speed"}})
	if err != nil || !strings.HasPrefix(body, "From proposal P-2.\n\nProblem: slow") || !strings.Contains(body, domain.ClarificationMarker) {
		t.Errorf("item = %q, %v", body, err)
	}
	d := itemDocData(&domain.Item{ID: "I-1", Area: "web"}, "")
	d.Default = "## Problem\nslow"
	issue, err := RenderDocument(cfg, domain.DocIssue, d)
	if err != nil || issue != "## Problem\nslow\nOwned by team web\n" {
		t.Errorf("issue = %q, %v", issue, err)
	}
}
