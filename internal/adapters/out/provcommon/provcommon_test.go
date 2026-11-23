package provcommon

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseResetsAt(t *testing.T) {
	sp, _ := time.LoadLocation("America/Sao_Paulo")
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		text string
		want time.Time
	}{
		{"Claude AI usage limit reached|1795447800", time.Unix(1795447800, 0)},
		{"You've hit your limit · resets 3pm (America/Sao_Paulo)", time.Date(2026, 11, 23, 15, 0, 0, 0, sp)},
		{"5-hour limit reached ∙ resets 8am (America/Sao_Paulo)", time.Date(2026, 11, 24, 8, 0, 0, 0, sp)},
		{"Weekly limit reached · resets Nov 27, 4:30pm (UTC)", time.Date(2026, 11, 27, 16, 30, 0, 0, time.UTC)},
		{"try again at Nov 24th, 2026 3:05 PM.", time.Date(2026, 11, 24, 15, 5, 0, 0, time.UTC)},
		{"You've hit your usage limit. Try again in 2 hours 5 minutes.", now.Add(2*time.Hour + 5*time.Minute)},
		{"try again in 3 days", now.Add(72 * time.Hour)},
		{"rate limit; resets at 2026-11-23T18:00:00Z", time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC)},
		{"usage limit reached", time.Time{}},
	}
	for _, c := range cases {
		got := ParseResetsAt(c.text, now)
		if !got.Equal(c.want) {
			t.Errorf("%q: got %v want %v", c.text, got, c.want)
		}
	}
}

func TestIsLimitText(t *testing.T) {
	for _, s := range []string{"Claude AI usage limit reached|1", "You've hit your limit", "429 Too Many Requests", "insufficient_quota",
		"unexpected status 402 Payment Required: {\"error\":{\"message\":\"Insufficient credits\"}}"} {
		if !IsLimitText(s) {
			t.Errorf("%q not a limit", s)
		}
	}
	if IsLimitText("authentication failed") {
		t.Error("false positive")
	}
	if l, ok := LimitLine("noise\nYou've hit your usage limit.\n\n"); !ok || l != "You've hit your usage limit." {
		t.Errorf("LimitLine = %q %v", l, ok)
	}
}

func TestEnvAndSudo(t *testing.T) {
	t.Setenv("HOME", "/home/x")
	t.Setenv("SOME_SECRET", "s")
	e := MinimalEnv()
	if _, ok := e.Get("SOME_SECRET"); ok {
		t.Fatal("secret inherited")
	}
	e.Merge(map[string]string{"B": "2", "A": "1"})
	e.Set("A", "3")
	l := e.List()
	if !slices.Contains(l, "HOME=/home/x") || !slices.Contains(l, "A=3") || slices.Contains(l, "A=1") {
		t.Fatalf("env = %v", l)
	}
	name, args := SudoArgs("agent1", e, "claude", []string{"-p"})
	if name != "sudo" || args[0] != "-n" || args[2] != "agent1" || !strings.HasPrefix(args[3], "--preserve-env=") || !strings.HasSuffix(args[3], "A,B") || strings.Contains(args[3], "HOME") ||
		!slices.Equal(args[4:], []string{"--", "claude", "-p"}) {
		t.Fatalf("sudo = %s %q", name, args)
	}
}

func TestVersion(t *testing.T) {
	v, s, ok := ParseVersion("2.1.330 (Claude Code)")
	if !ok || s != "2.1.330" || !AtLeast(v, 2, 1, 80) || AtLeast(v, 2, 2, 0) || !AtLeast(v, 2, 1, 330) {
		t.Fatalf("v = %v %q", v, s)
	}
	if _, _, ok := ParseVersion("nope"); ok {
		t.Fatal("want no version")
	}
}
