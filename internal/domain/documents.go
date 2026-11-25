package domain

import (
	"embed"
	"fmt"
	"slices"
	"strings"
)

// Document templates are the texts Barnraise writes for people: the body of
// a pull request, the structure of plan.md, the body of an item made from a
// proposal and of the issue in the tracker. Each has a built-in version and
// may be replaced by the project in .barnraise/documents/<name>.md, or per
// area or kind with <name>.<area>.md and <name>.<kind>.md.
const (
	DocPR    = "pr"
	DocPlan  = "plan"
	DocItem  = "item"
	DocIssue = "issue"
)

// DocumentNames lists the documents a project may rewrite.
var DocumentNames = []string{DocPR, DocPlan, DocItem, DocIssue}

//go:embed documents/*.md
var builtInDocuments embed.FS

// BuiltInDocument returns the built-in template of a document.
func BuiltInDocument(name string) (string, error) {
	if !slices.Contains(DocumentNames, name) {
		return "", fmt.Errorf("unknown document %q", name)
	}
	b, err := builtInDocuments.ReadFile("documents/" + name + ".md")
	return string(b), err
}

// DocumentsDir is where a project keeps its document templates, under the root.
const DocumentsDir = ".barnraise/documents"

// Diagram modes: when plan.md carries a before-and-after diagram.
const (
	DiagramsAuto   = "auto"   // when the change alters a flow or structure that already exists
	DiagramsAlways = "always" // every plan; the analyst sends back a plan without them
	DiagramsOff    = "off"
)

// DocumentsDef configures the documents Barnraise writes.
type DocumentsDef struct {
	// Diagrams decides when plan.md draws the design before and after the
	// change (Mermaid), which the pull request then shows.
	Diagrams string `json:"diagrams"`
}

// DefaultDocuments draws diagrams when the change alters what already exists.
func DefaultDocuments() DocumentsDef { return DocumentsDef{Diagrams: DiagramsAuto} }

// ValidDiagrams reports whether m is a diagram mode.
func ValidDiagrams(m string) bool {
	return slices.Contains([]string{DiagramsAuto, DiagramsAlways, DiagramsOff}, m)
}

// Diagram is a Mermaid block found in a document, with the heading above it.
type Diagram struct {
	Title  string `json:"title"`
	Source string `json:"source"`
}

// FindDiagrams returns the ```mermaid blocks of a Markdown text, each with
// the nearest heading above it ("Before", "After").
func FindDiagrams(md string) []Diagram {
	var out []Diagram
	var title, fence string
	var cur *strings.Builder // nil outside a Mermaid block
	for line := range strings.SplitSeq(md, "\n") {
		t := strings.TrimSpace(line)
		if fence != "" {
			if closesFence(t, fence) {
				if cur != nil {
					out = append(out, Diagram{Title: title, Source: strings.TrimRight(cur.String(), "\n")})
				}
				fence, cur = "", nil
			} else if cur != nil {
				cur.WriteString(line + "\n")
			}
			continue
		}
		if f, info := openFence(t); f != "" {
			fence = f
			if strings.EqualFold(strings.Fields(info + " x")[0], "mermaid") {
				cur = &strings.Builder{}
			}
			continue
		}
		if strings.HasPrefix(t, "#") {
			title = strings.TrimSpace(strings.TrimLeft(t, "#"))
		}
	}
	return out
}

// openFence returns the fence a line opens (``` or ~~~, three or more) and
// its info string; "" when the line opens none.
func openFence(t string) (fence, info string) {
	for _, c := range []string{"`", "~"} {
		n := len(t) - len(strings.TrimLeft(t, c))
		if n >= 3 {
			return t[:n], strings.TrimSpace(t[n:])
		}
	}
	return "", ""
}

func closesFence(t, fence string) bool {
	return strings.HasPrefix(t, fence) && strings.Trim(t, fence[:1]) == ""
}

// MarkdownSection returns the body of the first section of md whose heading
// is title (any level, case ignored), up to the next heading of the same or a
// higher level; "" when there is none.
func MarkdownSection(md, title string) string {
	lines := strings.Split(md, "\n")
	level, start := 0, -1
	inFence := false
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if f, _ := openFence(t); f != "" {
			inFence = !inFence
		}
		if inFence || !strings.HasPrefix(t, "#") {
			continue
		}
		l := len(t) - len(strings.TrimLeft(t, "#"))
		h := strings.TrimSpace(t[l:])
		if start >= 0 && l <= level {
			return strings.TrimSpace(strings.Join(lines[start:i], "\n"))
		}
		if start < 0 && strings.EqualFold(h, strings.TrimSpace(title)) {
			level, start = l, i+1
		}
	}
	if start < 0 {
		return ""
	}
	return strings.TrimSpace(strings.Join(lines[start:], "\n"))
}
