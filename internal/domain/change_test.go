package domain

import (
	"strings"
	"testing"
)

func TestChangeHeaderAndMessage(t *testing.T) {
	c := ChangeNote{Type: "feat", Scope: "api", Subject: "add CSV export of the statement", Body: "People asked for their data in a spreadsheet."}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Header() != "feat(api): add CSV export of the statement" {
		t.Fatal(c.Header())
	}
	want := "feat(api): add CSV export of the statement\n\nPeople asked for their data in a spreadsheet.\n\nBarnraise-Item: I-42"
	if got := c.CommitMessage("I-42"); got != want {
		t.Fatalf("message:\n%s", got)
	}
	b := ChangeNote{Type: "refactor", Subject: "drop the v1 statement endpoint", Breaking: "Clients must call /v2/statement"}
	if b.Header() != "refactor!: drop the v1 statement endpoint" {
		t.Fatal(b.Header())
	}
	if msg := b.CommitMessage("I-7"); !strings.Contains(msg, "\n\nBREAKING CHANGE: Clients must call /v2/statement\nBarnraise-Item: I-7") {
		t.Fatalf("breaking trailer:\n%s", msg)
	}
}

func TestChangeValidate(t *testing.T) {
	ok := []ChangeNote{
		{Type: "fix", Subject: "show the balance after a refund"},
		{Type: "feat", Scope: "mobile", Subject: "open the card from the Cards menu"},
		{Type: "docs", Subject: "explain how to pair a second machine"},
		{Type: "fix", Scope: "api", Subject: "do not round cents in the forecast"},
		{Type: "feat", Subject: "API tokens with a scope"},
		{Type: "chore", Scope: "deps", Subject: "bump example.com/client to v2"},
		{Type: "fix", Subject: "no longer lose events — even when a peer joins late"},
	}
	for _, c := range ok {
		if err := c.Validate(); err != nil {
			t.Errorf("%q should be accepted: %v", c.Header(), err)
		}
	}
	bad := map[string]ChangeNote{
		"unknown type":     {Type: "feature", Subject: "add export"},
		"empty subject":    {Type: "fix", Subject: " "},
		"final period":     {Type: "fix", Subject: "show the balance."},
		"capital":          {Type: "fix", Subject: "Show the balance"},
		"multi line":       {Type: "fix", Subject: "show the balance\nand more"},
		"bad scope":        {Type: "fix", Scope: "The API", Subject: "show the balance"},
		"too long":         {Type: "feat", Scope: "mobile", Subject: "open the card from the Cards menu and also from every other place where a card shows"},
		"accents":          {Type: "fix", Subject: "corrige o botão de exportação"},
		"portuguese words": {Type: "fix", Subject: "corrige a tela de login"},
		"portuguese plain": {Type: "feat", Subject: "exportar extrato para planilha"},
		"portuguese body":  {Type: "fix", Subject: "show the balance", Body: "O saldo não aparecia."},
		"portuguese break": {Type: "fix", Subject: "show the balance", Breaking: "quando o cliente usa a v1"},
	}
	for name, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: %q must be refused", name, c.Header())
		}
	}
	if err := (ChangeNote{Type: "fix", Subject: "corrige o botão"}).Validate(); err == nil || !strings.Contains(err.Error(), "English") {
		t.Fatalf("the refusal must say why: %v", err)
	}
}

func TestChangelogSectionAndEntry(t *testing.T) {
	for typ, want := range map[string]string{"feat": "Added", "fix": "Fixed", "perf": "Changed", "revert": "Removed",
		"refactor": "", "docs": "", "test": "", "build": "", "ci": "", "chore": "", "style": ""} {
		if got := (ChangeNote{Type: typ, Subject: "x"}).ChangelogSection(); got != want {
			t.Errorf("%s → %q, want %q", typ, got, want)
		}
	}
	if (ChangeNote{Type: "refactor", Subject: "x", Breaking: "y"}).ChangelogSection() != "Changed" || (ChangeNote{Type: "chore", Subject: "x", Breaking: "y"}).ChangelogSection() != "Changed" {
		t.Fatal("a breaking change is always in the changelog")
	}
	e := ChangeNote{Type: "feat", Scope: "api", Subject: "add CSV export of the statement"}.ChangelogEntry("I-42")
	if e != "**api:** Add CSV export of the statement (I-42)" {
		t.Fatal(e)
	}
	b := ChangeNote{Type: "feat", Subject: "drop the v1 endpoint", Breaking: "Call /v2/statement."}.ChangelogEntry("I-7")
	if b != "**Breaking:** Drop the v1 endpoint. Call /v2/statement (I-7)" {
		t.Fatal(b)
	}
}

func TestDefaultChange(t *testing.T) {
	c := DefaultChangeNote(&Item{ID: "I-1", Kind: "bug", Area: "Wallet Export", Title: "Show the balance after a refund."})
	if c.Header() != "fix(wallet-export): show the balance after a refund" || c.Validate() != nil {
		t.Fatalf("%q %v", c.Header(), c.Validate())
	}
	if DefaultChangeNote(&Item{Kind: "feature", Title: "API tokens"}).Header() != "feat: API tokens" {
		t.Fatal("an acronym keeps its capitals")
	}
	if DefaultChangeNote(&Item{Kind: "improvement", Title: "Tidy"}).Type != "refactor" || DefaultChangeNote(&Item{Kind: "docs", Title: "Guide"}).Type != "docs" || DefaultChangeNote(&Item{Kind: "odd", Title: "X y"}).Type != "chore" {
		t.Fatal("types from kinds")
	}
	long := DefaultChangeNote(&Item{Kind: "feature", Area: "mobile", Title: strings.Repeat("very long title ", 10)})
	if len(long.Header()) > HeaderMax {
		t.Fatalf("header %d", len(long.Header()))
	}
	// A title that is not English cannot be used as it is: the agent must describe the change.
	if err := DefaultChangeNote(&Item{Kind: "bug", Title: "Corrigir o botão de exportação"}).Validate(); err == nil {
		t.Fatal("a Portuguese title must not become a commit")
	}
}

func TestInsertChangelog(t *testing.T) {
	// A new file.
	got := InsertChangelog("", "Added", "Add CSV export (I-1)")
	if !strings.HasPrefix(got, "# Changelog\n") || !strings.Contains(got, "keepachangelog.com") ||
		!strings.HasSuffix(got, "## [Unreleased]\n\n### Added\n\n- Add CSV export (I-1)\n") {
		t.Fatalf("new file:\n%s", got)
	}
	// More entries: same section appends, sections keep the canonical order.
	got = InsertChangelog(got, "Fixed", "Show the balance (I-2)")
	got = InsertChangelog(got, "Added", "Add PDF export (I-3)")
	got = InsertChangelog(got, "Changed", "Faster sync (I-4)")
	want := "## [Unreleased]\n\n### Added\n\n- Add CSV export (I-1)\n- Add PDF export (I-3)\n\n### Changed\n\n- Faster sync (I-4)\n\n### Fixed\n\n- Show the balance (I-2)\n"
	if !strings.HasSuffix(got, want) {
		t.Fatalf("sections:\n%s", got)
	}
	// The same entry twice changes nothing.
	if again := InsertChangelog(got, "Added", "Add CSV export (I-1)"); again != got {
		t.Fatalf("duplicate entry:\n%s", again)
	}
	// An existing file with releases: Unreleased goes above them, the rest is untouched.
	old := "# Changelog\n\nNotes.\n\n## [1.2.0] - 2026-01-05\n\n### Added\n\n- Old thing\n\n## [1.1.0] - 2025-12-01\n\n### Fixed\n\n- Older thing\n"
	got = InsertChangelog(old, "Fixed", "New fix (I-9)")
	want = "# Changelog\n\nNotes.\n\n## [Unreleased]\n\n### Fixed\n\n- New fix (I-9)\n\n## [1.2.0] - 2026-01-05\n\n### Added\n\n- Old thing\n\n## [1.1.0] - 2025-12-01\n\n### Fixed\n\n- Older thing\n"
	if got != want {
		t.Fatalf("with releases:\n%s", got)
	}
	// An Unreleased that already has the section, followed by a release.
	got = InsertChangelog(got, "Fixed", "Second fix (I-10)")
	if !strings.Contains(got, "### Fixed\n\n- New fix (I-9)\n- Second fix (I-10)\n\n## [1.2.0]") {
		t.Fatalf("append in place:\n%s", got)
	}
	got = InsertChangelog(got, "Added", "A feature (I-11)")
	if !strings.Contains(got, "## [Unreleased]\n\n### Added\n\n- A feature (I-11)\n\n### Fixed\n\n- New fix (I-9)\n- Second fix (I-10)\n\n## [1.2.0]") {
		t.Fatalf("new section before a later one:\n%s", got)
	}
	// An empty Unreleased heading.
	got = InsertChangelog("# Changelog\n\n## [Unreleased]\n\n## [1.0.0]\n\n- first\n", "Security", "Rotate the keys (I-12)")
	if got != "# Changelog\n\n## [Unreleased]\n\n### Security\n\n- Rotate the keys (I-12)\n\n## [1.0.0]\n\n- first\n" {
		t.Fatalf("empty unreleased:\n%s", got)
	}
}
