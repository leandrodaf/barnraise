package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/adapters/in/cli"
	"github.com/leandrodaf/barnraise/internal/client"
	"github.com/leandrodaf/barnraise/internal/paths"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// cliRun runs the barnraise CLI in-process.
func cliRun(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	old, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer os.Chdir(old)
	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	code := cli.Main(args, cli.Hooks{Version: "test"})
	w.Close()
	os.Stdout = stdout
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return buf.String(), code
}

type harness struct {
	t      *testing.T
	root   string
	remote string
	cl     *client.Client
	cancel context.CancelFunc
	done   chan error
}

const demoYAML = `project: demo
template: blank
language: en
approves: owner
checks:
  main: "test -f README.md && for t in tests/repro_*.sh; do [ -f \"$t\" ] && { sh \"$t\" || exit 1; }; done; true"
lease: { renew: 1s, ttl: 3s }
`

func startHarness(t *testing.T, yml string) *harness {
	t.Helper()
	return startHarnessWith(t, yml, cli.DaemonOptions{Fake: true})
}

func startHarnessWith(t *testing.T, yml string, opts cli.DaemonOptions) *harness {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()
	t.Setenv("BARNRAISE_CONFIG_DIR", filepath.Join(base, "cfg"))
	t.Setenv("BARNRAISE_DATA_DIR", filepath.Join(base, "data"))
	t.Setenv("XDG_RUNTIME_DIR", base)
	t.Setenv("BARNRAISE_PROJECT", "")
	t.Setenv("BARNRAISE_TOKEN", "")
	remote := filepath.Join(base, "remote.git")
	root := filepath.Join(base, "repo")
	git(t, base, "init", "-q", "--bare", "-b", "main", remote)
	git(t, base, "clone", "-q", remote, root)
	git(t, root, "checkout", "-q", "-B", "main")
	os.WriteFile(filepath.Join(root, "README.md"), []byte("# demo\n"), 0o644)
	os.WriteFile(filepath.Join(root, "barnraise.yaml"), []byte(yml), 0o644)
	if out, code := cliRun(t, root, "init", "-y", "--force", "--no-accounts", "--template", "blank", "--check", "true"); code != 0 {
		t.Fatalf("init: %s", out)
	}
	os.WriteFile(filepath.Join(root, "barnraise.yaml"), []byte(yml), 0o644) // keep the test's config
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "init")
	git(t, root, "push", "-q", "origin", "main")
	os.MkdirAll(filepath.Join(base, "cfg"), 0o700)
	os.WriteFile(filepath.Join(base, "cfg", "daemon.yaml"), []byte(fmt.Sprintf("machine: e2e\nlisten: off\nnats_port: -1\nprojects: [%s]\n", root)), 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{t: t, root: root, remote: remote, cancel: cancel, done: make(chan error, 1)}
	go func() { h.done <- runDaemon(ctx, opts) }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		tok, err := os.ReadFile(paths.OwnerTokenFile())
		if err == nil {
			h.cl = client.New(client.Options{Socket: paths.Socket(), Token: strings.TrimSpace(string(tok))})
			if _, err := h.cl.ListProjects(ctx, connect.NewRequest(&pb.Empty{})); err == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.done:
		case <-time.After(15 * time.Second):
		}
	})
	return h
}

func (h *harness) create(r *pb.CreateItemRequest) *pb.Item {
	h.t.Helper()
	r.Project = "demo"
	res, err := h.cl.CreateItem(context.Background(), connect.NewRequest(r))
	if err != nil {
		h.t.Fatal(err)
	}
	if res.Msg.Item == nil {
		h.t.Fatalf("no item: %v", res.Msg)
	}
	return res.Msg.Item
}

func (h *harness) item(id string) *pb.Item {
	res, err := h.cl.GetItem(context.Background(), connect.NewRequest(&pb.ItemRef{Project: "demo", Id: id}))
	if err != nil {
		h.t.Fatal(err)
	}
	return res.Msg.Item
}

func (h *harness) waitStatus(id string, statuses ...string) *pb.Item {
	h.t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		it := h.item(id)
		if slices.Contains(statuses, it.Status) {
			return it
		}
		if time.Now().After(deadline) {
			tl, _ := h.cl.Timeline(context.Background(), connect.NewRequest(&pb.ItemRef{Project: "demo", Id: id}))
			var lines []string
			for _, e := range tl.Msg.GetEntries() {
				lines = append(lines, e.Type+" "+e.Summary)
			}
			h.t.Fatalf("%s stayed %s at step %s, want %v\n%s", id, it.Status, it.Step, statuses, strings.Join(lines, "\n"))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func crit(text string) []*pb.Criterion {
	return []*pb.Criterion{{Text: text, Check: "main"}}
}

func TestEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end")
	}
	h := startHarness(t, demoYAML)
	ctx := context.Background()

	t.Run("feature goes from the queue to main", func(t *testing.T) {
		it := h.create(&pb.CreateItemRequest{Title: "Cards screen", Area: "cards", Body: "fake: write app/cards.txt the cards list",
			Criteria: crit("WHEN the user opens cards, THE SYSTEM SHALL list the cards")})
		got := h.waitStatus(it.Id, "integrated")
		if got.MergeCommit == "" {
			t.Fatal("no merge commit")
		}
		log := git(t, h.root, "--git-dir", h.remote, "log", "--format=%s%n%b", "main")
		if !strings.Contains(log, "feat(cards): cards screen") || !strings.Contains(log, "Barnraise-Item: "+it.Id) {
			t.Fatalf("main log: %s", log)
		}
		files := git(t, h.root, "--git-dir", h.remote, "ls-tree", "-r", "--name-only", "main")
		if !strings.Contains(files, "app/cards.txt") || strings.Contains(files, "plan.md") || strings.Contains(files, "criteria.json") {
			t.Fatalf("main tree: %s", files)
		}
		for _, c := range got.Criteria {
			if !c.Met || c.Evidence == "" {
				t.Fatalf("criterion not marked: %+v", c)
			}
		}
	})

	t.Run("rework with the reason, then integrates", func(t *testing.T) {
		it := h.create(&pb.CreateItemRequest{Title: "Wallet", Area: "wallet", Body: "fake: rework-once\nfake: write app/wallet.txt balance",
			Criteria: crit("WHEN the user opens the wallet, THE SYSTEM SHALL show the balance")})
		got := h.waitStatus(it.Id, "integrated")
		if len(got.Rework) != 1 || got.Rework[0].From != "quick-look" || got.Rework[0].To != "implement" {
			t.Fatalf("rework %+v", got.Rework)
		}
	})

	t.Run("bug needs a reproduction test that fails on main", func(t *testing.T) {
		it := h.create(&pb.CreateItemRequest{Title: "Crash on login", Kind: "bug", Priority: "P1", Area: "login",
			Criteria: crit("WHEN the user logs in, THE SYSTEM SHALL not crash")})
		got := h.waitStatus(it.Id, "integrated")
		if len(got.Rework) != 0 {
			t.Fatalf("unexpected rework %+v", got.Rework)
		}
		files := git(t, h.root, "--git-dir", h.remote, "ls-tree", "-r", "--name-only", "main")
		if !strings.Contains(files, "tests/repro_"+strings.ToLower(it.Id)+".sh") {
			t.Fatal("reproduction test must land with the fix")
		}
	})

	t.Run("risky item: two attempts, the checks choose", func(t *testing.T) {
		it := h.create(&pb.CreateItemRequest{Title: "Payments", Area: "payments", Risky: true, Body: "fake: write app/pay.txt charge",
			Criteria: crit("WHEN the user pays, THE SYSTEM SHALL charge the card")})
		h.waitStatus(it.Id, "integrated")
		tl, err := h.cl.Timeline(ctx, connect.NewRequest(&pb.ItemRef{Project: "demo", Id: it.Id}))
		if err != nil {
			t.Fatal(err)
		}
		var attempts, chosen int
		for _, e := range tl.Msg.Entries {
			if strings.Contains(e.Summary, "attempt") {
				attempts++
			}
			if strings.Contains(e.Summary, "chosen") {
				chosen++
			}
		}
		if chosen == 0 || attempts < 3 {
			t.Fatalf("expected two attempts and a choice; attempts=%d chosen=%d", attempts, chosen)
		}
		branches := git(t, h.root, "--git-dir", h.remote, "branch", "--list", "*-b")
		if strings.Contains(branches, strings.ToLower(it.Id)) || strings.Contains(branches, it.Id) {
			t.Fatalf("the second attempt's branch must be deleted: %s", branches)
		}
	})

	t.Run("blocked goes to the owner with a question", func(t *testing.T) {
		it := h.create(&pb.CreateItemRequest{Title: "Export CSV", Area: "export", Body: "fake: block",
			Criteria: crit("WHEN the user exports, THE SYSTEM SHALL write a CSV file")})
		got := h.waitStatus(it.Id, "blocked")
		if got.Step != "implement" {
			t.Fatalf("step %s", got.Step)
		}
		if _, err := h.cl.CloseItem(ctx, connect.NewRequest(&pb.CloseItemRequest{Project: "demo", Id: it.Id, Reason: "abandoned", Note: "test"})); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("the analyst refines drafts; questions go to the owner", func(t *testing.T) {
		it := h.create(&pb.CreateItemRequest{Title: "Settings", Area: "settings", Body: "[NEEDS CLARIFICATION: which settings?]"})
		if it.Status != "draft" {
			t.Fatalf("status %s", it.Status)
		}
		got := h.waitStatus(it.Id, "integrated")
		if len(got.Criteria) != 1 || got.Criteria[0].Check != "main" {
			t.Fatalf("criteria %+v", got.Criteria)
		}
		ask := h.create(&pb.CreateItemRequest{Title: "Balance", Area: "balance", Body: "fake: needs-owner [NEEDS CLARIFICATION: what to show?]"})
		deadline := time.Now().Add(30 * time.Second)
		for {
			if cur := h.item(ask.Id); cur.WaitingQuestion != "" {
				if cur.Status == "draft" || cur.Status == "waiting_owner" {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("no owner question for %s: %+v", ask.Id, h.item(ask.Id))
			}
			time.Sleep(200 * time.Millisecond)
		}
		if _, err := h.cl.CloseItem(ctx, connect.NewRequest(&pb.CloseItemRequest{Project: "demo", Id: ask.Id, Reason: "abandoned"})); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("pause stops everything and resume continues", func(t *testing.T) {
		if _, err := h.cl.Pause(ctx, connect.NewRequest(&pb.PauseRequest{Project: "demo", Why: "test"})); err != nil {
			t.Fatal(err)
		}
		it := h.create(&pb.CreateItemRequest{Title: "Profile", Area: "profile", Criteria: crit("WHEN the user opens profile, THE SYSTEM SHALL show the name")})
		time.Sleep(3 * time.Second)
		if s := h.item(it.Id).Status; s != "ready" {
			t.Fatalf("paused but item is %s", s)
		}
		if _, err := h.cl.Resume(ctx, connect.NewRequest(&pb.ProjectRef{Project: "demo"})); err != nil {
			t.Fatal(err)
		}
		h.waitStatus(it.Id, "integrated")
	})

	t.Run("proposals: shape, duplicates and approval", func(t *testing.T) {
		p := &pb.Proposal{Title: "Cache quotes", Problem: "quotes load slowly on the cards screen", Evidence: "log line 12", Impact: "slow", Size: "3h", Area: "cards", GoalLink: "speed", Kind: "improvement"}
		res, err := h.cl.Propose(ctx, connect.NewRequest(&pb.ProposeRequest{Project: "demo", Proposal: p}))
		if err != nil || res.Msg.Proposal.Id == "" {
			t.Fatal(err)
		}
		dup, err := h.cl.Propose(ctx, connect.NewRequest(&pb.ProposeRequest{Project: "demo", Proposal: p}))
		if err != nil || dup.Msg.Duplicate != res.Msg.Proposal.Id {
			t.Fatalf("duplicate must become a comment: %v %v", dup, err)
		}
		if _, err := h.cl.Propose(ctx, connect.NewRequest(&pb.ProposeRequest{Project: "demo", Proposal: &pb.Proposal{Title: "x"}})); err == nil {
			t.Fatal("missing fields must fail")
		}
		dec, err := h.cl.DecideProposal(ctx, connect.NewRequest(&pb.DecideProposalRequest{Project: "demo", Id: res.Msg.Proposal.Id, Decision: "approve"}))
		if err != nil || dec.Msg.Proposal.ItemId == "" {
			t.Fatal(err)
		}
		if h.item(dec.Msg.Proposal.ItemId).Status != "draft" {
			t.Fatal("approved proposals become draft items for the analyst")
		}
	})

	t.Run("status, metrics, timeline, context, find", func(t *testing.T) {
		st, err := h.cl.Status(ctx, connect.NewRequest(&pb.ProjectRef{Project: "demo"}))
		if err != nil || st.Msg.Counts["integrated"] < 3 || len(st.Msg.Machines) != 1 || st.Msg.Machines[0].Health != "green" {
			t.Fatalf("%v %v", st, err)
		}
		m, err := h.cl.Metrics(ctx, connect.NewRequest(&pb.MetricsRequest{Project: "demo", Window: "1d"}))
		if err != nil || m.Msg.IntegratedPerDay <= 0 || len(m.Msg.TokensByModel) == 0 {
			t.Fatalf("%v %v", m, err)
		}
		tl, err := h.cl.Timeline(ctx, connect.NewRequest(&pb.ItemRef{Project: "demo", Id: "I-1"}))
		if err != nil || len(tl.Msg.Entries) < 10 {
			t.Fatal(err)
		}
		c, err := h.cl.Context(ctx, connect.NewRequest(&pb.ContextRequest{Project: "demo", Id: "I-1"}))
		if err != nil || !strings.Contains(c.Msg.Text, "Cards screen") {
			t.Fatal(err)
		}
		f, err := h.cl.Find(ctx, connect.NewRequest(&pb.FindRequest{Project: "demo", Query: "I-1"}))
		if err != nil || len(f.Msg.Hits) == 0 || f.Msg.Hits[0].Id != "I-1" {
			t.Fatalf("%v %v", f, err)
		}
		why, err := h.cl.Why(ctx, connect.NewRequest(&pb.KnowledgeRequest{Project: "demo", Target: "app/cards.txt:1"}))
		if err == nil && !strings.Contains(why.Msg.Text, "I-1") {
			t.Fatalf("why: %s", why.Msg.Text)
		}
	})

	t.Run("scoped tokens and audit", func(t *testing.T) {
		tok, err := h.cl.CreateToken(ctx, connect.NewRequest(&pb.CreateTokenRequest{Name: "ci", Project: "demo", Scopes: []string{"checks:report"}, Ttl: "1d"}))
		if err != nil {
			t.Fatal(err)
		}
		ci := client.New(client.Options{Socket: paths.Socket(), Token: tok.Msg.Secret})
		if _, err := ci.Pause(ctx, connect.NewRequest(&pb.PauseRequest{Project: "demo", Why: "x"})); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("a CI token must not pause: %v", err)
		}
		if _, err := ci.ListItems(ctx, connect.NewRequest(&pb.ListItemsRequest{Project: "demo"})); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("checks:report cannot read items: %v", err)
		}
		bad := client.New(client.Options{Socket: paths.Socket(), Token: "nope"})
		if _, err := bad.Status(ctx, connect.NewRequest(&pb.ProjectRef{Project: "demo"})); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("bad token: %v", err)
		}
		au, err := h.cl.ListAudit(ctx, connect.NewRequest(&pb.AuditRequest{Limit: 100}))
		if err != nil || len(au.Msg.Entries) == 0 {
			t.Fatal(err)
		}
	})

	t.Run("owner approval step: approve the delivery", func(t *testing.T) {
		yml := demoYAML + `flows:
  post:
    steps:
      draft:  { role: implementer }
      owner:  { approve: owner }
      integrate: { run: merge_queue }
kinds: { post: post }
`
		os.WriteFile(filepath.Join(h.root, "barnraise.yaml"), []byte(yml), 0o644)
		if _, err := h.cl.ReloadProject(ctx, connect.NewRequest(&pb.ProjectRef{Project: "demo"})); err != nil {
			t.Fatal(err)
		}
		it := h.create(&pb.CreateItemRequest{Title: "Launch post", Kind: "post", Area: "blog", Body: "fake: write posts/launch.md hello",
			Criteria: crit("WHEN the post is published, THE SYSTEM SHALL show the title")})
		h.waitStatus(it.Id, "waiting_owner")
		if _, err := h.cl.Approve(ctx, connect.NewRequest(&pb.ApproveRequest{Project: "demo", Target: it.Id, What: "plan", Approve: true})); err == nil {
			t.Fatal("approving the wrong thing must fail")
		}
		if _, err := h.cl.Approve(ctx, connect.NewRequest(&pb.ApproveRequest{Project: "demo", Target: it.Id, What: "delivery", Approve: true})); err != nil {
			t.Fatal(err)
		}
		h.waitStatus(it.Id, "integrated")
		q, _ := h.cl.ListQuestions(ctx, connect.NewRequest(&pb.ProjectRef{Project: "demo"}))
		for _, qq := range q.Msg.Questions {
			if qq.ItemId == it.Id {
				t.Fatal("the approval question must be closed")
			}
		}
	})

	t.Run("hook blocks without the daemon and reports", func(t *testing.T) {
		dir := t.TempDir()
		pol := filepath.Join(dir, "policy.json")
		os.WriteFile(pol, []byte(`{"deny_write":[".github/workflows/**"],"deny_commands":["\\bgh\\s+secret\\b"],"worktree":"`+dir+`"}`), 0o600)
		t.Setenv("BARNRAISE_POLICY", pol)
		in := `{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"` + dir + `/.github/workflows/ci.yml"}}`
		out := runHook(t, in)
		if !strings.Contains(out, `"permissionDecision":"deny"`) {
			t.Fatalf("hook output %q", out)
		}
		if out := runHook(t, `{"hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"file_path":"`+dir+`/app.go"}}`); strings.Contains(out, "deny") {
			t.Fatal(out)
		}
	})

	t.Run("CLI: status and item list", func(t *testing.T) {
		out, code := cliRun(t, h.root, "status")
		if code != 0 || !strings.Contains(out, "Project demo") {
			t.Fatalf("%d %s", code, out)
		}
		out, code = cliRun(t, h.root, "item", "list", "-a", "--json")
		var v struct {
			Items []json.RawMessage `json:"items"`
		}
		if code != 0 || json.Unmarshal([]byte(out), &v) != nil || len(v.Items) < 5 {
			t.Fatalf("%d %s", code, out)
		}
	})
}

func runHook(t *testing.T, input string) string {
	t.Helper()
	r, w, _ := os.Pipe()
	stdin := os.Stdin
	os.Stdin = r
	go func() { w.WriteString(input); w.Close() }()
	defer func() { os.Stdin = stdin }()
	out, _ := cliRunStdin(t, "hook", "pre-tool")
	return out
}

func cliRunStdin(t *testing.T, args ...string) (string, int) {
	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	code := cli.Main(args, cli.Hooks{Version: "test"})
	w.Close()
	os.Stdout = stdout
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return buf.String(), code
}
