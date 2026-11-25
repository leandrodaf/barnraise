package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// DocItem is what a document template sees of an item.
type DocItem struct {
	ID, Title, Kind, Priority, Area, Goal, TrackerRef, Step, Status, Body, Branch, PR string
}

// DocProposal is what the item template sees of the proposal it comes from.
type DocProposal struct {
	ID, Title, Kind, Problem, Evidence, Impact, Goal, Area string
}

// DocData is the data every document template is given. Fields that do not
// apply to a document are empty.
type DocData struct {
	Project  string
	Language string
	Item     DocItem
	// Criteria is the checklist of the item's criteria, one "- [ ] C1: …" per line.
	Criteria string
	Preserve []string
	Tweaks   []string
	// Plan is plan.md of the item's worktree, when there is one.
	Plan string
	// Diagrams are the Mermaid blocks of plan.md, with the heading above each.
	Diagrams []domain.Diagram
	// DiagramsMode is documents.diagrams: auto, always or off.
	DiagramsMode string
	Proposal     DocProposal
	// Default is the built-in text of the document, to wrap or extend.
	Default string
}

// DocSource says where the template of a document comes from.
type DocSource struct {
	Name string `json:"name"`
	// Path is the project file in use; "" for the built-in template.
	Path string `json:"path,omitempty"`
	// Variants are the per-area and per-kind files the project has.
	Variants []string `json:"variants,omitempty"`
}

var docFuncs = template.FuncMap{
	"bullets": func(xs []string) string {
		if len(xs) == 0 {
			return ""
		}
		return "- " + strings.Join(xs, "\n- ")
	},
	"join":    strings.Join,
	"trim":    strings.TrimSpace,
	"lower":   strings.ToLower,
	"upper":   strings.ToUpper,
	"section": domain.MarkdownSection,
	"mermaid": func(d domain.Diagram) string { return "```mermaid\n" + d.Source + "\n```" },
	"default": func(def, v string) string {
		if strings.TrimSpace(v) == "" {
			return def
		}
		return v
	},
}

func diagramsMode(cfg *domain.ProjectConfig) string {
	if m := cfg.Documents.Diagrams; domain.ValidDiagrams(m) {
		return m
	}
	return domain.DiagramsAuto
}

// docCandidates lists the project files that may hold a document, most
// specific first: <name>.<area>.md, <name>.<kind>.md, <name>.md.
func docCandidates(root, name, area, kind string) []string {
	dir := filepath.Join(root, filepath.FromSlash(domain.DocumentsDir))
	var out []string
	for _, v := range []string{domain.AreaName(area), kind} {
		if v != "" && !strings.ContainsAny(v, `/\.`) {
			out = append(out, filepath.Join(dir, name+"."+v+".md"))
		}
	}
	return append(out, filepath.Join(dir, name+".md"))
}

// docTemplate returns the template of a document for an item's area and
// kind, and the file it came from ("" for the built-in one).
func docTemplate(root, name, area, kind string) (string, string, error) {
	for _, f := range docCandidates(root, name, area, kind) {
		if b, err := os.ReadFile(f); err == nil {
			return string(b), f, nil
		}
	}
	b, err := domain.BuiltInDocument(name)
	return b, "", err
}

// DocumentFile is the project file a document of an item of this area and
// kind is written from; "" when the built-in template is used.
func DocumentFile(root, name, area, kind string) string {
	_, src, _ := docTemplate(root, name, area, kind)
	return src
}

func execDoc(name, text string, d DocData) (string, error) {
	t, err := template.New(name).Funcs(docFuncs).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, d); err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()) + "\n", nil
}

// ErrDocument is a project document template that does not parse or run.
var ErrDocument = errors.New("document template")

// RenderDocument renders a document for the project. A project template that
// fails falls back on the built-in one, so a mistake there never stops a pull
// request from opening; the error is returned alongside for the log.
func RenderDocument(cfg *domain.ProjectConfig, name string, d DocData) (string, error) {
	d.Project, d.Language, d.DiagramsMode = cfg.Name, cfg.Language, diagramsMode(cfg)
	text, src, err := docTemplate(cfg.Root, name, d.Item.Area, d.Item.Kind)
	if err != nil {
		return "", err
	}
	out, err := execDoc(name, text, d)
	if err == nil || src == "" {
		return out, err
	}
	b, _ := domain.BuiltInDocument(name)
	fallback, ferr := execDoc(name, b, d)
	if ferr != nil {
		return "", ferr
	}
	return fallback, fmt.Errorf("%w %s: %v (the built-in one was used)", ErrDocument, src, err)
}

// DocumentSources lists the documents and the files a project uses for them.
func DocumentSources(root string) []DocSource {
	dir := filepath.Join(root, filepath.FromSlash(domain.DocumentsDir))
	ents, _ := os.ReadDir(dir)
	var out []DocSource
	for _, n := range domain.DocumentNames {
		s := DocSource{Name: n}
		if _, err := os.Stat(filepath.Join(dir, n+".md")); err == nil {
			s.Path = filepath.ToSlash(filepath.Join(domain.DocumentsDir, n+".md"))
		}
		for _, e := range ents {
			if v, ok := strings.CutPrefix(e.Name(), n+"."); ok && v != "md" && strings.HasSuffix(v, ".md") {
				s.Variants = append(s.Variants, filepath.ToSlash(filepath.Join(domain.DocumentsDir, e.Name())))
			}
		}
		out = append(out, s)
	}
	return out
}

// SampleDocData is an item made up to try document templates with.
func SampleDocData() DocData {
	plan := "# Plan\n\n## Steps\n1. Move the cache in front of the store.\n\n## Before and after\n\n### Before\n```mermaid\nflowchart LR\n  API --> Store\n```\n\n### After\n```mermaid\nflowchart LR\n  API --> Cache --> Store\n```\n"
	return DocData{
		Item: DocItem{ID: "I-42", Title: "Cache the price lookups", Kind: "improvement", Priority: "P1", Area: "api", Goal: "G-1",
			Step: "implement", Status: "in_progress", Body: "Price lookups hit the store on every request.", Branch: "barnraise/I-42"},
		Criteria: "- [x] C1: WHEN a price is asked twice, THE SYSTEM SHALL read the store once (check: unit)\n",
		Preserve: []string{"prices are never older than one minute"},
		Plan:     plan, Diagrams: domain.FindDiagrams(plan),
		Proposal: DocProposal{ID: "P-7", Title: "Cache the price lookups", Kind: "improvement", Problem: "Every request reads the store.",
			Evidence: "p95 of /price is 800ms", Impact: "minor", Goal: "faster checkout"},
	}
}

// CheckDocuments renders every project document template with a sample item
// and reports the ones that fail.
func CheckDocuments(cfg *domain.ProjectConfig) map[string]error {
	out := map[string]error{}
	dir := filepath.Join(cfg.Root, filepath.FromSlash(domain.DocumentsDir))
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		base, ok := strings.CutSuffix(e.Name(), ".md")
		if !ok || e.IsDir() {
			continue
		}
		name, _, _ := strings.Cut(base, ".")
		if !slices.Contains(domain.DocumentNames, name) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err == nil {
			_, err = execDoc(name, string(b), previewData(cfg, name))
		}
		out[filepath.ToSlash(filepath.Join(domain.DocumentsDir, e.Name()))] = err
	}
	return out
}

// itemDocData fills the document data of an item; plan is plan.md or "".
func itemDocData(it *domain.Item, plan string) DocData {
	d := DocData{
		Item: DocItem{ID: it.ID, Title: it.Title, Kind: it.Kind, Priority: it.Priority.String(), Area: it.Area, Goal: it.GoalID,
			TrackerRef: it.TrackerRef, Step: it.Step, Status: string(it.Status), Body: it.Body, Branch: it.Branch, PR: it.PR},
		Criteria: it.CriteriaChecklist(), Preserve: it.Preserve, Tweaks: it.Tweaks, Plan: plan,
	}
	if plan != "" {
		d.Diagrams = domain.FindDiagrams(plan)
	}
	return d
}

// issueCtx carries the project's issue document to a tracker call, when the
// project has one; the built-in body is used otherwise.
func (s *Service) issueCtx(ctx context.Context, p *Project, it *domain.Item) context.Context {
	if _, src, _ := docTemplate(p.Cfg().Root, domain.DocIssue, it.Area, it.Kind); src == "" {
		return ctx
	}
	return ports.WithIssueBody(ctx, func(builtIn string) string {
		plan, _ := os.ReadFile(filepath.Join(s.WorktreeDir(p.Name, it.ID), "plan.md"))
		d := itemDocData(it, string(plan))
		d.Default = builtIn
		body, err := RenderDocument(p.Cfg(), domain.DocIssue, d)
		if err != nil {
			s.d.Log.Warn("issue document", "item", it.ID, "err", err)
		}
		return body
	})
}
