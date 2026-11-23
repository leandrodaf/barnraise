// Package itemdoc renders a Barnraise item for people reading it in an
// external tracker: title, labels and a body in the reader's language. The
// tracker adapters share it so every tracker shows the item the same way.
package itemdoc

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// Check is one line of a checklist.
type Check struct {
	Done bool
	Text string
}

// Section is one heading of the body with its content.
type Section struct {
	Heading string
	Text    string
	Bullets []string
	Checks  []Check
}

// Portuguese reports whether lang asks for Brazilian Portuguese.
func Portuguese(lang string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(lang)), "pt")
}

func pick(lang, en, pt string) string {
	if Portuguese(lang) {
		return pt
	}
	return en
}

// Title is "[I-42] Title".
func Title(it *domain.Item) string {
	return fmt.Sprintf("[%s] %s", it.ID, strings.TrimSpace(it.Title))
}

var titleRe = regexp.MustCompile(`^\s*\[([A-Za-z]-\d+)\]\s*(.*)$`)

// ParseTitle splits "[I-42] Title" into the item id and the plain title. id
// is empty when the title has no prefix.
func ParseTitle(title string) (id, rest string) {
	m := titleRe.FindStringSubmatch(title)
	if m == nil {
		return "", strings.TrimSpace(title)
	}
	return strings.ToUpper(m[1]), strings.TrimSpace(m[2])
}

// Labels are the labels Barnraise manages for an item (without status).
func Labels(it *domain.Item) []string {
	out := []string{"barnraise"}
	if a := strings.TrimSpace(it.Area); a != "" {
		out = append(out, "area:"+a)
	}
	if k := strings.TrimSpace(it.Kind); k != "" {
		out = append(out, "kind:"+k)
	}
	return append(out, it.Priority.String())
}

// StatusLabel is "status:<status>".
func StatusLabel(s domain.Status) string { return "status:" + string(s) }

var prioRe = regexp.MustCompile(`^P[0-3]$`)

// Managed reports whether a label is one Barnraise rewrites on update.
func Managed(label string) bool {
	return label == "barnraise" || strings.HasPrefix(label, "area:") || strings.HasPrefix(label, "kind:") ||
		strings.HasPrefix(label, "status:") || prioRe.MatchString(label)
}

// MergeLabels keeps the labels people added and replaces the managed ones.
func MergeLabels(current, managed []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range current {
		if Managed(l) || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	for _, l := range managed {
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

// StatusFromLabels returns the status carried by a "status:x" label.
func StatusFromLabels(labels []string) string {
	for _, l := range labels {
		if s, ok := strings.CutPrefix(l, "status:"); ok {
			return s
		}
	}
	return ""
}

// Closed reports whether the tracker should show the item as closed.
func Closed(it *domain.Item) bool { return it.Status.Terminal() }

// DefaultColumns are Barnraise's statuses, used when a tracker has no board.
func DefaultColumns() []string {
	return []string{
		string(domain.StatusDraft), string(domain.StatusReady), string(domain.StatusInProgress),
		string(domain.StatusWaitingOwner), string(domain.StatusBlocked), string(domain.StatusQueued),
		string(domain.StatusIntegrated), string(domain.StatusClosed), string(domain.StatusPaused),
	}
}

// Humanize turns "in_progress" into "In progress".
func Humanize(s string) string {
	s = strings.ReplaceAll(s, "_", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// SameName compares column names ignoring case, spaces, dashes and underscores.
func SameName(a, b string) bool { return squash(a) == squash(b) }

func squash(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '_', '-':
			return -1
		}
		return r
	}, strings.ToLower(strings.TrimSpace(s)))
}

// Sections builds the body of an item in the reader's language.
func Sections(it *domain.Item, lang string) []Section {
	var out []Section
	goal := strings.TrimSpace(it.Body)
	if goal == "" {
		goal = "—"
	}
	if it.GoalID != "" {
		goal += "\n\n" + pick(lang, "Part of ", "Parte de ") + it.GoalID + "."
	}
	out = append(out, Section{Heading: pick(lang, "Goal", "Objetivo"), Text: goal})
	if len(it.Criteria) > 0 {
		s := Section{Heading: pick(lang, "Criteria", "Critérios")}
		for _, c := range it.Criteria {
			text := c.Text
			if c.ID != "" {
				text = c.ID + ": " + text
			}
			if c.Check != "" {
				text += " (" + pick(lang, "check", "verificação") + ": " + c.Check + ")"
			}
			s.Checks = append(s.Checks, Check{Done: c.Met, Text: text})
		}
		out = append(out, s)
	}
	if it.Kind == "bug" || len(it.Preserve) > 0 {
		s := Section{Heading: pick(lang, "What must not change", "O que não pode mudar"), Bullets: it.Preserve}
		if len(it.Preserve) == 0 {
			s.Text = "—"
		}
		out = append(out, s)
	}
	if len(it.Clarifications) > 0 {
		out = append(out, Section{Heading: pick(lang, "Open questions", "Perguntas em aberto"), Bullets: it.Clarifications})
	}
	if it.Area != "" {
		out = append(out, Section{Heading: pick(lang, "Area", "Área"), Text: it.Area})
	}
	if it.Size != "" {
		out = append(out, Section{Heading: pick(lang, "Size", "Tamanho"), Text: it.Size})
	}
	if len(it.Deps) > 0 {
		out = append(out, Section{Heading: pick(lang, "Depends on", "Depende de"), Bullets: it.Deps})
	}
	return out
}

// Footer is the closing line telling readers where the item lives.
func Footer(it *domain.Item, lang string) string {
	return pick(lang,
		"Synced by Barnraise from "+it.ID+". Edits to this text may be overwritten; comment instead.",
		"Sincronizado pelo Barnraise a partir de "+it.ID+". Edições neste texto podem ser sobrescritas; comente em vez disso.")
}

// Markdown renders the body as Markdown (GitHub, GitLab, Linear).
func Markdown(it *domain.Item, lang string) string {
	var b strings.Builder
	for _, s := range Sections(it, lang) {
		fmt.Fprintf(&b, "## %s\n\n", s.Heading)
		if s.Text != "" {
			b.WriteString(s.Text)
			b.WriteString("\n\n")
		}
		for _, l := range s.Bullets {
			fmt.Fprintf(&b, "- %s\n", l)
		}
		for _, c := range s.Checks {
			mark := " "
			if c.Done {
				mark = "x"
			}
			fmt.Fprintf(&b, "- [%s] %s\n", mark, c.Text)
		}
		if len(s.Bullets)+len(s.Checks) > 0 {
			b.WriteString("\n")
		}
	}
	b.WriteString("---\n_")
	b.WriteString(Footer(it, lang))
	b.WriteString("_\n")
	return b.String()
}
