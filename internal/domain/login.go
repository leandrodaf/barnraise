package domain

import (
	"regexp"
	"strings"
	"time"
)

// States of a login session.
const (
	LoginRunning   = "running"
	LoginDone      = "done"
	LoginFailed    = "failed"
	LoginCancelled = "cancelled"
)

// LoginSession is the login of one account on one machine, as the owner
// follows it from the panel: the provider's own login runs on the machine,
// prints where to sign in, and may wait for a code. It lives in the memory
// of that machine's daemon and is never kept in the log.
type LoginSession struct {
	Machine  string `json:"machine"`
	Account  string `json:"account"`
	Provider string `json:"provider"`
	State    string `json:"state"`
	// URL is where the owner signs in; Code the one-time code to type there
	// when the provider gives one.
	URL  string `json:"url,omitempty"`
	Code string `json:"code,omitempty"`
	// WantsCode: the login waits for the code the page shows after signing in.
	WantsCode bool      `json:"wants_code,omitempty"`
	Lines     []string  `json:"lines,omitempty"`
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at,omitzero"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
}

// LoginProviders are the providers whose login can be followed from another
// machine: their CLI prints a link and needs no screen of its own.
var LoginProviders = []string{"claude", "codex"}

// loginLines is how many of the last printed lines a session keeps.
const loginLines = 12

var (
	ansiRe      = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)
	loginURLRe  = regexp.MustCompile(`https://[^\s"'<>]+`)
	loginCodeRe = regexp.MustCompile(`\b[A-Z0-9]{4}-[A-Z0-9]{4,6}\b`)
)

// Read takes in what the provider's login printed: the link, the one-time
// code, whether it waits for a code, and the last lines for the owner.
func (l *LoginSession) Read(out string) {
	out = strings.ReplaceAll(ansiRe.ReplaceAllString(out, ""), "\r", "\n")
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if u := loginURLRe.FindString(line); u != "" && l.URL == "" {
			l.URL = strings.TrimRight(u, ".,)")
		}
		low := strings.ToLower(line)
		if l.URL != "" && l.Code == "" && !strings.Contains(line, "://") {
			if c := loginCodeRe.FindString(line); c != "" {
				l.Code = c
			}
		}
		if strings.Contains(low, "paste code") {
			l.WantsCode = true
		}
		if len([]rune(line)) > 240 {
			line = string([]rune(line)[:240]) + "…"
		}
		l.Lines = append(l.Lines, line)
	}
	if len(l.Lines) > loginLines {
		l.Lines = l.Lines[len(l.Lines)-loginLines:]
	}
}

// Running reports whether the login still goes on.
func (l *LoginSession) Running() bool { return l.State == LoginRunning }
