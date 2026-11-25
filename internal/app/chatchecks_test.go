package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

func TestRunChecksInChatPostsTheResultForTheAdvisor(t *testing.T) {
	g := &testkit.FakeGit{Main: "c0ffee1234"}
	e := newEnv(t, withFakeGit(g))
	thread := "CG-ABCDE"
	if err := e.svc.RunChecksInChat(e.ctx, "demo", owner(), thread); err != nil {
		t.Fatal(err)
	}
	var msgs []*domain.DiscussionMessage
	eventually(t, "the result is posted", func() bool {
		msgs, _ = e.svc.Discussion("demo", owner(), thread)
		return len(msgs) == 2 && !e.svc.ChecksRunning("demo")
	})
	if msgs[0].Role != domain.DiscussChecks || !strings.Contains(msgs[0].Text, "Running the checks on main c0ffee1") || !strings.Contains(msgs[0].Text, "`true`") {
		t.Fatalf("start %+v", msgs[0])
	}
	if !strings.Contains(msgs[1].Text, "all 1 passed") || !strings.Contains(msgs[1].Text, "✓ main passed") {
		t.Fatalf("result %q", msgs[1].Text)
	}
	// The advisor reads it as Barnraise's, not as its own words.
	if p := advisorPrompt(nil, nil, nil, nil, nil, msgs); !strings.Contains(p, "**Barnraise (checks run on main):**") {
		t.Fatal(p)
	}
	if !strings.Contains(advisorChatSystem(e.p, ChatGoal), "Run the checks") {
		t.Fatal("the advisor is not told how to get the checks run")
	}
}

func TestRunChecksInChatRefusals(t *testing.T) {
	g := &testkit.FakeGit{Main: "c1"}
	e := newEnv(t, withFakeGit(g))
	if err := e.svc.RunChecksInChat(e.ctx, "demo", agent("implementer", "", ""), "CG-ABCDE"); err == nil {
		t.Fatal("an agent cannot run the owner's checks")
	}
	if err := e.svc.RunChecksInChat(e.ctx, "demo", owner(), "I-999"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	e.svc.chatChecks.Store("demo", "CG-OTHER")
	if err := e.svc.RunChecksInChat(e.ctx, "demo", owner(), "CG-ABCDE"); !errors.Is(err, ErrConflict) {
		t.Fatal("two runs at once:", err)
	}
	e.svc.chatChecks.Delete("demo")
	// Only what runs here, with a command: never a dispatch elsewhere.
	cfg := &domain.ProjectConfig{Checks: map[string]domain.CheckDef{
		"unit": {Run: "make test"}, "ci": {RunsOn: "github-actions", Workflow: "ci.yml"}, "empty": {RunsOn: "local"}}}
	if got := localChecks(cfg); len(got) != 1 || got[0] != "unit" {
		t.Fatal(got)
	}
	g.WorktreeErr = errors.New("disk full")
	if err := e.svc.RunChecksInChat(e.ctx, "demo", owner(), "CG-ABCDE"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the failure is said", func() bool {
		msgs, _ := e.svc.Discussion("demo", owner(), "CG-ABCDE")
		return len(msgs) == 2 && strings.Contains(msgs[1].Text, "no checkout of main")
	})
}
