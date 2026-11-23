package provcommon

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// limitMarkers are phrases the CLIs use when a subscription or rate limit stops work.
// They are only checked on error paths (error results, error events, stderr of a
// failed run), never on ordinary assistant text.
var limitMarkers = []string{
	"usage limit",
	"hit your limit",
	"limit reached",
	"rate limit",
	"rate_limit",
	"usage_limit",
	"quota exceeded",
	"insufficient_quota",
	"too many requests",
	"out of extra usage",
	"resets at",
	"try again at",
	"try again in",
}

// IsLimitText reports whether an error text says a usage or rate limit was hit.
func IsLimitText(text string) bool {
	t := strings.ToLower(text)
	for _, m := range limitMarkers {
		if strings.Contains(t, m) {
			return true
		}
	}
	return false
}

var (
	reEpoch    = regexp.MustCompile(`\|(\d{10})\b`)
	reAnchor   = regexp.MustCompile(`(?i)\b(?:resets?|try again|available again|retry)\b(?:\s+(?:at|on))?`)
	reIn       = regexp.MustCompile(`(?i)^\s*in\s+((?:\d+\s*(?:days?|d|hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s)\b[\s,]*(?:and\s+)?)+)`)
	reDurPart  = regexp.MustCompile(`(?i)(\d+)\s*(days?|d|hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s)\b`)
	reDate     = regexp.MustCompile(`(?i)\b(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s+(\d{1,2})(?:st|nd|rd|th)?,?(?:\s+(\d{4}))?`)
	reClock    = regexp.MustCompile(`(?i)\b(\d{1,2})(?::(\d{2}))?\s*(am|pm)\b|\b(\d{1,2}):(\d{2})\b`)
	reZone     = regexp.MustCompile(`\(([A-Za-z]+(?:/[A-Za-z0-9_+\-]+)+|UTC|GMT)\)`)
	reISO      = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:\d{2})`)
	monthIndex = map[string]time.Month{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
)

// ParseResetsAt finds when a limit resets in a CLI message, relative to now.
// It understands "…|1795447800" (epoch), "resets 3pm (America/Sao_Paulo)",
// "resets Nov 24, 3:30pm", "try again at Nov 24th, 2026 3:05 PM",
// "try again in 2 hours 5 minutes" and ISO timestamps. Zero when not found.
func ParseResetsAt(text string, now time.Time) time.Time {
	if m := reEpoch.FindStringSubmatch(text); m != nil {
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			return time.Unix(n, 0)
		}
	}
	loc := now.Location()
	if m := reZone.FindStringSubmatch(text); m != nil {
		if l, err := time.LoadLocation(m[1]); err == nil {
			loc = l
		}
	}
	idx := reAnchor.FindStringIndex(text)
	if idx == nil {
		if m := reISO.FindString(text); m != "" {
			if t, err := time.Parse(time.RFC3339, m); err == nil {
				return t
			}
		}
		return time.Time{}
	}
	rest := text[idx[1]:]
	if m := reIn.FindStringSubmatch(rest); m != nil {
		var d time.Duration
		for _, p := range reDurPart.FindAllStringSubmatch(m[1], -1) {
			n, _ := strconv.Atoi(p[1])
			switch u := strings.ToLower(p[2]); {
			case strings.HasPrefix(u, "d"):
				d += time.Duration(n) * 24 * time.Hour
			case strings.HasPrefix(u, "h"):
				d += time.Duration(n) * time.Hour
			case strings.HasPrefix(u, "m"):
				d += time.Duration(n) * time.Minute
			default:
				d += time.Duration(n) * time.Second
			}
		}
		if d > 0 {
			return now.Add(d)
		}
	}
	if m := reISO.FindString(rest); m != "" {
		if t, err := time.Parse(time.RFC3339, m); err == nil {
			return t
		}
	}
	// Only look a short way past the anchor so unrelated numbers are ignored.
	if len(rest) > 80 {
		rest = rest[:80]
	}
	cm := reClock.FindStringSubmatch(rest)
	if cm == nil {
		return time.Time{}
	}
	var hour, minute int
	if cm[1] != "" {
		hour, _ = strconv.Atoi(cm[1])
		minute, _ = strconv.Atoi(cm[2])
		pm := strings.EqualFold(cm[3], "pm")
		if hour == 12 {
			hour = 0
		}
		if pm {
			hour += 12
		}
	} else {
		hour, _ = strconv.Atoi(cm[4])
		minute, _ = strconv.Atoi(cm[5])
	}
	if hour > 23 || minute > 59 {
		return time.Time{}
	}
	local := now.In(loc)
	if dm := reDate.FindStringSubmatch(rest); dm != nil {
		month := monthIndex[strings.ToLower(dm[1])]
		day, _ := strconv.Atoi(dm[2])
		year := local.Year()
		explicitYear := dm[3] != ""
		if explicitYear {
			year, _ = strconv.Atoi(dm[3])
		}
		t := time.Date(year, month, day, hour, minute, 0, 0, loc)
		if !explicitYear && t.Before(now.Add(-24*time.Hour)) {
			t = t.AddDate(1, 0, 0)
		}
		return t
	}
	t := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

// LimitLine returns the last line of text that reads as a limit message.
func LimitLine(text string) (string, bool) {
	lines := strings.Split(text, "\n")
	for _, line := range slices.Backward(lines) {
		if l := strings.TrimSpace(line); l != "" && IsLimitText(l) {
			return l, true
		}
	}
	return "", false
}
