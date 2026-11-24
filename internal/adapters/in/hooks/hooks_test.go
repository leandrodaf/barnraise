package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func policy(t *testing.T) (*domain.Policy, string) {
	p := domain.DefaultPolicy()
	p.Home = "/home/agent"
	p.Worktree = "/w/I-1"
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.json")
	data, _ := json.Marshal(p)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	lp, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	return lp, path
}

func in(tool string, input map[string]any) Input {
	data, _ := json.Marshal(input)
	return Input{HookEventName: "PreToolUse", ToolName: tool, ToolInput: data}
}

func TestPreToolDenies(t *testing.T) {
	p, _ := policy(t)
	res := PreTool(in("Write", map[string]any{"file_path": "/w/I-1/.github/workflows/ci.yml", "content": "x"}), p, nil, "barnraise")
	if res.Decision.Allow {
		t.Fatal("must deny")
	}
	var buf bytes.Buffer
	_ = Write(&buf, res.Output)
	if !strings.Contains(buf.String(), `"permissionDecision":"deny"`) {
		t.Fatal(buf.String())
	}
	res = PreTool(in("Bash", map[string]any{"command": "git push origin main"}), p, nil, "barnraise")
	if res.Decision.Allow {
		t.Fatal("push to main")
	}
	res = PreTool(in("apply_patch", map[string]any{"input": "*** Begin Patch\n*** Update File: .barnraise/constitution.md\n"}), p, nil, "")
	if res.Decision.Allow {
		t.Fatal("codex patch into .barnraise")
	}
	res = PreTool(in("Read", map[string]any{"file_path": "/home/agent/.codex/auth.json"}), p, nil, "")
	if res.Decision.Allow {
		t.Fatal("credentials")
	}
}

func TestPreToolAllowsAndWraps(t *testing.T) {
	p, _ := policy(t)
	res := PreTool(in("Edit", map[string]any{"file_path": "/w/I-1/app/Main.kt"}), p, nil, "barnraise")
	if !res.Decision.Allow || res.Output != nil {
		t.Fatalf("%+v", res)
	}
	res = PreTool(in("Bash", map[string]any{"command": "./gradlew testDebugUnitTest", "description": "tests"}), p, nil, "/usr/bin/barnraise")
	if !res.Decision.Allow || res.Output == nil {
		t.Fatal("wrap noisy command")
	}
	data, _ := json.Marshal(res.Output)
	if !strings.Contains(string(data), "/usr/bin/barnraise filter -- ./gradlew testDebugUnitTest") || !strings.Contains(string(data), `"description":"tests"`) {
		t.Fatal(string(data))
	}
	res = PreTool(in("Bash", map[string]any{"command": "go test ./... | tail"}), p, nil, "barnraise")
	if res.Output != nil {
		t.Fatal("pipelines are left alone")
	}
}

func TestWrappedCommands(t *testing.T) {
	old := lookRTK
	defer func() { lookRTK = old }()
	lookRTK = func() string { return "" }
	for cmd, want := range map[string]bool{
		"git status":                       true,
		"git diff main":                    true,
		"rg -n TODO":                       true,
		"golangci-lint run ./...":          true,
		"CGO_ENABLED=0 go build ./cmd/...": true,
		"kubectl describe pod web":         true,
		"npm ci":                           true,
		"echo go test":                     false,
		"git commit -m wip":                false,
		"curl -s https://example.com":      false,
		"barnraise filter -- go test":      false,
		"git status > out.txt":             false,
	} {
		if got := wrapped(cmd); got != want {
			t.Errorf("wrapped(%q) = %v", cmd, got)
		}
	}
	// With rtk installed, auto also sends commands without a native filter.
	lookRTK = func() string { return "/usr/bin/rtk" }
	if !wrapped("curl -s https://example.com") || wrapped("echo hi") {
		t.Error("rtk delegation")
	}
	t.Setenv(domain.EnvOutfilterDelegate, domain.DelegateOff)
	if wrapped("curl -s https://example.com") || !wrapped("git status") {
		t.Error("off keeps the native filters only")
	}
}

func TestReadInputAndContext(t *testing.T) {
	i, err := ReadInput(strings.NewReader(`{"hook_event_name":"SessionStart","source":"compact"}`))
	if err != nil || i.Source != "compact" {
		t.Fatal(i, err)
	}
	if _, err := ReadInput(strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	if AdditionalContext("SessionStart", "  ") != nil {
		t.Fatal("empty")
	}
	if BlockStop("x")["decision"] != "block" {
		t.Fatal("block")
	}
	if _, err := LoadPolicy(""); err == nil {
		t.Fatal("no path")
	}
}

func TestStatusLimits(t *testing.T) {
	var s StatusInput
	_ = json.Unmarshal([]byte(`{"model":{"id":"opus"},"rate_limits":{"five_hour":{"used_percentage":32.5,"resets_at":"2026-11-23T18:00:00Z"},"seven_day":{"utilization":0.61}}}`), &s)
	f, w, _, fr, _, ok := s.Limits()
	if !ok || f != 32.5 || w != 61 || fr != "2026-11-23T18:00:00Z" {
		t.Fatal(f, w, fr, ok)
	}
	var none StatusInput
	if _, _, _, _, _, ok := none.Limits(); ok {
		t.Fatal("no limits")
	}
}

func BenchmarkPreTool(b *testing.B) {
	p := domain.DefaultPolicy()
	p.Home, p.Worktree = "/home/agent", "/w/I-1"
	input := in("Bash", map[string]any{"command": "cat app/src/main/Main.kt && echo done > out.txt"})
	for b.Loop() {
		PreTool(input, &p, nil, "barnraise")
	}
}
