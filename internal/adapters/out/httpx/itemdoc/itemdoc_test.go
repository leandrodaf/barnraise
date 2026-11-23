package itemdoc

import (
	"slices"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func item() *domain.Item {
	return &domain.Item{
		ID: "I-42", Title: " Remember the last email ", Body: "Users retype it.", Kind: "feature", Priority: domain.P1,
		Area: "auth", Size: "2h-6h", GoalID: "G-3", Status: domain.StatusReady,
		Criteria: []domain.Criterion{
			{ID: "C1", Text: "WHEN the app opens, THE SYSTEM SHALL fill the last email", Check: "LoginTest", Met: true},
			{ID: "C2", Text: "WHEN logout, THE SYSTEM SHALL forget it", Check: "LogoutTest"},
		},
	}
}

func TestTitleAndParse(t *testing.T) {
	if got := Title(item()); got != "[I-42] Remember the last email" {
		t.Errorf("title = %q", got)
	}
	id, rest := ParseTitle("[i-42]  Remember")
	if id != "I-42" || rest != "Remember" {
		t.Errorf("parse = %q %q", id, rest)
	}
	if id, rest := ParseTitle("Plain"); id != "" || rest != "Plain" {
		t.Errorf("plain = %q %q", id, rest)
	}
}

func TestMarkdownLanguages(t *testing.T) {
	en := Markdown(item(), "en")
	for _, want := range []string{"## Goal\n\nUsers retype it.\n\nPart of G-3.", "## Criteria", "- [x] C1: WHEN the app opens", "- [ ] C2:", "(check: LogoutTest)", "## Area\n\nauth", "## Size\n\n2h-6h", "Synced by Barnraise from I-42"} {
		if !strings.Contains(en, want) {
			t.Errorf("en misses %q:\n%s", want, en)
		}
	}
	if strings.Contains(en, "What must not change") {
		t.Error("feature has a preserve section")
	}
	bug := item()
	bug.Kind = "bug"
	bug.Preserve = []string{"Password is never stored"}
	pt := Markdown(bug, "pt-BR")
	for _, want := range []string{"## Objetivo", "Parte de G-3.", "## Critérios", "(verificação: LoginTest)", "## Área", "## Tamanho", "## O que não pode mudar\n\n- Password is never stored", "Sincronizado pelo Barnraise"} {
		if !strings.Contains(pt, want) {
			t.Errorf("pt misses %q:\n%s", want, pt)
		}
	}
}

func TestLabels(t *testing.T) {
	if got := Labels(item()); !slices.Equal(got, []string{"barnraise", "area:auth", "kind:feature", "P1"}) {
		t.Errorf("labels = %v", got)
	}
	merged := MergeLabels([]string{"needs-design", "status:ready", "P3", "area:old", "barnraise", "needs-design"}, []string{"barnraise", "P1", "status:queued"})
	if !slices.Equal(merged, []string{"needs-design", "barnraise", "P1", "status:queued"}) {
		t.Errorf("merged = %v", merged)
	}
	if StatusFromLabels([]string{"bug", "status:blocked"}) != "blocked" || StatusFromLabels(nil) != "" {
		t.Error("status from labels")
	}
}

func TestSameName(t *testing.T) {
	if !SameName("In Progress", "in_progress") || !SameName("to-do", "To Do") || SameName("Done", "Review") {
		t.Error("SameName")
	}
	if Humanize("waiting_owner") != "Waiting owner" {
		t.Error("Humanize")
	}
	if !Portuguese("pt-BR") || !Portuguese("PT") || Portuguese("en") || Portuguese("") {
		t.Error("Portuguese")
	}
}
