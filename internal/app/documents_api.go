package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// DocumentFileView is one template file of a document: the general one or a
// variant for an area or kind.
type DocumentFileView struct {
	// Variant is the area or kind it applies to; "" for the general file.
	Variant string `json:"variant"`
	Path    string `json:"path"`
	Text    string `json:"text"`
}

// DocumentView is a document, its built-in template and the project's files.
type DocumentView struct {
	Name    string             `json:"name"`
	BuiltIn string             `json:"built_in"`
	Files   []DocumentFileView `json:"files"`
}

// DocumentsView is what the panel shows of a project's documents.
type DocumentsView struct {
	Documents []DocumentView `json:"documents"`
	Diagrams  string         `json:"diagrams"`
	Areas     []string       `json:"areas"`
	Kinds     []string       `json:"kinds"`
	Dir       string         `json:"dir"`
}

// ItemDocumentsView is what an item's documents look like now: its plan,
// the diagrams in it and the pull request body Barnraise writes.
type ItemDocumentsView struct {
	Plan     string           `json:"plan"`
	Diagrams []domain.Diagram `json:"diagrams"`
	PRBody   string           `json:"pr_body"`
	// Template is the project file the pull request is written from; "" for the built-in one.
	Template string `json:"template,omitempty"`
	Warning  string `json:"warning,omitempty"`
}

// DocumentPreview is a template rendered with a sample item.
type DocumentPreview struct {
	Text  string `json:"text"`
	Error string `json:"error,omitempty"`
}

const maxDocumentSize = 64 << 10

// ListDocuments returns the documents of a project with their templates.
func (s *Service) ListDocuments(project string, a Actor) (*DocumentsView, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeItemsRead, p.Name); err != nil {
		return nil, err
	}
	cfg := p.Cfg()
	dir := filepath.Join(cfg.Root, filepath.FromSlash(domain.DocumentsDir))
	ents, _ := os.ReadDir(dir)
	out := &DocumentsView{Diagrams: diagramsMode(cfg), Areas: sortedKeys(cfg.Areas), Dir: domain.DocumentsDir}
	kinds := map[string]bool{"feature": true, "bug": true, "improvement": true}
	for k := range cfg.KindFlows {
		kinds[k] = true
	}
	out.Kinds = sortedKeys(kinds)
	for _, n := range domain.DocumentNames {
		builtIn, _ := domain.BuiltInDocument(n)
		v := DocumentView{Name: n, BuiltIn: builtIn, Files: []DocumentFileView{}}
		for _, e := range ents {
			base, ok := strings.CutSuffix(e.Name(), ".md")
			if !ok || e.IsDir() {
				continue
			}
			name, variant, _ := strings.Cut(base, ".")
			if name != n {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			v.Files = append(v.Files, DocumentFileView{Variant: variant, Path: domain.DocumentsDir + "/" + e.Name(), Text: string(b)})
		}
		slices.SortFunc(v.Files, func(a, b DocumentFileView) int { return strings.Compare(a.Variant, b.Variant) })
		out.Documents = append(out.Documents, v)
	}
	return out, nil
}

func documentPath(root, name, variant string) (string, error) {
	if !slices.Contains(domain.DocumentNames, name) {
		return "", fmt.Errorf("%w: unknown document %q", ErrInvalid, name)
	}
	file := name + ".md"
	if variant = strings.TrimSpace(variant); variant != "" {
		if strings.ContainsAny(variant, "/\\. \t") || len(variant) > 64 {
			return "", fmt.Errorf("%w: invalid area or kind %q", ErrInvalid, variant)
		}
		file = name + "." + variant + ".md"
	}
	return filepath.Join(root, filepath.FromSlash(domain.DocumentsDir), file), nil
}

// SaveDocument writes a project template of a document after checking it
// renders. The owner commits it so every machine uses it.
func (s *Service) SaveDocument(ctx context.Context, project string, a Actor, name, variant, text string) (*DocumentFileView, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeApprove, p.Name); err != nil {
		return nil, err
	}
	if len(text) > maxDocumentSize {
		return nil, fmt.Errorf("%w: a template is at most %d KiB", ErrInvalid, maxDocumentSize>>10)
	}
	path, err := documentPath(p.Cfg().Root, name, variant)
	if err != nil {
		return nil, err
	}
	if _, err := execDoc(name, text, previewData(p.Cfg(), name)); err != nil {
		return nil, fmt.Errorf("%w: the template does not render: %v", ErrInvalid, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return nil, err
	}
	s.d.Log.Info("document saved", "project", p.Name, "file", path, "by", actorName(a))
	return &DocumentFileView{Variant: strings.TrimSpace(variant), Path: filepath.ToSlash(mustRel(p.Cfg().Root, path)), Text: text}, nil
}

// DeleteDocument removes a project template: the document goes back to the
// general file, or to the built-in one.
func (s *Service) DeleteDocument(ctx context.Context, project string, a Actor, name, variant string) error {
	p, err := s.Project(project)
	if err != nil {
		return err
	}
	if err := a.Require(ScopeApprove, p.Name); err != nil {
		return err
	}
	path, err := documentPath(p.Cfg().Root, name, variant)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrNotFound, mustRel(p.Cfg().Root, path))
		}
		return err
	}
	s.d.Log.Info("document removed", "project", p.Name, "file", path, "by", actorName(a))
	return nil
}

// PreviewDocument renders a template text with a sample item, as the
// project's settings would.
func (s *Service) PreviewDocument(project string, a Actor, name, text, area, kind string) (*DocumentPreview, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeItemsRead, p.Name); err != nil {
		return nil, err
	}
	if !slices.Contains(domain.DocumentNames, name) {
		return nil, fmt.Errorf("%w: unknown document %q", ErrInvalid, name)
	}
	if len(text) > maxDocumentSize {
		return nil, fmt.Errorf("%w: a template is at most %d KiB", ErrInvalid, maxDocumentSize>>10)
	}
	d := previewData(p.Cfg(), name)
	if area != "" {
		d.Item.Area = area
	}
	if kind != "" {
		d.Item.Kind = kind
	}
	out, err := execDoc(name, text, d)
	if err != nil {
		return &DocumentPreview{Text: out, Error: err.Error()}, nil
	}
	return &DocumentPreview{Text: out}, nil
}

// ItemDocuments renders an item's documents as they are now.
func (s *Service) ItemDocuments(project string, a Actor, id string) (*ItemDocumentsView, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeItemsRead, p.Name); err != nil {
		return nil, err
	}
	nid, err := domain.NormalizeItemID(id)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	it := p.Snapshot().Items[nid]
	if it == nil {
		return nil, fmt.Errorf("%w: item %s", ErrNotFound, nid)
	}
	plan, _ := os.ReadFile(filepath.Join(s.WorktreeDir(p.Name, it.ID), "plan.md"))
	d := itemDocData(it, string(plan))
	out := &ItemDocumentsView{Plan: string(plan), Diagrams: d.Diagrams, Template: filepathRel(p.Cfg().Root, DocumentFile(p.Cfg().Root, domain.DocPR, it.Area, it.Kind))}
	if out.Diagrams == nil {
		out.Diagrams = []domain.Diagram{}
	}
	body, err := RenderDocument(p.Cfg(), domain.DocPR, d)
	out.PRBody = body
	if err != nil {
		out.Warning = err.Error()
	}
	return out, nil
}

func previewData(cfg *domain.ProjectConfig, name string) DocData {
	d := SampleDocData()
	d.Project, d.Language, d.DiagramsMode = cfg.Name, cfg.Language, diagramsMode(cfg)
	if name == domain.DocIssue {
		d.Default = "## Problem\n\nThe built-in issue body: problem, criteria and status.\n"
	}
	return d
}

func mustRel(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil {
		return r
	}
	return p
}

func filepathRel(root, p string) string {
	if p == "" {
		return ""
	}
	return filepath.ToSlash(mustRel(root, p))
}
