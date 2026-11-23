package domain

import "testing"

// The item's worktree lives under Barnraise's own data folder, which is
// otherwise off limits (state, tokens). On the first real run every agent
// was denied its own files: reading a source file was "credentials and
// secrets", writing plan.md was "changing Barnraise itself". Inside the
// worktree only the worktree rules apply.
func TestPolicyTheWorktreeUnderTheDataDirIsTheAgents(t *testing.T) {
	p := DefaultPolicy()
	p.Home = "/home/u"
	p.Worktree = "/home/u/.local/share/barnraise/worktrees/nobira/I-2"
	// SelfPaths as DefaultPolicy has them, with ~.
	allow := []ToolCall{
		{Tool: "Write", Paths: []string{p.Worktree + "/plan.md"}, Write: true},
		{Tool: "Read", Paths: []string{p.Worktree + "/backend/go.mod"}},
		{Tool: "Read", Paths: []string{"backend/internal/domain/plano_app_test.go"}},
		{Tool: "Edit", Paths: []string{"backend/cmd/api/main.go"}, Write: true},
		{Tool: "Bash", Command: "grep -rn pessoa backend/internal/app"},
	}
	for _, c := range allow {
		if d := p.Decide(c, nil); !d.Allow {
			t.Errorf("%s %v %q denied: %s", c.Tool, c.Paths, c.Command, d.Reason)
		}
	}
	deny := []ToolCall{
		{Tool: "Read", Paths: []string{"/home/u/.local/share/barnraise/barnraise.db"}},
		{Tool: "Read", Paths: []string{"/home/u/.local/share/barnraise/worktrees/nobira/I-3/main.go"}},
		{Tool: "Write", Paths: []string{"/home/u/.config/barnraise/owner.token"}, Write: true},
		{Tool: "Write", Paths: []string{p.Worktree + "/.env"}, Write: true},
		{Tool: "Read", Paths: []string{p.Worktree + "/android/app/release.keystore"}},
		{Tool: "Write", Paths: []string{p.Worktree + "/.barnraise/roles/implementer.md"}, Write: true},
		{Tool: "Read", Paths: []string{"/home/u/.ssh/id_ed25519"}},
	}
	for _, c := range deny {
		if d := p.Decide(c, nil); d.Allow {
			t.Errorf("%s %v allowed", c.Tool, c.Paths)
		}
	}
}

// Relative paths are resolved from where the agent is, like the shell does:
// from backend/internal/adapter/x, "../httpapi/*.go" was resolved from the
// worktree root, landed outside it, and was refused as a secret.
func TestPolicyRelativePathsFromTheAgentsFolder(t *testing.T) {
	p := DefaultPolicy()
	p.Home = "/home/u"
	p.Worktree = "/home/u/.local/share/barnraise/worktrees/nobira/I-58"
	p.Cwd = p.Worktree + "/backend/internal/adapter/push"
	p.PrivateTmp = true // a container: /tmp is the session's own
	for _, c := range []ToolCall{
		{Tool: "Read", Paths: []string{"../httpapi/rotas.go"}},
		{Tool: "Bash", Command: "grep -n Push ../httpapi/*.go"},
		{Tool: "Write", Paths: []string{"push_novo.go"}, Write: true},
		{Tool: "Bash", Command: "psql -f x.sql > /tmp/i58_cols.sql"},
		{Tool: "Write", Paths: []string{"/var/tmp/notes.txt"}, Write: true},
	} {
		if d := p.Decide(c, nil); !d.Allow {
			t.Errorf("%s %v %q denied: %s", c.Tool, c.Paths, c.Command, d.Reason)
		}
	}
	for _, c := range []ToolCall{
		{Tool: "Read", Paths: []string{"../../../../../I-3/main.go"}},
		{Tool: "Write", Paths: []string{"/tmp/../home/u/.bashrc"}, Write: true},
		{Tool: "Write", Paths: []string{"/etc/hosts"}, Write: true},
	} {
		if d := p.Decide(c, nil); d.Allow {
			t.Errorf("%s %v allowed", c.Tool, c.Paths)
		}
	}
}

// On the machine itself /tmp is shared with the owner: scratch writes there
// stay outside the worktree and refused.
func TestPolicySharedTmpStaysClosed(t *testing.T) {
	p := DefaultPolicy()
	p.Worktree = "/w/I-1"
	if d := p.Decide(ToolCall{Tool: "Write", Paths: []string{"/tmp/x"}, Write: true}, nil); d.Allow {
		t.Fatal("/tmp on the machine must stay closed")
	}
}
