package codex

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
	p := NewParser(domain.TokenUsage{Account: "codex-1", Model: "gpt-5.6-luna"})
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

func TestGoldenBasic(t *testing.T) {
	p, evs, lines := replay(t, "testdata/session_basic.jsonl")
	want := []ports.ProviderEventKind{ports.PEInit, ports.PEMessage, ports.PEMessage, ports.PEUsage, ports.PEDone}
	if got := kinds(evs); !slices.Equal(got, want) {
		t.Fatalf("kinds = %v", got)
	}
	const tid = "01a20848-ee6c-70a3-b221-23edf8214773"
	if evs[0].SessionID != tid || evs[2].Text != "pong" || string(evs[2].Raw) != lines[2] {
		t.Fatalf("events = %+v", evs[:3])
	}
	u := evs[3].Usage
	if u == nil || u.Input != 17722 || u.Output != 5 || u.CacheRead != 0 || u.Provider != "codex" || u.Model != "gpt-5.6-luna" {
		t.Fatalf("usage = %+v", u)
	}
	r := p.Result()
	if r.SessionID != tid || r.Text != "pong" || r.Limited || evs[4].IsError {
		t.Fatalf("result = %+v", r)
	}
}

func TestGoldenFileChange(t *testing.T) {
	_, evs, _ := replay(t, "testdata/session_file_change.jsonl")
	uses := find(evs, ports.PEToolUse)
	res := find(evs, ports.PEToolResult)
	if len(uses) != 2 || len(res) != 2 || uses[0].Tool != "file_change" || !res[0].IsError {
		t.Fatalf("uses = %+v res = %+v", uses, res)
	}
	if !strings.Contains(string(uses[0].Input), "hello.txt") {
		t.Fatalf("input = %s", uses[0].Input)
	}
}

func TestGoldenTools(t *testing.T) {
	p, evs, _ := replay(t, "testdata/session_tools.jsonl")
	uses := find(evs, ports.PEToolUse)
	res := find(evs, ports.PEToolResult)
	if len(uses) != 3 || len(res) != 3 {
		t.Fatalf("uses = %v res = %v", uses, res)
	}
	var in struct{ Command string }
	_ = json.Unmarshal(uses[0].Input, &in)
	if uses[0].Tool != "command_execution" || !strings.Contains(in.Command, "wc -l") {
		t.Fatalf("use 0 = %+v", uses[0])
	}
	if res[0].Text != "212\n" || res[0].IsError || !res[1].IsError {
		t.Fatalf("results = %+v", res)
	}
	if uses[2].Tool != "docs.search" || string(uses[2].Input) != `{"query":"sandbox"}` || res[2].Text != "3 pages found" {
		t.Fatalf("mcp = %+v %+v", uses[2], res[2])
	}
	r := p.Result()
	if r.Text != "212" || r.Usage.Input != 53376-41216 || r.Usage.CacheRead != 41216 || r.Usage.Output != 279 {
		t.Fatalf("result = %+v", r)
	}
}

func TestGoldenLimit(t *testing.T) {
	p, evs, _ := replay(t, "testdata/session_limit.jsonl")
	lim := find(evs, ports.PELimit)
	if len(lim) != 1 {
		t.Fatalf("limits = %v", kinds(evs))
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
	if r := p.Result(); r.Limited || !strings.Contains(r.Text, "stream disconnected") {
		t.Fatalf("result = %+v", r)
	}
}

func TestParserNeverCrashes(t *testing.T) {
	p := NewParser(domain.TokenUsage{})
	for _, l := range []string{"x", "{}", "[]", `{"type":"item.completed"}`, `{"type":"item.completed","item":{"type":"mcp_tool_call"}}`,
		`{"type":"turn.failed"}`, `{"type":"error","error":"plain"}`, `{"type":"turn.completed"}`, `{"type":"new.kind"}`} {
		for _, e := range p.Parse([]byte(l)) {
			if len(e.Raw) == 0 {
				t.Fatalf("%q: no raw", l)
			}
		}
	}
}

func fakeProvider(s fakecli.Script) *Provider {
	return New(Options{Command: fakecli.Command(s), Grace: 300 * time.Millisecond})
}

func TestDetect(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "call.json")
	p := fakeProvider(fakecli.Script{Record: rec, Outputs: map[string]string{
		"--version":     "testdata/version.txt",
		"--help":        "testdata/help.txt",
		"exec --help":   "testdata/exec_help.txt",
		"features list": "testdata/features.txt",
	}})
	caps, err := p.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := ports.Capabilities{
		Version: "0.162.1", Headless: true, Stream: true, Resume: true, Fork: true, Hooks: true,
		Subagents: true, Skills: true, MCP: true, Sandbox: true, Messaging: true, JSONSchema: true, Images: true,
	}
	if caps != want {
		t.Fatalf("caps = %+v\nwant %+v", caps, want)
	}
	if !p.Features()["hooks"] {
		t.Fatal("features not kept")
	}
	_ = os.Remove(rec)
	if _, err := p.Detect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rec); !os.IsNotExist(err) {
		t.Fatal("Detect not cached")
	}
}

func TestOpenBuildsCommand(t *testing.T) {
	dir := t.TempDir()
	rec := filepath.Join(dir, "call.json")
	mcp := filepath.Join(dir, "mcp.json")
	_ = os.WriteFile(mcp, []byte(`{"mcpServers":{"barnraise":{"command":"barnraise","args":["mcp","--project","p 1"],"env":{"B":"2","A":"1"}},"web":{"type":"http","url":"http://127.0.0.1:9/mcp"}}}`), 0o600)
	p := fakeProvider(fakecli.Script{Fixture: "testdata/session_tools.jsonl", Record: rec})
	spec := ports.SessionSpec{
		Model: "gpt-5.6-luna", Effort: "low", Workdir: dir, Prompt: "count\n\"quoted\"",
		SystemPrompt: "say \"hi\"\nbriefly", MCPConfig: mcp, JSONSchema: `{"type":"object"}`,
		Account: domain.Account{ID: "codex-1", ConfigDir: "/cfg/codex-1"},
		Env:     map[string]string{"BARNRAISE_ITEM": "it-1"},
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
	if res.Text != "212" || res.Usage.Account != "codex-1" {
		t.Fatalf("result = %+v", res)
	}
	call, err := fakecli.ReadCall(rec)
	if err != nil {
		t.Fatal(err)
	}
	a := call.Args
	schemaIdx := slices.Index(a, "--output-schema")
	if schemaIdx < 0 {
		t.Fatalf("no schema: %q", a)
	}
	if _, err := os.Stat(a[schemaIdx+1]); !os.IsNotExist(err) {
		t.Fatal("schema temp file not removed after the session")
	}
	want := []string{"codex", "exec", "--json", "--sandbox", "workspace-write", "--cd", dir,
		"--skip-git-repo-check", "--model", "gpt-5.6-luna",
		"-c", `model_reasoning_effort="low"`,
		"-c", `developer_instructions="say \"hi\"\nbriefly"`,
		"-c", `mcp_servers.barnraise.command="barnraise"`,
		"-c", `mcp_servers.barnraise.args=["mcp","--project","p 1"]`,
		"-c", `mcp_servers.barnraise.env={"A"="1","B"="2"}`,
		"-c", `mcp_servers.web.url="http://127.0.0.1:9/mcp"`,
		"--output-schema", a[schemaIdx+1], "-"}
	if !slices.Equal(a, want) {
		t.Fatalf("args = %q\nwant   %q", a, want)
	}
	for _, bad := range []string{"--dangerously-bypass-approvals-and-sandbox", "danger-full-access"} {
		if slices.Contains(a, bad) {
			t.Fatalf("args contain %s", bad)
		}
	}
	if call.Stdin != spec.Prompt {
		t.Fatalf("stdin = %q", call.Stdin)
	}
	if !slices.Contains(call.Env, "CODEX_HOME=/cfg/codex-1") || !slices.Contains(call.Env, "BARNRAISE_ITEM=it-1") {
		t.Fatalf("env = %v", call.Env)
	}
}

func TestResumeArgs(t *testing.T) {
	p := New(Options{})
	args, err := p.Args(ports.SessionSpec{ResumeID: "th-1", Workdir: "/w", JSONSchema: "/s.json"}, "/s.json")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"exec", "resume", "--json", "-c", `sandbox_mode="workspace-write"`, "--skip-git-repo-check", "--output-schema", "/s.json", "th-1", "-"}
	if !slices.Equal(args, want) {
		t.Fatalf("args = %q", args)
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
	p := fakeProvider(fakecli.Script{Stderr: "2026-11-23T12:00:00Z ERROR rmcp: noise\nERROR: You've hit your usage limit. Try again in 2 hours.\n", Exit: 1})
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

func TestBadMCPConfig(t *testing.T) {
	p := fakeProvider(fakecli.Script{})
	if _, err := p.Open(context.Background(), ports.SessionSpec{Workdir: t.TempDir(), MCPConfig: "/nonexistent.json"}); err == nil {
		t.Fatal("want error")
	}
}

// TestContract runs against the installed CLI with BARNRAISE_CONTRACT=1.
// Set BARNRAISE_CODEX_MODEL to pick the model (default gpt-5.6-luna).
func TestContract(t *testing.T) {
	if os.Getenv("BARNRAISE_CONTRACT") != "1" {
		t.Skip("set BARNRAISE_CONTRACT=1 to run against the installed codex CLI")
	}
	model := os.Getenv("BARNRAISE_CODEX_MODEL")
	if model == "" {
		model = "gpt-5.6-luna"
	}
	p := New(Options{})
	caps, err := p.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !caps.Headless || !caps.Stream || !caps.Sandbox {
		t.Fatalf("caps = %+v", caps)
	}
	t.Logf("capabilities: %+v", caps)
	s, err := p.Open(context.Background(), ports.SessionSpec{
		Workdir: t.TempDir(), Model: model, Effort: "low", Timeout: 3 * time.Minute,
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
