package claudechat

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/ports"
)

const sample = `{"type":"permission-mode","permissionMode":"dontAsk"}
{"type":"user","isMeta":true,"message":{"role":"user","content":"<local-command-caveat>hidden</local-command-caveat>"}}
{"type":"user","timestamp":"2026-11-26T17:50:00Z","message":{"role":"user","content":"What is stuck?"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"…"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"barnraise status -p bastion"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"I-54 blocked"}]}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"/x/plan.md"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","is_error":true,"content":"no such file"}]}}
{"type":"assistant","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"subagent noise"}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"  I-54 waits for the owner.  "}]}}
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]}}
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Focus on it"}]}}
not json
`

func TestParseTranscriptKeepsTheConversation(t *testing.T) {
	got := ParseTranscript(strings.NewReader(sample))
	var shape []string
	for _, e := range got {
		shape = append(shape, e.Role+":"+e.Text+e.Tool)
	}
	want := []string{"user:What is stuck?", "tool:Bash", "tool:Read", "assistant:I-54 waits for the owner.", "user:Focus on it"}
	if !slices.Equal(shape, want) {
		t.Fatalf("got %q", shape)
	}
	if got[1].Input != "barnraise status -p bastion" || got[1].Result != "I-54 blocked" || got[1].Failed {
		t.Fatalf("bash entry: %+v", got[1])
	}
	if got[2].Input != "/x/plan.md" || !got[2].Failed || got[2].Result != "no such file" {
		t.Fatalf("read entry: %+v", got[2])
	}
	for i, e := range got {
		if e.Seq != i {
			t.Fatalf("seq %d at %d", e.Seq, i)
		}
	}
	if got[0].At.IsZero() {
		t.Fatal("the time of a message is kept")
	}
}

func TestTranscriptReadsFromTheSessionsFile(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	s := New()
	dir := "/home/u/.local/share/barnraise/chat/workspace"
	if got, err := s.Transcript("", "abc", dir, 0); err != nil || len(got) != 0 {
		t.Fatalf("no file yet: %v %v", got, err)
	}
	path := TranscriptPath("", dir, "abc")
	if !strings.HasSuffix(path, "-home-u--local-share-barnraise-chat-workspace/abc.jsonl") {
		t.Fatalf("path %s", path)
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, []byte(sample), 0o600)
	all, _ := s.Transcript("", "abc", dir, 0)
	rest, _ := s.Transcript("", "abc", dir, 3)
	if len(all) != 5 || len(rest) != 2 || rest[0].Seq != 3 {
		t.Fatalf("all %d rest %+v", len(all), rest)
	}
	if past, _ := s.Transcript("", "abc", dir, 99); len(past) != 0 {
		t.Fatal("after the end: nothing")
	}
}

func TestArgsStartOrResume(t *testing.T) {
	spec := ports.ChatSpec{SessionID: "id", Name: "Barnraise · m1", SettingsFile: "/s.json", SystemPrompt: "be brief"}
	a := strings.Join(args(spec), " ")
	for _, want := range []string{"--session-id id", "--remote-control Barnraise · m1", "--permission-mode dontAsk", "--settings /s.json", "--append-system-prompt be brief"} {
		if !strings.Contains(a, want) {
			t.Fatalf("%q lacks %q", a, want)
		}
	}
	spec.Resume = true
	if a := strings.Join(args(spec), " "); !strings.Contains(a, "--resume id") || strings.Contains(a, "--session-id") {
		t.Fatalf("resume: %q", a)
	}
}

func TestEnvironDropsTheParentSessionsMarkers(t *testing.T) {
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "1")
	t.Setenv("CLAUDECODE", "1")
	env := strings.Join(environ([]string{"X=1"}), "\n")
	if strings.Contains(env, "CLAUDE_CODE_CHILD_SESSION") || strings.Contains(env, "CLAUDECODE=") {
		t.Fatal("a child marker would turn the transcript off")
	}
	if !strings.Contains(env, "TERM=xterm-256color") || !strings.HasSuffix(env, "X=1") {
		t.Fatal(env)
	}
}

// A stand-in claude: prints the trust prompt, then the Remote Control line,
// and echoes what it is sent.
func TestSessionRunsInATerminal(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
printf 'Quick safety check  Yes, I trust this folder\n'
read -r answer
printf '/remote-control is active · Continue here, on your phone, or at https://claude.ai/code/session_01ABCdef\n'
while IFS= read -r line; do printf 'esc to interrupt got:%s\n' "$line"; done
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s := &Session{Bin: bin}
	if _, err := s.Start(ports.ChatSpec{SessionID: "id", Name: "n", Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	if _, err := s.Start(ports.ChatSpec{}); err != nil {
		t.Fatal("a second start does nothing")
	}
	if err := s.Send("hello"); err != nil {
		t.Fatal(err)
	}
	st := s.Status()
	if !st.Running || st.RemoteURL != "https://claude.ai/code/session_01ABCdef" {
		t.Fatalf("status %+v", st)
	}
	if err := s.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(); err != nil || s.Status().Running {
		t.Fatalf("stop: %v", err)
	}
	if err := s.Send("x"); err != ErrNotRunning {
		t.Fatalf("send after stop: %v", err)
	}
	if err := s.Interrupt(); err != ErrNotRunning {
		t.Fatalf("interrupt after stop: %v", err)
	}
}

func TestStartWithoutClaude(t *testing.T) {
	s := &Session{Bin: "/no/such/claude"}
	if _, err := s.Start(ports.ChatSpec{}); err == nil {
		t.Fatal("no claude: an error")
	}
	if err := s.Send(" "); err == nil {
		t.Fatal("empty message: an error")
	}
}

// Resuming a session that never wrote a transcript starts it under its id;
// one that wrote it resumes.
func TestResumeOnlyWhatWasSaved(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	dir := t.TempDir()
	bin := filepath.Join(t.TempDir(), "claude")
	log := filepath.Join(t.TempDir(), "args")
	_ = os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\" > "+log+"\nexit 3\n"), 0o755)
	run := func() string {
		s := &Session{Bin: bin}
		if _, err := s.Start(ports.ChatSpec{SessionID: "abc", Resume: true, Name: "n", Dir: dir}); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 200 && s.Status().Running; i++ {
			time.Sleep(10 * time.Millisecond)
		}
		if st := s.Status(); st.Running || !strings.Contains(st.Err, "exit status 3") {
			t.Fatalf("status %+v", st)
		}
		if err := s.Send("x"); err != ErrNotRunning {
			t.Fatalf("send: %v", err)
		}
		b, _ := os.ReadFile(log)
		return string(b)
	}
	if a := run(); !strings.Contains(a, "--session-id abc") {
		t.Fatalf("no transcript: %q", a)
	}
	path := TranscriptPath("", dir, "abc")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, []byte(sample), 0o600)
	if a := run(); !strings.Contains(a, "--resume abc") {
		t.Fatalf("with transcript: %q", a)
	}
}
