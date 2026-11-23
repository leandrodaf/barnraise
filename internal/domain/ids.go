// Package domain holds Barnraise's pure rules: items, goals, focus, flows,
// rework, capacity, proposals, tweaks and policy. It depends only on the
// standard library; time and randomness enter through arguments.
package domain

import (
	"fmt"
	"strconv"
	"strings"
)

// Kind of a short id. The same id appears in the tracker, the PR title, the
// branch name and the commit trailer.
type IDKind string

const (
	KindGoal     IDKind = "G"
	KindItem     IDKind = "I"
	KindProposal IDKind = "P"
	KindDecision IDKind = "D"
	KindMemory   IDKind = "M"
	KindQuestion IDKind = "Q"
)

// FormatID renders a short id like "I-142".
func FormatID(k IDKind, n int) string { return fmt.Sprintf("%s-%d", k, n) }

// ParseID splits "I-142" into its kind and number.
func ParseID(s string) (IDKind, int, error) {
	s = strings.TrimSpace(s)
	i := strings.IndexByte(s, '-')
	if i <= 0 {
		return "", 0, fmt.Errorf("invalid id %q", s)
	}
	k := IDKind(strings.ToUpper(s[:i]))
	switch k {
	case KindGoal, KindItem, KindProposal, KindDecision, KindMemory, KindQuestion:
	default:
		return "", 0, fmt.Errorf("invalid id kind in %q", s)
	}
	n, err := strconv.Atoi(s[i+1:])
	if err != nil || n <= 0 {
		return "", 0, fmt.Errorf("invalid id number in %q", s)
	}
	return k, n, nil
}

// NormalizeItemID accepts "42", "#42", "i-42" or "I-42" and returns "I-42".
func NormalizeItemID(s string) (string, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return FormatID(KindItem, n), nil
	}
	k, n, err := ParseID(s)
	if err != nil {
		return "", err
	}
	if k != KindItem {
		return "", fmt.Errorf("%q is not an item id", s)
	}
	return FormatID(k, n), nil
}

// AreaID renders an area name as "A-cards".
func AreaID(name string) string {
	if strings.HasPrefix(name, "A-") {
		return name
	}
	return "A-" + name
}

// AreaName strips the "A-" prefix.
func AreaName(id string) string { return strings.TrimPrefix(id, "A-") }

// Slug turns a title into a short branch-safe slug.
func Slug(title string, max int) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		r = foldAccent(r)
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
		if b.Len() >= max {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

func foldAccent(r rune) rune {
	switch r {
	case 'á', 'à', 'â', 'ã', 'ä':
		return 'a'
	case 'é', 'è', 'ê', 'ë':
		return 'e'
	case 'í', 'ì', 'î', 'ï':
		return 'i'
	case 'ó', 'ò', 'ô', 'õ', 'ö':
		return 'o'
	case 'ú', 'ù', 'û', 'ü':
		return 'u'
	case 'ç':
		return 'c'
	case 'ñ':
		return 'n'
	}
	return r
}

// BranchName is "br/<item>-<slug>".
func BranchName(itemID, title string) string {
	s := Slug(title, 40)
	if s == "" {
		return "br/" + itemID
	}
	return "br/" + itemID + "-" + s
}

// CommitTrailer is the trailer every commit of an item carries.
func CommitTrailer(itemID string) string { return "Barnraise-Item: " + itemID }
