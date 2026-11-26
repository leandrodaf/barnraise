package domain

import "testing"

// Commands agents ran that the policy refused by mistake (2026-11-26), and
// what it must still refuse.
func TestPolicyReadsCommandsLikeTheShell(t *testing.T) {
	p := DefaultPolicy()
	p.Home = "/home/u"
	p.Worktree = "/home/u/.local/share/barnraise/worktrees/demo/I-58"
	allowed := []string{
		// A sed script in quotes redirects nothing.
		`cd /home/u/.local/share/barnraise/worktrees/demo/I-58 && env | grep -iE "^(DATABASE|PG|TEST_)" | sed 's/=.*/=<set>/'`,
		// grep's pattern is not a file.
		`grep -n "aparelho\|prancha.py" scripts/mobile/prints.sh | head -60; echo ---; grep -rn "prancha .." scripts`,
		// A cd inside the command moves the relative paths with it.
		`cd internal/handlers && grep -n periodicidade ../httpapi/*.go`,
		// A heredoc body is text, whatever it says.
		"cat >> plan.md <<'EOF'\n## Rework\n- rm -rf / and > /etc/passwd are only words here\nEOF\ngit add plan.md",
		// Scratch space.
		`cp bastion.gd /tmp/i6-bastion.bak && sed -i '753s/.*/x/' bastion.gd`,
		`rm -rf /var/tmp/barnraise-1000/claude-1000/tmp.djBV03XrKD`,
		`echo "a > b" > out.txt`,
		`git commit -m "fix: keep x > y; rm -rf nothing"`,
		`echo x >| notes.txt`,
	}
	for _, c := range allowed {
		if d := p.Decide(ToolCall{Tool: "Bash", Command: c}, nil); !d.Allow {
			t.Errorf("%q denied: %s", c, d.Reason)
		}
	}
	denied := map[string]string{
		`echo x > "/home/u/.bashrc"`:              "outside_worktree",
		`cat "/home/u/.ssh/id_rsa"`:               "deny_read",
		`cat '/home/u/.ssh/id_rsa'`:               "deny_read",
		`cd .. && rm -rf I-1`:                     "self",
		`cd /home/u && echo x > .profile`:         "outside_worktree",
		`grep -e secret -f /home/u/.ssh/id_rsa .`: "deny_read",
		`grep token /home/u/.netrc`:               "deny_read",
		`echo x >| .env`:                          "deny_write",
		`rm -rf /tmp`:                             "outside_worktree",
		"cat <<EOF > /home/u/x\nhi\nEOF":          "outside_worktree",
		`a=1; tee /etc/hosts < x`:                 "outside_worktree",
	}
	for c, rule := range denied {
		if d := p.Decide(ToolCall{Tool: "Bash", Command: c}, nil); d.Allow || d.Rule != rule {
			t.Errorf("%q: allow=%v rule=%q, want %q", c, d.Allow, d.Rule, rule)
		}
	}
}

func TestShellWords(t *testing.T) {
	got := words(`grep -n "a b" 'c d' e\ f g`)
	want := []string{"grep", "-n", "a b", "c d", "e f", "g"}
	if len(got) != len(want) {
		t.Fatalf("%q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%q", got)
		}
	}
	if m := mask(`a 'b>c' "d\"e" f\>g`); m != `a 'qqq' "qqqq" f\qg` {
		t.Fatalf("mask %q", m)
	}
	if s := searchFiles([]string{"-A", "3", "-e", "x", "--file=pats", "a.go", "-g", "*.go", "b"}); len(s) != 3 || s[0] != "pats" || s[1] != "a.go" || s[2] != "b" {
		t.Fatalf("search %q", s)
	}
}

// A session reads its own item's check logs and its worktree's git state,
// nothing else of Barnraise's folders; a port probe writes no file.
func TestPolicyOwnLogsAndProbes(t *testing.T) {
	p := DefaultPolicy()
	p.Home = "/home/u"
	p.Worktree = "/home/u/.local/share/barnraise/worktrees/demo/I-36"
	p.ReadAlso = []string{"/home/u/.local/share/barnraise/logs/demo/*-I-36-*", "/home/u/.local/share/barnraise/projects/demo/.git/worktrees/I-36/**"}
	for _, c := range []string{
		`grep -E "^(FAIL|ok)" /home/u/.local/share/barnraise/logs/demo/20261125T161820Z-I-36-main-branch-3.log`,
		`cat /home/u/.local/share/barnraise/projects/demo/.git/worktrees/I-36/rebase-merge/git-rebase-todo`,
		`(echo > /dev/tcp/127.0.0.1/5441) 2>/dev/null && echo open`,
	} {
		if d := p.Decide(ToolCall{Tool: "Bash", Command: c}, nil); !d.Allow {
			t.Errorf("%q: %s", c, d.Reason)
		}
	}
	for _, c := range []string{
		`cat /home/u/.local/share/barnraise/logs/demo/20261125T161820Z-I-37-main-branch-3.log`,
		`echo x > /home/u/.local/share/barnraise/logs/demo/20261125T161820Z-I-36-main.log`,
		`cat /home/u/.local/share/barnraise/projects/demo/.git/worktrees/I-37/HEAD`,
	} {
		if d := p.Decide(ToolCall{Tool: "Bash", Command: c}, nil); d.Allow {
			t.Errorf("%q must stay closed", c)
		}
	}
}
