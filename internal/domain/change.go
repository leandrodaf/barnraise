package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Every change that lands on main is described the same way, in English:
// a Conventional Commits header on the commit and, when people outside the
// code care about it, a line in a Keep a Changelog file. The agent that did
// the work writes the description; Barnraise checks its shape and writes
// both places, so history and changelog never disagree.

// ChangeNote describes what an item does to the product, for the commit that
// lands and for the changelog.
type ChangeNote struct {
	Type     string `json:"type"`               // feat, fix, perf, refactor, docs, test, build, ci, chore, style, revert
	Scope    string `json:"scope,omitempty"`    // the part of the product: api, web, mobile, auth…
	Subject  string `json:"subject"`            // imperative, lower case, no final period: "add CSV export of the statement"
	Body     string `json:"body,omitempty"`     // why, and what changes for people; optional
	Breaking string `json:"breaking,omitempty"` // what breaks and how to migrate; empty when nothing does
}

// ChangeTypes are the Conventional Commits types Barnraise accepts, with
// what each one means.
var ChangeTypes = map[string]string{
	"feat":     "a new capability for the people who use the product",
	"fix":      "a bug fix",
	"perf":     "the same behavior, faster or lighter",
	"refactor": "the same behavior, restructured code",
	"docs":     "documentation only",
	"test":     "tests only",
	"build":    "the build system or dependencies",
	"ci":       "continuous integration",
	"chore":    "upkeep that changes neither the product nor its tests",
	"style":    "formatting only",
	"revert":   "undoes an earlier change",
}

// HeaderMax is the longest a commit header may be.
const HeaderMax = 72

var (
	scopeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*$`)
	// Words that give away a subject written in Portuguese. English text does
	// not use them as whole words. It is a guard, not a language detector: the
	// accent check below catches most of the rest.
	notEnglish = regexp.MustCompile(`(?i)(^|[^a-z])(para|uma|que|dos|das|pelo|pela|quando|nao|esta|sao|corrige|corrigir|adiciona|adicionar|ajusta|ajustar|altera|alterar|tela|botao|usuario|erro ao|ao abrir|e o|e a|da|na|um)([^a-z]|$)`)
)

// changeTypeAliases are the words agents write for a type, mapped to it.
var changeTypeAliases = map[string]string{
	"feature": "feat", "features": "feat", "add": "feat", "new": "feat",
	"bugfix": "fix", "bug": "fix", "hotfix": "fix", "fixes": "fix", "bug-fix": "fix",
	"doc": "docs", "documentation": "docs",
	"tests": "test", "testing": "test",
	"refactoring": "refactor", "performance": "perf",
	"dependencies": "build", "deps": "build",
	"chores": "chore", "maintenance": "chore",
	"formatting": "style", "format": "style",
}

var (
	spacesRe     = regexp.MustCompile(`\s+`)
	notScopeRe   = regexp.MustCompile(`[^a-z0-9._/-]+`)
	typePrefixRe = regexp.MustCompile(`^(?i)(feat|fix|perf|refactor|docs|test|build|ci|chore|style|revert)(\([^)]*\))?!?:\s*`)
)

// Normalize fixes what can be fixed in a change without guessing what it
// says: the type's usual synonyms and case, a scope with capitals or
// spaces, the subject's spaces, line breaks, final period, first capital
// and a repeated "type:" prefix. A header over HeaderMax is cut at a word
// and the whole subject opens the body. What needs judgment (a text not in
// English, an empty subject, an unknown type) is left to Validate.
func (c ChangeNote) Normalize() ChangeNote {
	c.Type = strings.ToLower(strings.TrimSpace(c.Type))
	if t, ok := changeTypeAliases[c.Type]; ok {
		c.Type = t
	}
	c.Scope = strings.Trim(notScopeRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(c.Scope)), "-"), "-._/")
	if c.Scope != "" && !scopeRe.MatchString(c.Scope) {
		c.Scope = ""
	}
	s := strings.TrimSpace(spacesRe.ReplaceAllString(c.Subject, " "))
	for {
		t := typePrefixRe.ReplaceAllString(s, "")
		if t == s {
			break
		}
		s = strings.TrimSpace(t)
	}
	s = strings.TrimRight(s, ". ")
	if len(s) > 1 && s[0] >= 'A' && s[0] <= 'Z' && !(s[1] >= 'A' && s[1] <= 'Z') {
		s = string(s[0]+'a'-'A') + s[1:]
	}
	c.Subject = s
	c.Body, c.Breaking = strings.TrimSpace(c.Body), strings.TrimSpace(c.Breaking)
	if over := len(c.Header()) - HeaderMax; over > 0 && s != "" {
		full := s
		room := len(s) - over
		cut := strings.LastIndex(s[:max(room, 0)+1], " ")
		if cut <= 0 {
			cut = max(room, 1)
		}
		c.Subject = strings.TrimRight(s[:cut], " ,;:-")
		if c.Body == "" {
			c.Body = upperFirst(full) + "."
		} else if !strings.Contains(c.Body, full) {
			c.Body = upperFirst(full) + ".\n\n" + c.Body
		}
	}
	return c
}

func upperFirst(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}

// Validate checks the shape of a change: a known type, a simple scope, and
// a subject in the form every tool and reader expects.
func (c ChangeNote) Validate() error {
	if _, ok := ChangeTypes[c.Type]; !ok {
		return fmt.Errorf("change type %q is not one of feat, fix, perf, refactor, docs, test, build, ci, chore, style, revert", c.Type)
	}
	if c.Scope != "" && !scopeRe.MatchString(c.Scope) {
		return fmt.Errorf("change scope %q: use one short lower-case word for the part of the product (api, web, mobile)", c.Scope)
	}
	s := c.Subject
	switch {
	case strings.TrimSpace(s) == "":
		return errors.New("change subject is empty: say what the change does, in the imperative (\"add CSV export of the statement\")")
	case s != strings.TrimSpace(s) || strings.ContainsAny(s, "\n\r"):
		return errors.New("change subject must be a single line without leading or trailing spaces")
	case strings.HasSuffix(s, "."):
		return errors.New("change subject must not end with a period")
	case s[0] >= 'A' && s[0] <= 'Z' && !(len(s) > 1 && s[1] >= 'A' && s[1] <= 'Z'):
		return errors.New("change subject starts in lower case (\"add …\", \"fix …\")")
	}
	if err := englishOnly("change subject", s); err != nil {
		return err
	}
	if n := len(c.Header()); n > HeaderMax {
		return fmt.Errorf("commit header is %d characters, over the limit of %d: shorten the subject and put the detail in the body", n, HeaderMax)
	}
	if err := englishOnly("change body", c.Body); err != nil {
		return err
	}
	return englishOnly("breaking change", c.Breaking)
}

// englishOnly refuses text that is visibly not English. Commits and the
// changelog are always in English, whatever language the project's people
// read: tools, contributors and future readers depend on it.
func englishOnly(what, s string) error {
	for _, r := range s {
		if r > 127 && !strings.ContainsRune("—–‘’“”…→≤≥×°", r) {
			return fmt.Errorf("%s must be written in English (found %q): commits and the changelog are always in English", what, string(r))
		}
	}
	if m := notEnglish.FindStringSubmatch(s); m != nil {
		return fmt.Errorf("%s must be written in English (found %q): commits and the changelog are always in English", what, m[2])
	}
	return nil
}

// Header is the first line of the commit: type(scope)!: subject.
func (c ChangeNote) Header() string {
	h := c.Type
	if c.Scope != "" {
		h += "(" + c.Scope + ")"
	}
	if c.Breaking != "" {
		h += "!"
	}
	return h + ": " + c.Subject
}

// CommitMessage is the full message of the commit that lands on main:
// header, body, and trailers (the breaking change and the item).
func (c ChangeNote) CommitMessage(itemID string) string {
	var b strings.Builder
	b.WriteString(c.Header())
	if body := strings.TrimSpace(c.Body); body != "" {
		b.WriteString("\n\n" + body)
	}
	b.WriteString("\n\n")
	if c.Breaking != "" {
		b.WriteString("BREAKING CHANGE: " + strings.TrimSpace(c.Breaking) + "\n")
	}
	b.WriteString(CommitTrailer(itemID))
	return b.String()
}

// ChangelogSection is the Keep a Changelog section a change belongs to, or
// "" when it is not something the people who use the product would read
// about (tests, build, upkeep).
func (c ChangeNote) ChangelogSection() string {
	switch c.Type {
	case "feat":
		return "Added"
	case "fix":
		return "Fixed"
	case "perf", "refactor":
		if c.Type == "refactor" && c.Breaking == "" {
			return ""
		}
		return "Changed"
	case "revert":
		return "Removed"
	}
	if c.Breaking != "" {
		return "Changed"
	}
	return ""
}

// ChangelogEntry is the line of the change in the changelog.
func (c ChangeNote) ChangelogEntry(itemID string) string {
	s := c.Subject
	if s != "" && s[0] >= 'a' && s[0] <= 'z' {
		s = strings.ToUpper(s[:1]) + s[1:]
	}
	if c.Scope != "" {
		s = "**" + c.Scope + ":** " + s
	}
	if c.Breaking != "" {
		s = "**Breaking:** " + s + ". " + strings.TrimSuffix(strings.TrimSpace(c.Breaking), ".")
	}
	return s + " (" + itemID + ")"
}

// DefaultChangeNote describes an item whose agent gave no description: the type
// from the item's kind, the scope from its area, the subject from its
// title. It still has to pass Validate, so a title that is not English
// makes the agent write the description.
func DefaultChangeNote(it *Item) ChangeNote {
	c := ChangeNote{Type: "chore", Scope: Slug(it.Area, 20)}
	switch it.Kind {
	case "bug":
		c.Type = "fix"
	case "feature", "":
		c.Type = "feat"
	case "improvement":
		c.Type = "refactor"
	}
	if len(it.Kind) > 0 {
		if _, ok := ChangeTypes[it.Kind]; ok {
			c.Type = it.Kind
		}
	}
	s := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(it.Title), "."))
	if s != "" && s[0] >= 'A' && s[0] <= 'Z' && !(len(s) > 1 && s[1] >= 'A' && s[1] <= 'Z') {
		s = strings.ToLower(s[:1]) + s[1:]
	}
	c.Subject = s
	if over := len(c.Header()) - HeaderMax; over > 0 && len(s) > over+1 {
		c.Subject = strings.TrimSpace(s[:len(s)-over])
	}
	return c
}

// changelogSections is the order Keep a Changelog gives its sections.
var changelogSections = []string{"Added", "Changed", "Deprecated", "Removed", "Fixed", "Security"}

const changelogPreface = `# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
`

// InsertChangelog adds an entry to the Unreleased part of a Keep a Changelog
// document, under the given section, creating the document, the Unreleased
// heading and the section as needed. Everything else is left as it is. The
// same entry is never added twice.
func InsertChangelog(content, section, entry string) string {
	line := "- " + entry
	if strings.TrimSpace(content) == "" {
		content = changelogPreface
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	isH2 := func(s string) bool { return strings.HasPrefix(s, "## ") }
	isH3 := func(s string) bool { return strings.HasPrefix(s, "### ") }
	unreleased := func(s string) bool {
		t := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(s, "## ")))
		return isH2(s) && (t == "[unreleased]" || t == "unreleased" || strings.HasPrefix(t, "[unreleased]"))
	}

	// Where Unreleased starts and ends.
	start := -1
	for i, l := range lines {
		if unreleased(l) {
			start = i
			break
		}
	}
	if start < 0 {
		// Before the first release heading, or at the end of the preface.
		at := len(lines)
		for i, l := range lines {
			if isH2(l) {
				at = i
				break
			}
		}
		block := []string{"## [Unreleased]", ""}
		if at > 0 && strings.TrimSpace(lines[at-1]) != "" {
			block = append([]string{""}, block...)
		}
		lines = append(lines[:at:at], append(block, lines[at:]...)...)
		for i, l := range lines {
			if unreleased(l) {
				start = i
				break
			}
		}
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if isH2(lines[i]) {
			end = i
			break
		}
	}
	for _, l := range lines[start:end] {
		if strings.TrimSpace(l) == line {
			return strings.Join(lines, "\n") + "\n"
		}
	}

	// The section inside Unreleased.
	secStart := -1
	for i := start + 1; i < end; i++ {
		if isH3(lines[i]) && strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(lines[i], "### ")), section) {
			secStart = i
			break
		}
	}
	if secStart < 0 {
		// In the canonical order: before the first later section, else at the end.
		rank := func(name string) int {
			for i, s := range changelogSections {
				if strings.EqualFold(s, name) {
					return i
				}
			}
			return len(changelogSections)
		}
		at := end
		for i := start + 1; i < end; i++ {
			if isH3(lines[i]) && rank(strings.TrimSpace(strings.TrimPrefix(lines[i], "### "))) > rank(section) {
				at = i
				break
			}
		}
		for at > start+1 && strings.TrimSpace(lines[at-1]) == "" {
			at--
		}
		block := []string{"", "### " + section, "", line}
		// What follows keeps one blank line before it; trailing blanks go.
		rest := at
		for rest < len(lines) && strings.TrimSpace(lines[rest]) == "" {
			rest++
		}
		if rest < len(lines) {
			block = append(block, "")
		}
		lines = append(lines[:at:at], append(block, lines[rest:]...)...)
		return strings.Join(lines, "\n") + "\n"
	}
	// After the last entry of the section.
	secEnd := end
	for i := secStart + 1; i < end; i++ {
		if isH3(lines[i]) {
			secEnd = i
			break
		}
	}
	at := secEnd
	for at > secStart+1 && strings.TrimSpace(lines[at-1]) == "" {
		at--
	}
	if at == secStart+1 {
		lines = append(lines[:at:at], append([]string{"", line}, lines[at:]...)...)
	} else {
		lines = append(lines[:at:at], append([]string{line}, lines[at:]...)...)
	}
	return strings.Join(lines, "\n") + "\n"
}
