package jira

import (
	"strings"

	"github.com/leandrodaf/barnraise/internal/adapters/out/httpx/itemdoc"
)

// Block is an Atlassian Document Format element.
type Block struct {
	Type    string         `json:"type"`
	Version int            `json:"version,omitempty"`
	Text    string         `json:"text,omitempty"`
	Attrs   map[string]any `json:"attrs,omitempty"`
	Marks   []Mark         `json:"marks,omitempty"`
	Content []Block        `json:"content,omitempty"`
}

// Mark is an ADF text mark.
type Mark struct {
	Type string `json:"type"`
}

func text(s string) Block { return Block{Type: "text", Text: s} }

// Paragraphs turns plain text into an ADF document, one paragraph per
// blank-line separated block (single newlines become hard breaks).
func Paragraphs(s string) Block {
	doc := Block{Type: "doc", Version: 1}
	doc.Content = paragraphs(s)
	if len(doc.Content) == 0 {
		doc.Content = []Block{{Type: "paragraph"}}
	}
	return doc
}

func paragraphs(s string) []Block {
	var out []Block
	for block := range strings.SplitSeq(strings.ReplaceAll(s, "\r\n", "\n"), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		p := Block{Type: "paragraph"}
		for i, line := range strings.Split(block, "\n") {
			if i > 0 {
				p.Content = append(p.Content, Block{Type: "hardBreak"})
			}
			if line != "" {
				p.Content = append(p.Content, text(line))
			}
		}
		out = append(out, p)
	}
	return out
}

func bulletList(items []string) Block {
	l := Block{Type: "bulletList"}
	for _, it := range items {
		l.Content = append(l.Content, Block{Type: "listItem", Content: []Block{{Type: "paragraph", Content: []Block{text(it)}}}})
	}
	return l
}

// Document renders item sections as ADF: headings, paragraphs and bullet
// lists. Checklist lines become bullets starting with ☐ or ☑.
func Document(sections []itemdoc.Section, footer string) Block {
	doc := Block{Type: "doc", Version: 1}
	for _, s := range sections {
		doc.Content = append(doc.Content, Block{Type: "heading", Attrs: map[string]any{"level": 2}, Content: []Block{text(s.Heading)}})
		doc.Content = append(doc.Content, paragraphs(s.Text)...)
		if len(s.Bullets) > 0 {
			doc.Content = append(doc.Content, bulletList(s.Bullets))
		}
		if len(s.Checks) > 0 {
			lines := make([]string, 0, len(s.Checks))
			for _, c := range s.Checks {
				mark := "☐ "
				if c.Done {
					mark = "☑ "
				}
				lines = append(lines, mark+c.Text)
			}
			doc.Content = append(doc.Content, bulletList(lines))
		}
	}
	if footer != "" {
		doc.Content = append(doc.Content, Block{Type: "rule"},
			Block{Type: "paragraph", Content: []Block{{Type: "text", Text: footer, Marks: []Mark{{Type: "em"}}}}})
	}
	return doc
}

// PlainText flattens an ADF document into text: blocks end with a newline,
// list items start with "- ".
func PlainText(n *Block) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	walk(&b, *n, false)
	return strings.TrimSpace(b.String())
}

func walk(b *strings.Builder, n Block, inList bool) {
	switch n.Type {
	case "text":
		b.WriteString(n.Text)
		return
	case "hardBreak":
		b.WriteString("\n")
		return
	case "mention", "emoji":
		if t, ok := n.Attrs["text"].(string); ok {
			b.WriteString(t)
		}
		return
	case "listItem":
		b.WriteString("- ")
		inList = true
	case "heading":
		if lvl, ok := n.Attrs["level"].(float64); ok && lvl > 0 {
			b.WriteString(strings.Repeat("#", int(lvl)) + " ")
		}
	}
	for _, c := range n.Content {
		walk(b, c, inList)
	}
	switch n.Type {
	case "paragraph", "heading", "codeBlock", "blockquote", "rule":
		if inList {
			b.WriteString("\n")
		} else {
			b.WriteString("\n\n")
		}
	case "bulletList", "orderedList":
		if !inList {
			b.WriteString("\n")
		}
	}
}
