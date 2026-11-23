package claude

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
	p := NewParser(domain.TokenUsage{Agent: "a1", Account: "claude-1", Role: "dev"})
	var evs []ports.ProviderEvent
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
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
	want := []ports.ProviderEventKind{
		ports.PEMessage, ports.PEMessage, // hook_started, hook_response
		ports.PEInit,
		ports.PEMessage, // "Pong"
		ports.PEUsage,   // rate_limit_event (allowed_warning)
		ports.PEUsage, ports.PEDone,
	}
	if got := kinds(evs); !slices.Equal(got, want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	const sid = "2afaf751-fd2a-4bd7-b526-b49e63d93623"
	if evs[2].SessionID != sid || evs[2].Text != "claude-haiku-5-5" {
		t.Fatalf("init = %+v", evs[2])
	}
	if evs[3].Text != "Pong" {
		t.Fatalf("message = %q", evs[3].Text)
	}
	// Raw is the original line.
	for i, idx := range []int{0, 1, 2, 3, 4, 5} {
		if string(evs[i].Raw) != lines[idx] {
			t.Fatalf("event %d raw differs from line %d", i, idx)
		}
	}
	if ri, ok := RateLimit(evs[4]); !ok || ri.Status != "allowed_warning" || ri.Windows["five_hour"].Utilization != 0.49 {
		t.Fatalf("rate limit info = %+v %v", ri, ok)
	}
	done := evs[len(evs)-1]
	if done.Text != "Pong" || done.IsError || done.Usage == nil {
		t.Fatalf("done = %+v", done)
	}
	u := *done.Usage
	if u.Input != 2 || u.Output != 5 || u.CacheRead != 11842 || u.CacheWrite != 20132 ||
		u.CostUSD != 0.00414752 || u.Model != "claude-haiku-5-5" || u.Account != "claude-1" ||
		u.Agent != "a1" || u.Role != "dev" || u.Provider != "claude" || u.Duration != 1228*time.Millisecond {
		t.Fatalf("usage = %+v", u)
	}
	r := p.Result()
	if r.SessionID != sid || r.Text != "Pong" || r.Limited || r.Usage.Total() != 2+5+11842+20132 {
		t.Fatalf("result = %+v", r)
	}
}

func TestGoldenToolUse(t *testing.T) {
	p, evs, _ := replay(t, "testdata/session_tool.jsonl")
	uses := find(evs, ports.PEToolUse)
	if len(uses) != 1 || uses[0].Tool != "Bash" {
		t.Fatalf("tool uses = %+v", uses)
	}
	var in struct{ Command string }
	if err := json.Unmarshal(uses[0].Input, &in); err != nil || in.Command != "echo hi" {
		t.Fatalf("input = %s", uses[0].Input)
	}
	res := find(evs, ports.PEToolResult)
	if len(res) != 1 || res[0].Tool != "Bash" || res[0].Text != "hi" || res[0].IsError {
		t.Fatalf("tool results = %+v", res)
	}
	if r := p.Result(); r.Text != "hi" || r.Usage.Output == 0 {
		t.Fatalf("result = %+v", r)
	}
	if len(find(evs, ports.PEDone)) != 1 {
		t.Fatal("want exactly one done")
	}
}

func TestGoldenDenied(t *testing.T) {
	_, evs, _ := replay(t, "testdata/session_denied.jsonl")
	res := find(evs, ports.PEToolResult)
	if len(res) != 1 || !res[0].IsError || res[0].Tool != "Write" {
		t.Fatalf("tool results = %+v", res)
	}
	perm := find(evs, ports.PEPermission)
	if len(perm) != 1 || perm[0].Tool != "Write" || !perm[0].IsError || !strings.Contains(string(perm[0].Input), "notes.txt") {
		t.Fatalf("permission = %+v", perm)
	}
}

func TestGoldenLimit(t *testing.T) {
	p, evs, _ := replay(t, "testdata/session_limit.jsonl")
	lim := find(evs, ports.PELimit)
	if len(lim) != 2 {
		t.Fatalf("limits = %+v", lim)
	}
	if !lim[0].ResetsAt.Equal(time.Unix(1795447800, 0)) {
		t.Fatalf("resets at = %v", lim[0].ResetsAt)
	}
	if !strings.Contains(lim[1].Text, "hit your limit") || lim[1].ResetsAt.IsZero() {
		t.Fatalf("second limit = %+v", lim[1])
	}
	if lim[1].ResetsAt.Hour() != 15 || lim[1].ResetsAt.Location().String() != "America/Sao_Paulo" {
		t.Fatalf("parsed reset = %v", lim[1].ResetsAt)
	}
	done := find(evs, ports.PEDone)
	if len(done) != 1 || !done[0].IsError {
		t.Fatalf("done = %+v", done)
	}
	if r := p.Result(); !r.Limited || r.ResetsAt.IsZero() {
		t.Fatalf("result = %+v", r)
	}
}

func TestParserNeverCrashes(t *testing.T) {
	p := NewParser(domain.TokenUsage{})
	for _, l := range []string{
		"not json", "{", "[]", "null", `{"type":"assistant","message":"x"}`,
		`{"type":"user","message":{"content":"plain"}}`, `{"type":"result"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use"}]}}`,
		`{"type":"something_new","x":1}`,
	} {
		evs := p.Parse([]byte(l))
		for _, e := range evs {
			if len(e.Raw) == 0 {
				t.Fatalf("%q: event without raw", l)
			}
		}
	}
	if evs := p.Parse([]byte(`{"type":"something_new"}`)); len(evs) != 1 || evs[0].Kind != ports.PEMessage {
		t.Fatalf("unknown line: %+v", evs)
	}
	if evs := p.Parse([]byte("garbage")); len(evs) != 0 {
		t.Fatalf("garbage: %+v", evs)
	}
}

func TestFinishWithoutResult(t *testing.T) {
	p := NewParser(domain.TokenUsage{})
	p.Parse([]byte(`{"type":"system","subtype":"init","session_id":"s1","model":"m"}`))
	evs := p.Finish(1, "Error: Claude AI usage limit reached|1795447800\n")
	if k := kinds(evs); !slices.Equal(k, []ports.ProviderEventKind{ports.PELimit, ports.PEDone}) {
		t.Fatalf("kinds = %v", k)
	}
	r := p.Result()
	if !r.Limited || !r.ResetsAt.Equal(time.Unix(1795447800, 0)) || r.SessionID != "s1" {
		t.Fatalf("result = %+v", r)
	}
}

func fakeProvider(s fakecli.Script) *Provider {
	return New(Options{Command: fakecli.Command(s), Grace: 300 * time.Millisecond})
}

func TestDetect(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "call.json")
	p := fakeProvider(fakecli.Script{
		Record: rec,
		Outputs: map[string]string{
			"--version":     "testdata/version.txt",
			"--help":        "testdata/help.txt",
			"agents --help": "testdata/agents_help.txt",
		},
	})
	caps, err := p.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := ports.Capabilities{
		Version: "2.1.330", Headless: true, Stream: true, Resume: true, Fork: true,
		Hooks: true, HookBlock: true, Subagents: true, Skills: true, MCP: true, Sandbox: true,
		SessionState: true, UsageLimits: true, ModelSwitch: true, JSONSchema: true, Images: true,
	}
	if caps != want {
		t.Fatalf("caps = %+v\nwant %+v", caps, want)
	}
	if !p.Flags()["--effort"] {
		t.Fatal("--effort not seen")
	}
	_ = os.Remove(rec)
	if _, err := p.Detect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rec); !os.IsNotExist(err) {
		t.Fatal("second Detect ran the CLI again; want cached")
	}
}

func TestDetectOldCLI(t *testing.T) {
	dir := t.TempDir()
	ver := filepath.Join(dir, "v")
	help := filepath.Join(dir, "h")
	_ = os.WriteFile(ver, []byte("1.0.10 (Claude Code)\n"), 0o600)
	_ = os.WriteFile(help, []byte("Options:\n  -p, --print  x\n  --output-format <f> \"text\", \"json\"\n  -r, --resume\n"), 0o600)
	p := fakeProvider(fakecli.Script{Outputs: map[string]string{"--version": ver, "--help": help}})
	caps, err := p.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !caps.Headless || caps.Stream || !caps.Resume || caps.Fork || caps.Hooks || caps.MCP || caps.SessionState || caps.JSONSchema {
		t.Fatalf("caps = %+v", caps)
	}
	// Flags missing from the help are left out of the command line.
	args := p.Args(ports.SessionSpec{Effort: "high", JSONSchema: "{}"})
	if slices.Contains(args, "--effort") || slices.Contains(args, "--json-schema") {
		t.Fatalf("args = %v", args)
	}
}

func TestDetectFails(t *testing.T) {
	p := New(Options{Binary: "/nonexistent/claude"})
	if _, err := p.Detect(context.Background()); err == nil {
		t.Fatal("want error")
	}
}

func collect(s ports.Session) []ports.ProviderEvent {
	var evs []ports.ProviderEvent
	for e := range s.Events() {
		evs = append(evs, e)
	}
	return evs
}

func TestOpenReplaysAndBuildsCommand(t *testing.T) {
	dir := t.TempDir()
	rec := filepath.Join(dir, "call.json")
	t.Setenv("BARNRAISE_SECRET_SHOULD_NOT_LEAK", "x")
	p := fakeProvider(fakecli.Script{Fixture: "testdata/session_tool.jsonl", Record: rec})
	spec := ports.SessionSpec{
		Agent: "a1", Role: "dev", Model: "haiku", Effort: "low", Workdir: dir,
		Account:      domain.Account{ID: "claude-2", ConfigDir: "/cfg/claude-2"},
		Prompt:       "do the thing\nwith two lines",
		SystemPrompt: "be brief", MCPConfig: "/m.json", SettingsFile: "/s.json",
		ResumeID: "r-1", MaxTurns: 3, JSONSchema: `{"type":"object"}`,
		Env: map[string]string{"BARNRAISE_ITEM": "it-1"},
	}
	s, err := p.Open(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(s)
	res, err := s.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "hi" || res.ExitCode != 0 || res.Duration <= 0 || res.Usage.Account != "claude-2" {
		t.Fatalf("result = %+v", res)
	}
	if len(find(evs, ports.PEToolUse)) != 1 || evs[len(evs)-1].Kind != ports.PEDone {
		t.Fatalf("events = %v", kinds(evs))
	}

	call, err := fakecli.ReadCall(rec)
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"claude", "-p", "--output-format", "stream-json", "--verbose",
		"--model", "haiku", "--effort", "low", "--append-system-prompt", "be brief",
		"--mcp-config", "/m.json", "--strict-mcp-config", "--settings", "/s.json", "--setting-sources", "project,local", "--resume", "r-1",
		"--max-turns", "3", "--json-schema", `{"type":"object"}`}
	if !slices.Equal(call.Args, wantArgs) {
		t.Fatalf("args = %q", call.Args)
	}
	if call.Stdin != spec.Prompt {
		t.Fatalf("stdin = %q", call.Stdin)
	}
	if real, _ := filepath.EvalSymlinks(dir); call.Dir != dir && call.Dir != real {
		t.Fatalf("dir = %q", call.Dir)
	}
	for _, kv := range []string{"CLAUDE_CONFIG_DIR=/cfg/claude-2", "DISABLE_AUTOUPDATER=1", "BARNRAISE_ITEM=it-1"} {
		if !slices.Contains(call.Env, kv) {
			t.Fatalf("env misses %s: %v", kv, call.Env)
		}
	}
	for _, kv := range call.Env {
		if strings.HasPrefix(kv, "BARNRAISE_SECRET_SHOULD_NOT_LEAK=") {
			t.Fatal("daemon env leaked into the session")
		}
	}
}

func TestOpenLimitedExit(t *testing.T) {
	p := fakeProvider(fakecli.Script{Fixture: "testdata/session_limit.jsonl", Exit: 1})
	s, err := p.Open(context.Background(), ports.SessionSpec{Workdir: t.TempDir(), Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(s)
	res, err := s.Wait()
	if err != nil {
		t.Fatalf("a limited run is a result, not an error: %v", err)
	}
	if !res.Limited || res.ExitCode != 1 || res.ResetsAt.IsZero() || len(find(evs, ports.PELimit)) == 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestOpenFailureReportsStderr(t *testing.T) {
	p := fakeProvider(fakecli.Script{Stderr: "boom: not logged in\n", Exit: 2})
	s, err := p.Open(context.Background(), ports.SessionSpec{Workdir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(s)
	_, err = s.Wait()
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("err = %v", err)
	}
	if len(evs) != 1 || evs[0].Kind != ports.PEDone || !evs[0].IsError {
		t.Fatalf("events = %+v", evs)
	}
}

func TestStopGracefulAndForced(t *testing.T) {
	for _, ignore := range []bool{false, true} {
		p := fakeProvider(fakecli.Script{Fixture: "testdata/session_basic.jsonl", Sleep: 30 * time.Second, IgnoreInt: ignore})
		s, err := p.Open(context.Background(), ports.SessionSpec{Workdir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		// Wait for the first event so the process is up and has set its signal handling.
		<-s.Events()
		start := time.Now()
		_ = s.Stop()
		_ = s.Stop() // idempotent
		_, err = s.Wait()
		if !errors.Is(err, provcommon.ErrStopped) {
			t.Fatalf("ignore=%v: err = %v", ignore, err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("ignore=%v: stop took %s", ignore, d)
		}
		for range s.Events() {
		}
	}
}

func TestTimeoutAndCancel(t *testing.T) {
	p := fakeProvider(fakecli.Script{Sleep: 30 * time.Second})
	s, err := p.Open(context.Background(), ports.SessionSpec{Workdir: t.TempDir(), Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Wait(); !errors.Is(err, provcommon.ErrTimeout) {
		t.Fatalf("err = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s, err = p.Open(ctx, ports.SessionSpec{Workdir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := s.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunAsUserWrapsWithSudo(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "call.json")
	p := fakeProvider(fakecli.Script{Record: rec})
	s, err := p.Open(context.Background(), ports.SessionSpec{
		Workdir: t.TempDir(), RunAsUser: "agent1", Model: "haiku",
		Account: domain.Account{ConfigDir: "/cfg"}, Env: map[string]string{"X_ONE": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Wait()
	call, err := fakecli.ReadCall(rec)
	if err != nil {
		t.Fatal(err)
	}
	if call.Args[0] != "sudo" || !slices.Equal(call.Args[1:4], []string{"-n", "-u", "agent1"}) {
		t.Fatalf("args = %q", call.Args)
	}
	pe := call.Args[4]
	if !strings.HasPrefix(pe, "--preserve-env=") || !strings.Contains(pe, "CLAUDE_CONFIG_DIR") ||
		!strings.Contains(pe, "X_ONE") || strings.Contains(pe, "HOME") {
		t.Fatalf("preserve = %q", pe)
	}
	if call.Args[5] != "--" || call.Args[6] != "claude" || call.Args[7] != "-p" {
		t.Fatalf("args = %q", call.Args)
	}
}

func TestOpenNeedsWorkdir(t *testing.T) {
	if _, err := New(Options{}).Open(context.Background(), ports.SessionSpec{}); err == nil {
		t.Fatal("want error")
	}
}

// TestContract runs against the installed CLI. It spends a few tokens of the
// logged-in subscription, so it only runs with BARNRAISE_CONTRACT=1.
func TestContract(t *testing.T) {
	if os.Getenv("BARNRAISE_CONTRACT") != "1" {
		t.Skip("set BARNRAISE_CONTRACT=1 to run against the installed claude CLI")
	}
	p := New(Options{})
	ctx := context.Background()
	caps, err := p.Detect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !caps.Headless || !caps.Stream {
		t.Fatalf("caps = %+v", caps)
	}
	t.Logf("capabilities: %+v", caps)
	s, err := p.Open(ctx, ports.SessionSpec{
		Workdir: t.TempDir(), Model: "haiku", MaxTurns: 1, Timeout: 2 * time.Minute,
		Prompt: "Reply with the single word: pong",
	})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(s)
	res, err := s.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if len(find(evs, ports.PEInit)) != 1 || len(find(evs, ports.PEDone)) != 1 || res.SessionID == "" {
		t.Fatalf("events = %v result = %+v", kinds(evs), res)
	}
	if !res.Limited && (!strings.Contains(strings.ToLower(res.Text), "pong") || res.Usage.Output == 0) {
		t.Fatalf("result = %+v", res)
	}
}
