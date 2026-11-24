package domain

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoginSessionReadsWhatTheLoginPrints(t *testing.T) {
	t.Run("claude: a link, then it waits for the code", func(t *testing.T) {
		var l LoginSession
		l.Read("Opening browser to sign in…\r\nIf the browser didn't open, visit:\n")
		l.Read("\x1b[1mhttps://claude.ai/oauth/authorize?code=true&client_id=abc\x1b[0m\nPaste code here if prompted > ")
		if l.URL != "https://claude.ai/oauth/authorize?code=true&client_id=abc" {
			t.Errorf("url: %q", l.URL)
		}
		if !l.WantsCode || l.Code != "" {
			t.Errorf("wants %v code %q", l.WantsCode, l.Code)
		}
		if got := l.Lines[len(l.Lines)-1]; got != "Paste code here if prompted >" {
			t.Errorf("last line: %q", got)
		}
	})
	t.Run("codex: a link and a one-time code", func(t *testing.T) {
		var l LoginSession
		l.Read("Follow these steps:\n1. Open this link in your browser\n   https://auth.openai.com/codex/device\n2. Enter this one-time code (expires in 15 minutes)\n   0MWR-Y787B\n")
		if l.URL != "https://auth.openai.com/codex/device" || l.Code != "0MWR-Y787B" || l.WantsCode {
			t.Errorf("got %+v", l)
		}
	})
	t.Run("keeps the last lines only, each of a bounded length", func(t *testing.T) {
		var l LoginSession
		l.Read(strings.Repeat("line\n", 40) + strings.Repeat("x", 500))
		if len(l.Lines) != loginLines {
			t.Fatalf("%d lines", len(l.Lines))
		}
		if n := len([]rune(l.Lines[loginLines-1])); n != 241 {
			t.Errorf("long line kept %d runes", n)
		}
	})
	t.Run("a code before any link is not a one-time code", func(t *testing.T) {
		var l LoginSession
		l.Read("build ABCD-12345\n")
		if l.Code != "" {
			t.Errorf("code %q", l.Code)
		}
	})
	if (&LoginSession{State: LoginRunning}).Running() == false || (&LoginSession{State: LoginDone}).Running() {
		t.Error("Running follows the state")
	}
}

func TestAccountOnEachMachine(t *testing.T) {
	data := filepath.Join("/var", "barnraise")
	own := filepath.Join(data, AccountsDir, "claude-1")
	cases := []struct {
		name    string
		acc     Account
		machine string
		usable  bool
		dir     string
	}{
		{"no machine named: any machine, the folder as written", Account{ID: "claude-1", ConfigDir: "/home/a/.claude"}, "m2", true, "/home/a/.claude"},
		{"no machine named, the CLI's own login", Account{ID: "claude-1"}, "m2", true, ""},
		{"pinned: its machine keeps the folder as written", Account{ID: "claude-1", Machine: "m1", ConfigDir: "/home/a/.claude"}, "m1", true, "/home/a/.claude"},
		{"pinned: another machine is out", Account{ID: "claude-1", Machine: "m1", ConfigDir: "/home/a/.claude"}, "m2", false, own},
		{"pinned and logged in elsewhere: the other machine has its own folder", Account{ID: "claude-1", Machine: "m1", ConfigDir: "/home/a/.claude", Machines: []string{"m2"}}, "m2", true, own},
		{"pinned and logged in elsewhere: the pinned machine is unchanged", Account{ID: "claude-1", Machine: "m1", Machines: []string{"m2"}}, "m1", true, ""},
		{"logged in machine by machine: a folder under each data dir", Account{ID: "claude-1", ConfigDir: "accounts/claude-1", Machines: []string{"m1", "m2"}}, "m2", true, own},
		{"logged in machine by machine: a machine without it is out", Account{ID: "claude-1", ConfigDir: "accounts/claude-1", Machines: []string{"m1"}}, "m3", false, own},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.acc.UsableOn(c.machine); got != c.usable {
				t.Errorf("usable on %s: %v", c.machine, got)
			}
			if got := c.acc.DirOn(c.machine, data); got != c.dir {
				t.Errorf("dir on %s: %q, want %q", c.machine, got, c.dir)
			}
			on := c.acc.On(c.machine, data)
			if on.ConfigDir != c.dir || on == &c.acc {
				t.Errorf("On: %q", on.ConfigDir)
			}
		})
	}
}

func TestPickAccountKeepsToTheMachinesLoggedIn(t *testing.T) {
	now := time.Now()
	accs := []*Account{
		{ID: "claude-1", Provider: "claude", Status: AccountOK, CeilingPct: 100, ConfigDir: "accounts/claude-1", Machines: []string{"m1", "m2"}},
	}
	if a := PickAccount(accs, "claude", "m2", "demo", now); a == nil || a.ID != "claude-1" {
		t.Errorf("m2 is logged in: %v", a)
	}
	if a := PickAccount(accs, "claude", "m3", "demo", now); a != nil {
		t.Errorf("m3 never logged in, picked %v", a.ID)
	}
}
