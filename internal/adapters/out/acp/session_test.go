package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/provcommon"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

func TestMain(m *testing.M) {
	fakeAgentMain()
	os.Exit(m.Run())
}

// fixture is a worktree with a session folder, a compiled policy and an
// MCP configuration, as the runtime writes them.
type fixture struct {
	dir, sessDir, record string
	spec                 ports.SessionSpec
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	sessDir := filepath.Join(dir, ".barnraise-session")
	if err := os.MkdirAll(sessDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pol := domain.DefaultPolicy()
	pol.Home = t.TempDir()
	pol.Worktree = dir
	data, _ := json.Marshal(pol)
	polPath := filepath.Join(sessDir, "policy.json")
	if err := os.WriteFile(polPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	mcp := `{"mcpServers":{"barnraise":{"command":"/bin/barnraise","args":["mcp"],"env":{"BARNRAISE_TOKEN":"tok"}},"docs":{"url":"https://docs.example/mcp","type":"http"}}}`
	mcpPath := filepath.Join(sessDir, "mcp.json")
	if err := os.WriteFile(mcpPath, []byte(mcp), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fixture{dir: dir, sessDir: sessDir, record: filepath.Join(t.TempDir(), "agent.jsonl")}
	f.spec = ports.SessionSpec{Agent: "dev-1", Role: "developer", Account: domain.Account{ID: "myagent-1"}, Model: "big", Workdir: dir,
		Prompt: "Do the item.", SystemPrompt: "You are a developer.", MCPConfig: mcpPath,
		Env: map[string]string{"BARNRAISE_POLICY": polPath, "BARNRAISE_ITEM": "I-1"},
		ACP: &ports.ACPAgent{Name: "myagent", Command: "fake-agent", Args: []string{"--acp"}, Env: map[string]string{"MYAGENT_MODE": "x"}}}
	return f
}

func (f *fixture) open(t *testing.T, sc scenario) (*session, []ports.ProviderEvent, ports.SessionResult, error) {
	t.Helper()
	sc.Record = f.record
	p := New(Options{Command: fakeCommand(sc), Grace: 500 * time.Millisecond, Sandbox: func() bool { return false }, Version: "1.2.3"})
	sess, err := p.Open(context.Background(), f.spec)
	if err != nil {
		t.Fatal(err)
	}
	var evs []ports.ProviderEvent
	for ev := range sess.Events() {
		evs = append(evs, ev)
	}
	res, werr := sess.Wait()
	return sess.(*session), evs, res, werr
}

// received is what the fake agent got from Barnraise, by method (requests)
// and by answered id (responses to its own requests).
func (f *fixture) received(t *testing.T) (map[string]json.RawMessage, []json.RawMessage) {
	t.Helper()
	fh, err := os.Open(f.record)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	byMethod := map[string]json.RawMessage{}
	var answers []json.RawMessage
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var m fakeMsg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if m.Method != "" {
			byMethod[m.Method] = m.Params
		} else {
			answers = append(answers, append(json.RawMessage(nil), sc.Bytes()...))
		}
	}
	return byMethod, answers
}

func sendUpdate(v map[string]any) fakeStep {
	b, _ := json.Marshal(v)
	return fakeStep{Update: b}
}

func request(method string, v map[string]any) fakeStep {
	v["sessionId"] = "sess-1"
	b, _ := json.Marshal(v)
	return fakeStep{Request: method, Params: b}
}

func kindsOf(evs []ports.ProviderEvent) []ports.ProviderEventKind {
	var out []ports.ProviderEventKind
	for _, e := range evs {
		out = append(out, e.Kind)
	}
	return out
}

func TestSessionHappyPath(t *testing.T) {
	f := newFixture(t)
	sc := scenario{Models: []string{"small", "big"}, Usage: true, Steps: []fakeStep{
		sendUpdate(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]string{"type": "text", "text": "thinking"}}),
		sendUpdate(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "Reading first."}}),
		sendUpdate(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "Read main.go", "kind": "read", "status": "pending",
			"locations": []map[string]string{{"path": "/x/main.go"}}, "rawInput": map[string]string{"path": "/x/main.go"}}),
		sendUpdate(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "completed",
			"content": []map[string]any{{"type": "content", "content": map[string]string{"type": "text", "text": "package main"}}}}),
		sendUpdate(map[string]any{"sessionUpdate": "plan", "entries": []map[string]string{{"content": "step", "priority": "high", "status": "pending"}}}),
		sendUpdate(map[string]any{"sessionUpdate": "usage_update", "used": 1000, "size": 200000, "cost": map[string]any{"amount": 0.25, "currency": "USD"}}),
		sendUpdate(map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": "m2", "content": map[string]string{"type": "text", "text": "All "}}),
		sendUpdate(map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": "m2", "content": map[string]string{"type": "text", "text": "done."}}),
	}}
	_, evs, res, err := f.open(t, sc)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	got := kindsOf(evs)
	want := []ports.ProviderEventKind{ports.PEInit, ports.PEMessage, ports.PEToolUse, ports.PEToolResult, ports.PEUsage, ports.PEMessage, ports.PEMessage, ports.PEUsage, ports.PEDone}
	if strings.Join(kindStrings(got), ",") != strings.Join(kindStrings(want), ",") {
		t.Fatalf("events %v, want %v", got, want)
	}
	if evs[0].SessionID != "sess-1" || evs[2].Tool != "Read" || evs[3].Text != "package main" {
		t.Fatalf("events: %+v", evs[:4])
	}
	if res.Text != "All done." || res.SessionID != "sess-1" {
		t.Fatalf("result: %+v", res)
	}
	u := res.Usage
	if u.Input != 120 || u.Output != 35 || u.CacheRead != 1000 || u.CacheWrite != 7 || u.CostUSD != 0.25 || u.Provider != "myagent" || u.Account != "myagent-1" || u.Model != "big" {
		t.Fatalf("usage: %+v", u)
	}
	byMethod, _ := f.received(t)
	var init struct {
		ProtocolVersion    int `json:"protocolVersion"`
		ClientCapabilities struct {
			FS       map[string]bool `json:"fs"`
			Terminal bool            `json:"terminal"`
		} `json:"clientCapabilities"`
		ClientInfo struct{ Name, Version string } `json:"clientInfo"`
	}
	_ = json.Unmarshal(byMethod["initialize"], &init)
	if init.ProtocolVersion != 1 || !init.ClientCapabilities.FS["readTextFile"] || !init.ClientCapabilities.FS["writeTextFile"] || !init.ClientCapabilities.Terminal || init.ClientInfo.Version != "1.2.3" {
		t.Fatalf("initialize: %s", byMethod["initialize"])
	}
	var ns struct {
		Cwd        string           `json:"cwd"`
		McpServers []map[string]any `json:"mcpServers"`
	}
	_ = json.Unmarshal(byMethod["session/new"], &ns)
	if ns.Cwd != f.dir || len(ns.McpServers) != 1 || ns.McpServers[0]["name"] != "barnraise" || ns.McpServers[0]["command"] != "/bin/barnraise" {
		t.Fatalf("session/new (the http server is left out: the agent cannot reach it): %s", byMethod["session/new"])
	}
	if !strings.Contains(string(byMethod["session/new"]), `{"name":"BARNRAISE_TOKEN","value":"tok"}`) {
		t.Fatalf("mcp env: %s", byMethod["session/new"])
	}
	if !strings.Contains(string(byMethod["session/set_model"]), `"modelId":"big"`) {
		t.Fatalf("set_model: %s", byMethod["session/set_model"])
	}
	prompt := string(byMethod["session/prompt"])
	if !strings.Contains(prompt, "You are a developer.") || !strings.Contains(prompt, "Do the item.") {
		t.Fatalf("prompt: %s", prompt)
	}
	// The transcript has the whole conversation.
	tr, err := os.ReadFile(filepath.Join(f.sessDir, "acp-myagent.jsonl"))
	if err != nil || !strings.Contains(string(tr), `"dir":"out"`) || !strings.Contains(string(tr), "agent_thought_chunk") {
		t.Fatalf("transcript: %v %.200s", err, tr)
	}
}

func kindStrings(k []ports.ProviderEventKind) []string {
	out := make([]string, len(k))
	for i, v := range k {
		out[i] = string(v)
	}
	return out
}

func TestSessionPolicyAnswersTheAgent(t *testing.T) {
	f := newFixture(t)
	ok := filepath.Join(f.dir, "src", "new.go")
	bad := filepath.Join(f.dir, ".github", "workflows", "ci.yml")
	if err := os.WriteFile(filepath.Join(f.dir, "README.md"), []byte("l1\nl2\nl3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := []map[string]string{{"optionId": "yes", "name": "Allow", "kind": "allow_once"}, {"optionId": "always", "name": "Always", "kind": "allow_always"}, {"optionId": "no", "name": "Reject", "kind": "reject_once"}}
	sc := scenario{Steps: []fakeStep{
		request("session/request_permission", map[string]any{"toolCall": map[string]any{"toolCallId": "p1", "title": "Edit ci", "kind": "edit", "locations": []map[string]string{{"path": bad}}}, "options": opts}),
		request("session/request_permission", map[string]any{"toolCall": map[string]any{"toolCallId": "p2", "title": "Edit new.go", "kind": "edit", "locations": []map[string]string{{"path": ok}}}, "options": opts}),
		request("session/request_permission", map[string]any{"toolCall": map[string]any{"toolCallId": "p3", "title": "`sudo rm -rf /`", "kind": "execute", "rawInput": map[string]string{"command": "sudo rm -rf /"}}, "options": opts}),
		request("session/request_permission", map[string]any{"toolCall": map[string]any{"toolCallId": "p4", "title": "Mode", "kind": "switch_mode"}, "options": opts}),
		request("fs/write_text_file", map[string]any{"path": ok, "content": "package src\n"}),
		request("fs/write_text_file", map[string]any{"path": bad, "content": "on: push"}),
		request("fs/read_text_file", map[string]any{"path": filepath.Join(f.dir, "README.md"), "line": 2, "limit": 1}),
		request("fs/read_text_file", map[string]any{"path": filepath.Join(f.dir, "release.keystore")}),
		request("terminal/create", map[string]any{"command": "sudo", "args": []string{"ls"}}),
		request("terminal/create", map[string]any{"command": "echo", "args": []string{"hello", "world"}, "cwd": f.dir}),
		request("terminal/wait_for_exit", map[string]any{"terminalId": "term-1"}),
		request("terminal/output", map[string]any{"terminalId": "term-1"}),
		request("terminal/release", map[string]any{"terminalId": "term-1"}),
		request("terminal/output", map[string]any{"terminalId": "term-1"}),
		request("unknown/method", map[string]any{}),
		// An edit the agent did with its own tool, never asking.
		sendUpdate(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t9", "title": "Write .env", "kind": "edit", "status": "in_progress",
			"content": []map[string]any{{"type": "diff", "path": filepath.Join(f.dir, ".env"), "newText": "X=1"}}}),
		sendUpdate(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t9", "status": "completed"}),
	}}
	_, evs, _, err := f.open(t, sc)
	if err != nil {
		t.Fatal(err)
	}
	_, answers := f.received(t)
	if len(answers) != 15 {
		t.Fatalf("answers: %d\n%s", len(answers), joinRaw(answers))
	}
	expect := []string{
		`"optionId":"no"`, `"optionId":"yes"`, `"optionId":"no"`, `"optionId":"no"`,
		`"result":{}`, `Barnraise policy: writing to .github/workflows/ci.yml is blocked`,
		`"content":"l2\n"`, `access to credentials and secrets is blocked`,
		`command blocked by policy`, `"terminalId":"term-1"`, `"exitCode":0`, `"output":"hello world\n"`, `"result":{}`, `unknown terminal`, `method not found`,
	}
	for i, want := range expect {
		if !strings.Contains(string(answers[i]), want) {
			t.Errorf("answer %d: %s, want %s", i, answers[i], want)
		}
	}
	if data, err := os.ReadFile(ok); err != nil || string(data) != "package src\n" {
		t.Fatalf("written file: %q %v", data, err)
	}
	if _, err := os.Stat(bad); err == nil {
		t.Fatal("the denied file was written")
	}
	var rules []string
	for _, ev := range evs {
		if ev.Kind == ports.PEPermission && ev.IsError {
			var r struct{ Rule string }
			_ = json.Unmarshal(ev.Raw, &r)
			rules = append(rules, ev.Tool+":"+r.Rule)
		}
	}
	want := "Edit:deny_write,Bash:deny_command,SwitchMode:mode,Write:deny_write,Read:deny_read,Bash:deny_command,Edit:deny_write"
	if strings.Join(rules, ",") != want {
		t.Fatalf("violations %s, want %s", strings.Join(rules, ","), want)
	}
	late := false
	for _, ev := range evs {
		late = late || ev.Kind == ports.PEPermission && strings.Contains(ev.Text, "after the fact")
	}
	if !late {
		t.Fatal("the edit done without asking was not reported")
	}
}

func joinRaw(r []json.RawMessage) string {
	var s []string
	for _, x := range r {
		s = append(s, string(x))
	}
	return strings.Join(s, "\n")
}

func TestSessionAuthRequired(t *testing.T) {
	f := newFixture(t)
	_, evs, _, err := f.open(t, scenario{AuthRequired: true})
	if err == nil || !strings.Contains(err.Error(), "needs a login (Log in with the browser)") {
		t.Fatalf("err: %v", err)
	}
	if k := kindsOf(evs); len(k) != 1 || k[0] != ports.PEError {
		t.Fatalf("events: %v", k)
	}
}

func TestSessionUnknownVersion(t *testing.T) {
	f := newFixture(t)
	_, _, _, err := f.open(t, scenario{Version: 7})
	if err == nil || !strings.Contains(err.Error(), "protocol version 7; Barnraise speaks 1") {
		t.Fatalf("err: %v", err)
	}
}

func TestSessionAgentCrashes(t *testing.T) {
	f := newFixture(t)
	_, _, res, err := f.open(t, scenario{ExitAtInit: true})
	if err == nil || !strings.Contains(err.Error(), "ended (exit code 3) before answering initialize") || !strings.Contains(err.Error(), "crashed on purpose") {
		t.Fatalf("err: %v", err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit code %d", res.ExitCode)
	}
}

func TestSessionLimit(t *testing.T) {
	f := newFixture(t)
	_, evs, res, err := f.open(t, scenario{PromptError: "You hit your usage limit; try again in 2 hours"})
	if err != nil {
		t.Fatalf("a limit is not an error of the agent: %v", err)
	}
	if !res.Limited || res.ResetsAt.IsZero() {
		t.Fatalf("result: %+v", res)
	}
	if k := kindsOf(evs); k[len(k)-1] != ports.PELimit {
		t.Fatalf("events: %v", k)
	}
}

func TestSessionRefusal(t *testing.T) {
	f := newFixture(t)
	_, _, _, err := f.open(t, scenario{StopReason: "refusal"})
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("err: %v", err)
	}
}

func TestSessionStopCancelsTheTurn(t *testing.T) {
	f := newFixture(t)
	sc := scenario{WaitCancel: true, Record: f.record}
	p := New(Options{Command: fakeCommand(sc), Grace: 2 * time.Second, Sandbox: func() bool { return false }})
	sess, err := p.Open(context.Background(), f.spec)
	if err != nil {
		t.Fatal(err)
	}
	for ev := range sess.Events() {
		if ev.Kind == ports.PEInit {
			_ = sess.Stop()
		}
	}
	_, err = sess.Wait()
	if !errors.Is(err, provcommon.ErrStopped) {
		t.Fatalf("err: %v", err)
	}
	byMethod, _ := f.received(t)
	if _, ok := byMethod["session/cancel"]; !ok {
		t.Fatal("no session/cancel sent")
	}
}

func TestSessionTimeout(t *testing.T) {
	f := newFixture(t)
	f.spec.Timeout = 300 * time.Millisecond
	_, _, _, err := f.open(t, scenario{WaitCancel: true})
	if !errors.Is(err, provcommon.ErrTimeout) {
		t.Fatalf("err: %v", err)
	}
}

func TestSessionAgentThatDoesNotEnd(t *testing.T) {
	f := newFixture(t)
	start := time.Now()
	_, _, res, err := f.open(t, scenario{IgnoreEOF: true})
	if err != nil {
		t.Fatalf("the turn ended well; the forced end is ours: %v", err)
	}
	if res.ExitCode != -1 || time.Since(start) > 10*time.Second {
		t.Fatalf("exit code %d after %s", res.ExitCode, time.Since(start))
	}
}

func TestSessionResume(t *testing.T) {
	f := newFixture(t)
	f.spec.ResumeID = "old-7"
	_, evs, res, err := f.open(t, scenario{LoadSession: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionID != "old-7" {
		t.Fatalf("session %q", res.SessionID)
	}
	for _, ev := range evs {
		if ev.Kind == ports.PEMessage {
			t.Fatalf("the replayed history came out as events: %+v", ev)
		}
	}
	byMethod, _ := f.received(t)
	if _, ok := byMethod["session/load"]; !ok {
		t.Fatal("no session/load")
	}
}

func TestOpenNeedsAnAgent(t *testing.T) {
	p := New(Options{})
	if _, err := p.Open(context.Background(), ports.SessionSpec{Workdir: t.TempDir()}); err == nil {
		t.Fatal("no agent program accepted")
	}
	if _, err := p.Open(context.Background(), ports.SessionSpec{ACP: &ports.ACPAgent{Command: "x"}}); err == nil {
		t.Fatal("no workdir accepted")
	}
	caps, err := New(Options{Sandbox: func() bool { return true }}).Detect(context.Background())
	if err != nil || caps.Version != "acp/1" || !caps.Sandbox || !caps.MCP {
		t.Fatalf("caps %+v %v", caps, err)
	}
	if p.Name() != "acp" || p.Family() != "acp" {
		t.Fatal("name")
	}
}

func TestOpenMissingProgram(t *testing.T) {
	f := newFixture(t)
	f.spec.ACP.Command = filepath.Join(t.TempDir(), "nope")
	p := New(Options{Sandbox: func() bool { return false }})
	if _, err := p.Open(context.Background(), f.spec); err == nil {
		t.Fatal("a missing program started")
	}
}
