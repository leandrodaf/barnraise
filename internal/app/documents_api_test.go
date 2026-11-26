package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// reader may read the project but not approve: it cannot change templates.
func reader() Actor {
	return Actor{Kind: ActorToken, Name: "viewer", Scopes: []string{ScopeItemsRead}}
}

func TestDocumentsAPI(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.root, filepath.FromSlash(domain.DocumentsDir))

	// Nothing written yet: every document, built-in only.
	v, err := e.svc.ListDocuments("demo", reader())
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Documents) != len(domain.DocumentNames) || v.Dir != domain.DocumentsDir {
		t.Fatalf("list: %+v", v)
	}
	for _, d := range v.Documents {
		if d.BuiltIn == "" || len(d.Files) != 0 {
			t.Fatalf("%s: built-in %d bytes, files %v", d.Name, len(d.BuiltIn), d.Files)
		}
	}
	for _, k := range []string{"bug", "feature", "improvement"} {
		if !strings.Contains(strings.Join(v.Kinds, " "), k) {
			t.Fatalf("kinds %v lack %s", v.Kinds, k)
		}
	}

	// The owner saves a general pull request template and one for bugs.
	general := "## {{.Item.Title}}\n\nGeneral.\n"
	f, err := e.svc.SaveDocument(e.ctx, "demo", owner(), domain.DocPR, "", general)
	if err != nil {
		t.Fatal(err)
	}
	if f.Path != domain.DocumentsDir+"/pr.md" || f.Variant != "" {
		t.Fatalf("saved %+v", f)
	}
	if _, err := e.svc.SaveDocument(e.ctx, "demo", owner(), domain.DocPR, " bug ", "Bug {{.Item.ID}}\n"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "pr.bug.md")); string(b) != "Bug {{.Item.ID}}\n" {
		t.Fatalf("pr.bug.md = %q", b)
	}
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a template"), 0o644)
	v, _ = e.svc.ListDocuments("demo", reader())
	for _, d := range v.Documents {
		if d.Name != domain.DocPR {
			continue
		}
		if len(d.Files) != 2 || d.Files[0].Variant != "" || d.Files[1].Variant != "bug" || d.Files[0].Text != general {
			t.Fatalf("pr files: %+v", d.Files)
		}
	}
	if src := DocumentSources(e.root); src[0].Name != domain.DocPR || src[0].Path == "" || len(src[0].Variants) != 1 {
		t.Fatalf("sources: %+v", src)
	}
	if got := DocumentFile(e.root, domain.DocPR, "api", "bug"); filepath.Base(got) != "pr.bug.md" {
		t.Fatalf("bug item uses %q", got)
	}

	// What is refused: a reader, an unknown document, a bad variant, a
	// template that does not render, one too big.
	for name, call := range map[string]func() error{
		"reader": func() error {
			_, err := e.svc.SaveDocument(e.ctx, "demo", reader(), domain.DocPR, "", general)
			return err
		},
		"unknown": func() error {
			_, err := e.svc.SaveDocument(e.ctx, "demo", owner(), "readme", "", general)
			return err
		},
		"variant": func() error {
			_, err := e.svc.SaveDocument(e.ctx, "demo", owner(), domain.DocPR, "../x", general)
			return err
		},
		"broken": func() error {
			_, err := e.svc.SaveDocument(e.ctx, "demo", owner(), domain.DocPR, "", "{{.Nope}}")
			return err
		},
		"big": func() error {
			_, err := e.svc.SaveDocument(e.ctx, "demo", owner(), domain.DocPR, "", strings.Repeat("x", maxDocumentSize+1))
			return err
		},
		"project": func() error {
			_, err := e.svc.ListDocuments("nope", owner())
			return err
		},
	} {
		if call() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := e.svc.SaveDocument(e.ctx, "demo", reader(), domain.DocPR, "", general); !errors.Is(err, ErrForbidden) {
		t.Fatalf("reader save: %v", err)
	}

	// Preview renders with a sample item, or says why it does not.
	p, err := e.svc.PreviewDocument("demo", reader(), domain.DocPR, "{{.Item.Area}}/{{.Item.Kind}}", "web", "bug")
	if err != nil || strings.TrimSpace(p.Text) != "web/bug" || p.Error != "" {
		t.Fatalf("preview: %+v %v", p, err)
	}
	if p, err := e.svc.PreviewDocument("demo", reader(), domain.DocIssue, "{{.Nope}}", "", ""); err != nil || p.Error == "" {
		t.Fatalf("broken preview: %+v %v", p, err)
	}
	if _, err := e.svc.PreviewDocument("demo", reader(), "readme", "x", "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown preview: %v", err)
	}
	if _, err := e.svc.PreviewDocument("demo", reader(), domain.DocPR, strings.Repeat("x", maxDocumentSize+1), "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("big preview: %v", err)
	}

	// An item's documents come from the bug template.
	it := e.create(NewItem{Title: "Fix the cart", Kind: "bug", Area: "web"})
	d, err := e.svc.ItemDocuments("demo", reader(), it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.PRBody != "Bug "+it.ID+"\n" || d.Template != domain.DocumentsDir+"/pr.bug.md" || d.Diagrams == nil || d.Warning != "" {
		t.Fatalf("item documents: %+v", d)
	}
	if _, err := e.svc.ItemDocuments("demo", reader(), "I-999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing item: %v", err)
	}
	if _, err := e.svc.ItemDocuments("demo", reader(), "not an id"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad id: %v", err)
	}

	// A template broken on disk falls back to the built-in one, with a warning.
	_ = os.WriteFile(filepath.Join(dir, "pr.bug.md"), []byte("{{.Nope}}"), 0o644)
	if d, _ := e.svc.ItemDocuments("demo", reader(), it.ID); d.Warning == "" || d.PRBody == "" {
		t.Fatalf("broken template: %+v", d)
	}

	// Removing the bug variant goes back to the general file; twice is not found.
	if err := e.svc.DeleteDocument(e.ctx, "demo", owner(), domain.DocPR, "bug"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteDocument(e.ctx, "demo", owner(), domain.DocPR, "bug"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if err := e.svc.DeleteDocument(e.ctx, "demo", reader(), domain.DocPR, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("reader delete: %v", err)
	}
	if err := e.svc.DeleteDocument(e.ctx, "demo", owner(), "readme", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown delete: %v", err)
	}
	if got := DocumentFile(e.root, domain.DocPR, "api", "bug"); filepath.Base(got) != "pr.md" {
		t.Fatalf("after delete the bug item uses %q", got)
	}
	if filepathRel(e.root, "") != "" || mustRel("rel", "/abs") != "/abs" {
		t.Fatal("path helpers")
	}
}
