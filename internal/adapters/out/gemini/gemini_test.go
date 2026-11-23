package gemini

// The fixtures in testdata follow the stream-json format of the Gemini CLI's
// headless mode as documented for gemini-cli 0.21 (events init, message,
// tool_use, tool_result, error and result with stats). They were written by
// hand from that documentation, not recorded from a live run; TestContract
// checks the format against an installed CLI.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/provcommon"
	"github.com/leandrodaf/barnraise/internal/adapters/out/provcommon/fakecli"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

func TestMain(m *testing.M) {
	fakecli.Main()
	os.Exit(m.Run())
}

func replay(t *testing.T, path string) (*Parser, []ports.ProviderEvent, []string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p := NewParser(domain.TokenUsage{Account: "gemini-1", Model: "gemini-2.5-flash"})
	var evs []ports.ProviderEvent
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		evs = append(evs, p.Parse(sc.Bytes())...)
	}
	evs = append(evs, p.Finish(0, "")...)
	return p, evs, lines
}

func kinds(evs []ports.ProviderEvent) []ports.ProviderEventKind {
	var out []ports.ProviderEventKind
	for _, e := range evs {
		out = append(out, e.Kind)
	}
	return out
}

func find(evs []ports.ProviderEvent, k ports.ProviderEventKind) []ports.ProviderEvent {
	var out []ports.ProviderEvent
	for _, e := range evs {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

func TestIdentity(t *testing.T) {
	p := New(Options{})
	if p.Name() != "gemini" || p.Family() != "google" {
		t.Fatalf("%s %s", p.Name(), p.Family())
	}
}

func TestGoldenBasic(t *testing.T) {
	p, evs, lines := replay(t, "testdata/session_basic.jsonl")
	want := []ports.ProviderEventKind{ports.PEInit, ports.PEMessage, ports.PEMessage, ports.PEMessage, ports.PEUsage, ports.PEDone}
	if got := kinds(evs); !slices.Equal(got, want) {
		t.Fatalf("kinds = %v", got)
	}
	const sid = "8f1c2d3e-4b5a-4c6d-9e7f-0a1b2c3d4e5f"
	if evs[0].SessionID != sid || evs[1].Text != "" || evs[2].Text != "po" || evs[3].Text != "ng" || string(evs[2].Raw) != lines[2] {
		t.Fatalf("events = %+v", evs[:4])
	}
	u := evs[4].Usage
	if u == nil || u.Input != 8310 || u.Output != 2 || u.CacheRead != 0 || u.Provider != "gemini" || u.Model != "gemini-2.5-flash" || u.Account != "gemini-1" {
		t.Fatalf("usage = %+v", u)
	}
	r := p.Result()
	if r.SessionID != sid || r.Text != "pong" || r.Limited || evs[5].IsError {
		t.Fatalf("result = %+v", r)
	}
}

func TestGoldenTools(t *testing.T) {
	p, evs, lines := replay(t, "testdata/session_tools.jsonl")
	uses := find(evs, ports.PEToolUse)
	res := find(evs, ports.PEToolResult)
	if len(uses) != 3 || len(res) != 3 {
		t.Fatalf("kinds = %v", kinds(evs))
	}
	var in struct {
		AbsolutePath string `json:"absolute_path"`
	}
	_ = json.Unmarshal(uses[0].Input, &in)
	if uses[0].Tool != "read_file" || in.AbsolutePath != "/w/main.go" {
		t.Fatalf("use 0 = %+v", uses[0])
	}
	if res[0].Tool != "read_file" || res[0].Text != "package main\n" || res[0].IsError {
		t.Fatalf("result 0 = %+v", res[0])
	}
	if !res[1].IsError || !strings.Contains(res[1].Text, "File not found") {
		t.Fatalf("result 1 = %+v", res[1])
	}
	if uses[2].Tool != "mcp_barnraise_item_get" || string(uses[2].Input) != `{"item":"I-42"}` || res[2].Text != "I-42: count the lines" {
		t.Fatalf("mcp = %+v %+v", uses[2], res[2])
	}
	// The warning and the unknown event pass through raw, as plain messages.
	if len(find(evs, ports.PEError)) != 0 {
		t.Fatalf("a warning is not an error: %v", kinds(evs))
	}
	for i, l := range lines {
		if !strings.Contains(l, "future_event") {
			continue
		}
		found := false
		for _, e := range evs {
			if string(e.Raw) == lines[i] && e.Kind == ports.PEMessage && e.Text == "" {
				found = true
			}
		}
		if !found {
			t.Fatal("unknown event type not passed through")
		}
	}
	r := p.Result()
	if r.Text != "212" || r.Usage.Input != 53376-41216 || r.Usage.CacheRead != 41216 || r.Usage.Output != 279 {
		t.Fatalf("result = %+v", r)
	}
}

func TestGoldenLimit(t *testing.T) {
	p, evs, _ := replay(t, "testdata/session_limit.jsonl")
	lim := find(evs, ports.PELimit)
	if len(lim) != 1 || len(find(evs, ports.PEError)) != 0 {
		t.Fatalf("kinds = %v", kinds(evs))
	}
	at := lim[0].ResetsAt
	if at.Year() != 2026 || at.Month() != time.November || at.Day() != 24 || at.Hour() != 15 || at.Minute() != 5 {
		t.Fatalf("resets at = %v", at)
	}
	done := find(evs, ports.PEDone)
	if len(done) != 1 || !done[0].IsError {
		t.Fatalf("done = %+v", done)
	}
	if r := p.Result(); !r.Limited || !r.ResetsAt.Equal(at) {
		t.Fatalf("result = %+v", r)
	}
}

func TestGoldenFailed(t *testing.T) {
	p, evs, _ := replay(t, "testdata/session_failed.jsonl")
	if len(find(evs, ports.PELimit)) != 0 || len(find(evs, ports.PEError)) != 2 {
		t.Fatalf("kinds = %v", kinds(evs))
	}
	if r := p.Result(); r.Limited || !strings.Contains(r.Text, "socket hang up") {
		t.Fatalf("result = %+v", r)
	}
	if d := find(evs, ports.PEDone); len(d) != 1 || !d[0].IsError {
		t.Fatalf("done = %+v", d)
	}
}

func TestParserShapes(t *testing.T) {
	p := NewParser(domain.TokenUsage{})
	// Content as a list of parts, an output that is not a string, a result
	// without tool_use, an error as a plain string.
	evs := p.Parse([]byte(`{"type":"message","role":"assistant","content":[{"text":"a"},{"text":"b"}]}`))
	if len(evs) != 1 || evs[0].Text != "ab" {
		t.Fatalf("parts = %+v", evs)
	}
	evs = p.Parse([]byte(`{"type":"tool_result","tool_id":"x","tool_name":"glob","status":"success","output":{"files":2}}`))
	if evs[0].Tool != "glob" || evs[0].Text != `{"files":2}` {
		t.Fatalf("output = %+v", evs[0])
	}
	evs = p.Parse([]byte(`{"type":"tool_use","tool_name":"list_directory","tool_id":"y"}`))
	if string(evs[0].Input) != "{}" {
		t.Fatalf("input = %s", evs[0].Input)
	}
	evs = p.Parse([]byte(`{"type":"error","error":"plain failure"}`))
	if evs[0].Kind != ports.PEError || evs[0].Text != "plain failure" {
		t.Fatalf("error = %+v", evs[0])
	}
	// A second limit only passes through.
	_ = p.Parse([]byte(`{"type":"error","message":"429 RESOURCE_EXHAUSTED"}`))
	evs = p.Parse([]byte(`{"type":"error","message":"429 RESOURCE_EXHAUSTED"}`))
	if evs[0].Kind != ports.PEMessage || !p.Result().Limited {
		t.Fatalf("second limit = %+v", evs)
	}
	// Text after a tool call replaces the text before it.
	evs = p.Parse([]byte(`{"type":"result","status":"error"}`))
	if d := evs[len(evs)-1]; d.Kind != ports.PEDone || !d.IsError || d.Text != "ab" {
		t.Fatalf("done = %+v", d)
	}
}

func TestParserNeverCrashes(t *testing.T) {
	p := NewParser(domain.TokenUsage{})
	for _, l := range []string{"x", "{}", "[]", `{"type":"message"}`, `{"type":"tool_result"}`, `{"type":"tool_use","parameters":null}`,
		`{"type":"result"}`, `{"type":"result","stats":{"input_tokens":1,"cached":5}}`, `{"type":"error","error":"plain"}`, `{"type":"new.kind"}`} {
		for _, e := range p.Parse([]byte(l)) {
			if len(e.Raw) == 0 {
				t.Fatalf("%q: no raw", l)
			}
		}
	}
	if p.Parse([]byte("not json")) != nil {
		t.Fatal("invalid JSON yields no event")
	}
}

func TestFinishWithoutResult(t *testing.T) {
	p := NewParser(domain.TokenUsage{})
	_ = p.Parse([]byte(`{"type":"init","session_id":"s-1"}`))
	evs := p.Finish(1, "Error: RESOURCE_EXHAUSTED: you have exhausted your capacity on this model\n")
	if len(evs) != 2 || evs[0].Kind != ports.PELimit || evs[1].Kind != ports.PEDone || !evs[1].IsError || evs[1].Usage != nil {
		t.Fatalf("finish = %+v", evs)
	}
	if r := p.Result(); r.SessionID != "s-1" || !r.Limited {
		t.Fatalf("result = %+v", r)
	}
	q := NewParser(domain.TokenUsage{})
	if evs := q.Finish(1, "plain crash"); len(evs) != 1 || evs[0].Kind != ports.PEDone {
		t.Fatalf("finish = %+v", evs)
	}
	if q.Result().SessionID != "" {
		t.Fatal("no session id")
	}
}

func fakeProvider(s fakecli.Script) *Provider {
	return New(Options{Command: fakecli.Command(s), Grace: 300 * time.Millisecond})
}

func TestDetect(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "call.json")
	p := fakeProvider(fakecli.Script{Record: rec, Outputs: map[string]string{
		"--version": "testdata/version.txt",
		"--help":    "testdata/help.txt",
	}})
	caps, err := p.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := ports.Capabilities{Version: "0.21.0", Headless: true, Stream: true, Resume: true, MCP: true}
	if caps != want {
		t.Fatalf("caps = %+v\nwant %+v", caps, want)
	}
	_ = os.Remove(rec)
	if _, err := p.Detect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rec); !os.IsNotExist(err) {
		t.Fatal("Detect not cached")
	}
}

func TestDetectOldCLI(t *testing.T) {
	p := fakeProvider(fakecli.Script{Outputs: map[string]string{
		"--version": "testdata/version.txt",
		"--help":    "testdata/help_old.txt",
	}})
	caps, err := p.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !caps.Headless || caps.Stream || caps.Resume || caps.MCP {
		t.Fatalf("caps = %+v", caps)
	}
}

func TestDetectFails(t *testing.T) {
	if _, err := fakeProvider(fakecli.Script{Exit: 1}).Detect(context.Background()); err == nil {
		t.Fatal("want error")
	}
	if _, err := fakeProvider(fakecli.Script{Outputs: map[string]string{"--version": "testdata/help_old.txt"}}).Detect(context.Background()); err == nil {
		t.Fatal("want no version error")
	}
}

func TestOpenBuildsCommand(t *testing.T) {
	dir := t.TempDir()
	rec := filepath.Join(dir, "call.json")
	sess := filepath.Join(dir, ".barnraise-session")
	_ = os.MkdirAll(sess, 0o700)
	mcp := filepath.Join(sess, "mcp.json")
	_ = os.WriteFile(mcp, []byte(`{"mcpServers":{"barnraise":{"command":"barnraise","args":["mcp"],"env":{"A":"1"}},"web":{"type":"http","url":"http://127.0.0.1:9/mcp"},"old":{"type":"sse","url":"http://127.0.0.1:9/sse"}}}`), 0o600)
	p := fakeProvider(fakecli.Script{Fixture: "testdata/session_tools.jsonl", Record: rec})
	spec := ports.SessionSpec{
		Model: "gemini-2.5-pro", Effort: "high", Workdir: dir, Prompt: "count", SystemPrompt: "be brief", MCPConfig: mcp, MaxTurns: 7,
		Account: domain.Account{ID: "gemini-1", ConfigDir: "/cfg/gemini-1"},
		Env:     map[string]string{"BARNRAISE_ITEM": "it-1"},
	}
	settings, _, err := p.Settings(spec)
	if err != nil {
		t.Fatal(err)
	}
	s, err := p.Open(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	for range s.Events() {
	}
	res, err := s.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "212" || res.Usage.Account != "gemini-1" || res.Usage.Provider != "gemini" {
		t.Fatalf("result = %+v", res)
	}
	call, err := fakecli.ReadCall(rec)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gemini", "--output-format", "stream-json", "--approval-mode", "default", "--extensions", "none",
		"--model", "gemini-2.5-pro", "--allowed-mcp-server-names", "barnraise,old,web"}
	if !slices.Equal(call.Args, want) {
		t.Fatalf("args = %q\nwant   %q", call.Args, want)
	}
	if slices.Contains(call.Args, "yolo") || slices.Contains(call.Args, "--yolo") {
		t.Fatal("yolo on the machine itself")
	}
	if call.Stdin != "<system>\nbe brief\n</system>\n\ncount" {
		t.Fatalf("stdin = %q", call.Stdin)
	}
	settingsFile := filepath.Join(sess, "gemini-settings.json")
	for _, e := range []string{"GEMINI_CLI_HOME=/cfg/gemini-1", "BARNRAISE_ITEM=it-1", "GEMINI_CLI_SYSTEM_SETTINGS_PATH=" + settingsFile} {
		if !slices.Contains(call.Env, e) {
			t.Fatalf("env %s missing: %v", e, call.Env)
		}
	}
	if _, err := os.Stat(settingsFile); !os.IsNotExist(err) {
		t.Fatal("settings file not removed after the session")
	}
	var doc struct {
		MCPServers map[string]map[string]any  `json:"mcpServers"`
		Tools      struct{ Exclude []string } `json:"tools"`
		Model      struct {
			MaxSessionTurns int `json:"maxSessionTurns"`
		} `json:"model"`
	}
	if err := json.Unmarshal(settings, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.MCPServers["barnraise"]["command"] != "barnraise" || doc.MCPServers["barnraise"]["trust"] != true ||
		doc.MCPServers["web"]["httpUrl"] != "http://127.0.0.1:9/mcp" || doc.MCPServers["old"]["url"] != "http://127.0.0.1:9/sse" {
		t.Fatalf("mcp = %+v", doc.MCPServers)
	}
	if !slices.Equal(doc.Tools.Exclude, HostExcludedTools) || doc.Model.MaxSessionTurns != 7 {
		t.Fatalf("settings = %s", settings)
	}
}

func TestOpenWithoutMCPUsesTempSettings(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "call.json")
	p := fakeProvider(fakecli.Script{Fixture: "testdata/session_basic.jsonl", Record: rec})
	s, err := p.Open(context.Background(), ports.SessionSpec{Workdir: t.TempDir(), Prompt: "ping", ResumeID: "s-9"})
	if err != nil {
		t.Fatal(err)
	}
	for range s.Events() {
	}
	if _, err := s.Wait(); err != nil {
		t.Fatal(err)
	}
	call, err := fakecli.ReadCall(rec)
	if err != nil {
		t.Fatal(err)
	}
	if call.Stdin != "ping" || slices.Contains(call.Args, "--allowed-mcp-server-names") || !slices.Contains(call.Args, "--resume") {
		t.Fatalf("call = %+v", call)
	}
	for _, e := range call.Env {
		if v, ok := strings.CutPrefix(e, "GEMINI_CLI_SYSTEM_SETTINGS_PATH="); ok {
			if _, err := os.Stat(filepath.Dir(v)); !os.IsNotExist(err) {
				t.Fatal("temporary settings folder not removed")
			}
			return
		}
	}
	t.Fatal("no settings path")
}

func TestOpenErrors(t *testing.T) {
	p := fakeProvider(fakecli.Script{})
	if _, err := p.Open(context.Background(), ports.SessionSpec{}); err == nil {
		t.Fatal("want workdir error")
	}
	if _, err := p.Open(context.Background(), ports.SessionSpec{Workdir: t.TempDir(), MCPConfig: "/nonexistent.json"}); err == nil {
		t.Fatal("want mcp error")
	}
	if _, err := p.Open(context.Background(), ports.SessionSpec{Workdir: t.TempDir(), MCPConfig: "{not json"}); err == nil {
		t.Fatal("want mcp parse error")
	}
}

func TestOpenLimited(t *testing.T) {
	p := fakeProvider(fakecli.Script{Fixture: "testdata/session_limit.jsonl", Exit: 1})
	s, err := p.Open(context.Background(), ports.SessionSpec{Workdir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for range s.Events() {
	}
	res, err := s.Wait()
	if err != nil || !res.Limited || res.ExitCode != 1 {
		t.Fatalf("res = %+v err = %v", res, err)
	}
}

func TestStderrLimit(t *testing.T) {
	p := fakeProvider(fakecli.Script{Stderr: "Loaded cached credentials.\nError: Quota exceeded for quota metric. Try again in 2 hours.\n", Exit: 1})
	s, err := p.Open(context.Background(), ports.SessionSpec{Workdir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var lim []ports.ProviderEvent
	for e := range s.Events() {
		if e.Kind == ports.PELimit {
			lim = append(lim, e)
		}
	}
	res, err := s.Wait()
	if err != nil || !res.Limited || len(lim) != 1 {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if d := time.Until(lim[0].ResetsAt); d < 119*time.Minute || d > 121*time.Minute {
		t.Fatalf("resets in %s", d)
	}
}

func TestStop(t *testing.T) {
	p := fakeProvider(fakecli.Script{Fixture: "testdata/session_basic.jsonl", Sleep: 30 * time.Second})
	s, err := p.Open(context.Background(), ports.SessionSpec{Workdir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	<-s.Events()
	_ = s.Stop()
	if _, err := s.Wait(); !errors.Is(err, provcommon.ErrStopped) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunAsUser(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "call.json")
	p := fakeProvider(fakecli.Script{Fixture: "testdata/session_basic.jsonl", Record: rec})
	s, err := p.Open(context.Background(), ports.SessionSpec{Workdir: t.TempDir(), RunAsUser: "barnraise-gemini-1"})
	if err != nil {
		t.Fatal(err)
	}
	for range s.Events() {
	}
	_, _ = s.Wait()
	call, err := fakecli.ReadCall(rec)
	if err != nil {
		t.Fatal(err)
	}
	if call.Args[0] != "sudo" || !slices.Contains(call.Args, "barnraise-gemini-1") || !slices.Contains(call.Args, "gemini") {
		t.Fatalf("args = %q", call.Args)
	}
}

// TestContract runs against the installed CLI with BARNRAISE_CONTRACT=1.
// Set BARNRAISE_GEMINI_MODEL to pick the model (default gemini-2.5-flash).
func TestContract(t *testing.T) {
	if os.Getenv("BARNRAISE_CONTRACT") != "1" {
		t.Skip("set BARNRAISE_CONTRACT=1 to run against the installed gemini CLI")
	}
	model := os.Getenv("BARNRAISE_GEMINI_MODEL")
	if model == "" {
		model = "gemini-2.5-flash"
	}
	p := New(Options{})
	caps, err := p.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !caps.Headless || !caps.Stream {
		t.Fatalf("caps = %+v", caps)
	}
	t.Logf("capabilities: %+v", caps)
	s, err := p.Open(context.Background(), ports.SessionSpec{
		Workdir: t.TempDir(), Model: model, Timeout: 3 * time.Minute,
		Prompt: "Reply with the single word: pong",
	})
	if err != nil {
		t.Fatal(err)
	}
	var evs []ports.ProviderEvent
	for e := range s.Events() {
		evs = append(evs, e)
	}
	res, err := s.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if len(find(evs, ports.PEInit)) != 1 || len(find(evs, ports.PEDone)) != 1 {
		t.Fatalf("kinds = %v", kinds(evs))
	}
	if !res.Limited && !strings.Contains(strings.ToLower(res.Text), "pong") {
		t.Fatalf("result = %+v", res)
	}
}
