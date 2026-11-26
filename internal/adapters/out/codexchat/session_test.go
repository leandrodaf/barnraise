package codexchat

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// fakeServer is a Codex app-server on a Unix socket: it keeps one thread's
// items and answers the calls the conversation makes.
type fakeServer struct {
	mu      sync.Mutex
	calls   []string
	params  map[string]json.RawMessage
	items   []json.RawMessage
	resumeE bool
}

func (f *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ctx := context.Background()
	send := func(v any) { b, _ := json.Marshal(v); _ = ws.Write(ctx, websocket.MessageText, b) }
	send(map[string]any{"method": "remoteControl/status/changed", "params": map[string]any{"status": "connected", "serverName": "m1", "installationId": "i"}})
	for {
		_, b, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var m struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(b, &m)
		f.mu.Lock()
		f.calls = append(f.calls, m.Method)
		f.params[m.Method] = m.Params
		resumeE := f.resumeE
		f.mu.Unlock()
		if m.ID == nil {
			continue
		}
		reply := func(res any) { send(map[string]any{"id": *m.ID, "result": res}) }
		switch m.Method {
		case "initialize", "thread/name/set", "turn/interrupt", "turn/steer":
			reply(map[string]any{})
		case "thread/start":
			reply(map[string]any{"thread": map[string]any{"id": "th-new"}})
		case "thread/resume":
			if resumeE {
				send(map[string]any{"id": *m.ID, "error": map[string]any{"message": "no such thread"}})
				continue
			}
			var p struct {
				ThreadID string `json:"threadId"`
			}
			_ = json.Unmarshal(m.Params, &p)
			reply(map[string]any{"thread": map[string]any{"id": p.ThreadID}})
		case "turn/start":
			reply(map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress"}})
			// A server request the conversation declines (an approval).
			send(map[string]any{"id": 99, "method": "item/commandExecution/requestApproval", "params": map[string]any{}})
		case "thread/items/list":
			f.mu.Lock()
			items := f.items
			f.mu.Unlock()
			reply(map[string]any{"data": items, "nextCursor": nil})
		default:
			send(map[string]any{"id": *m.ID, "error": map[string]any{"message": "unknown " + m.Method}})
		}
	}
}

func (f *fakeServer) sawCall(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == name {
			return true
		}
	}
	return false
}

// serve starts the fake server under a CODEX_HOME and a stand-in codex
// command whose remote-control start does nothing and pair prints a code.
func serve(t *testing.T) (*fakeServer, string, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := SocketPath(dir)
	_ = os.MkdirAll(filepath.Dir(sock), 0o700)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeServer{params: map[string]json.RawMessage{}}
	srv := &http.Server{Handler: http.HandlerFunc(f.handle)}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	bin := filepath.Join(dir, "codex")
	_ = os.WriteFile(bin, []byte(`#!/bin/sh
case "$2" in
  pair) echo '{"pairingCode":"123456","manualPairingCode":"AB12-CD34","environmentId":"e","expiresAt":1900000000}';;
  *) echo '{"stage":"pid_publish","outcome":"succeeded"}';;
esac
`), 0o755)
	return f, dir, bin
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 300 && !cond(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !cond() {
		t.Fatal("timed out")
	}
}

func TestCodexConversationRunsAThread(t *testing.T) {
	f, home, bin := serve(t)
	s := &Session{Bin: bin}
	spec := ports.ChatSpec{ConfigDir: home, Dir: home, Name: "Barnraise · m1", SystemPrompt: "be brief", Model: "gpt-x"}
	id, err := s.Start(spec)
	if err != nil || id != "th-new" {
		t.Fatalf("start %q %v", id, err)
	}
	if again, _ := s.Start(spec); again != "th-new" {
		t.Fatal("a second start keeps the thread")
	}
	waitFor(t, func() bool { return s.Status().Remote == "connected" })
	st := s.Status()
	if !st.Running || st.Provider != "codex" || st.Busy {
		t.Fatalf("status %+v", st)
	}
	var start map[string]any
	_ = json.Unmarshal(f.params["thread/start"], &start)
	if start["sandbox"] != "workspace-write" || start["approvalPolicy"] != "never" || start["developerInstructions"] != "be brief" || start["model"] != "gpt-x" {
		t.Fatalf("thread/start %v", start)
	}
	if !f.sawCall("thread/name/set") {
		t.Fatal("the thread is named for the phone")
	}
	if err := s.Send("  "); err == nil {
		t.Fatal("empty message")
	}
	if err := s.Send("what is stuck?"); err != nil {
		t.Fatal(err)
	}
	if !s.Status().Busy {
		t.Fatal("a turn runs")
	}
	if err := s.Send("and why?"); err != nil || !f.sawCall("turn/steer") {
		t.Fatalf("a message during a turn steers it: %v", err)
	}
	if err := s.Interrupt(); err != nil || !f.sawCall("turn/interrupt") {
		t.Fatalf("interrupt: %v", err)
	}
	// The answer streams before it lands in the thread's items.
	s.notify("item/agentMessage/delta", json.RawMessage(`{"threadId":"th-new","itemId":"m1","delta":"I-54 "}`))
	s.notify("item/agentMessage/delta", json.RawMessage(`{"threadId":"th-new","itemId":"m1","delta":"waits"}`))
	if d := s.Status().Draft; d != "I-54 waits" {
		t.Fatalf("draft %q", d)
	}
	s.notify("item/agentMessage/delta", json.RawMessage(`{"threadId":"th-new","itemId":"m2","delta":"New"}`))
	if d := s.Status().Draft; d != "New" {
		t.Fatalf("a new message starts the draft over: %q", d)
	}
	s.notify("item/completed", json.RawMessage(`{"threadId":"th-new","item":{"type":"agentMessage"}}`))
	if d := s.Status().Draft; d != "" {
		t.Fatalf("landed: %q", d)
	}
	s.notify("item/agentMessage/delta", json.RawMessage(`{"threadId":"th-new","itemId":"m3","delta":"x"}`))
	s.notify("turn/completed", json.RawMessage(`{"threadId":"th-new","turn":{"id":"turn-1"}}`))
	if s.Status().Draft != "" {
		t.Fatal("the turn ended: no draft")
	}
	s.notify("turn/started", json.RawMessage(`{"threadId":"other","turn":{"id":"x"}}`))
	if s.Status().Busy {
		t.Fatal("the turn ended; another thread's turn is not ours")
	}
	if err := s.Interrupt(); err != nil {
		t.Fatal("no turn: nothing to stop")
	}

	f.mu.Lock()
	f.items = []json.RawMessage{
		json.RawMessage(`{"turnId":"t","startedAtMs":1795728813489,"item":{"type":"userMessage","id":"1","content":[{"type":"text","text":"what is stuck?"}]}}`),
		json.RawMessage(`{"turnId":"t","item":{"type":"reasoning","id":"2"}}`),
		json.RawMessage(`{"turnId":"t","item":{"type":"commandExecution","id":"3","command":"barnraise status","aggregatedOutput":"I-54 blocked\n","exitCode":0,"status":"completed"}}`),
		json.RawMessage(`{"turnId":"t","item":{"type":"commandExecution","id":"4","command":"false","aggregatedOutput":"","exitCode":1,"status":"failed"}}`),
		json.RawMessage(`{"turnId":"t","item":{"type":"mcpToolCall","id":"5","server":"gh","tool":"pr","arguments":{"n":1},"result":{"ok":true}}}`),
		json.RawMessage(`{"turnId":"t","item":{"type":"mcpToolCall","id":"6","server":"gh","tool":"pr","arguments":{},"error":{"message":"down"}}}`),
		json.RawMessage(`{"turnId":"t","item":{"type":"webSearch","id":"7","query":"codex"}}`),
		json.RawMessage(`{"turnId":"t","item":{"type":"fileChange","id":"8","changes":[{"path":"a"}],"status":"declined"}}`),
		json.RawMessage(`{"turnId":"t","item":{"type":"imageView","id":"9","path":"/x.png"}}`),
		json.RawMessage(`{"turnId":"t","item":{"type":"agentMessage","id":"10","text":" I-54 waits. "}}`),
	}
	f.mu.Unlock()
	es, err := s.Transcript(home, "th-new", home, 0)
	if err != nil || len(es) != 9 {
		t.Fatalf("entries %d %v", len(es), err)
	}
	if es[0].Role != "user" || es[0].At.IsZero() || es[1].Tool != "Bash" || es[1].Result != "I-54 blocked" || es[1].Failed ||
		!es[2].Failed || es[3].Tool != "mcp__gh__pr" || es[3].Result != `{"ok":true}` || !es[4].Failed ||
		es[5].Tool != "WebSearch" || !es[6].Failed || es[8].Text != "I-54 waits." {
		t.Fatalf("entries %+v", es)
	}
	if rest, _ := s.Transcript(home, "th-new", home, 8); len(rest) != 1 || rest[0].Seq != 8 {
		t.Fatalf("after 8: %+v", rest)
	}

	// Stopped: the transcript comes on a short connection; the thread resumes.
	_ = s.Stop()
	if s.Status().Running {
		t.Fatal("stopped")
	}
	if err := s.Send("x"); err != ErrNotRunning {
		t.Fatalf("send stopped: %v", err)
	}
	if err := s.Interrupt(); err != ErrNotRunning {
		t.Fatalf("interrupt stopped: %v", err)
	}
	if es, _ := s.Transcript(home, "th-new", home, 0); len(es) != 9 {
		t.Fatalf("offline: %d", len(es))
	}
	spec.SessionID, spec.Resume = "th-new", true
	if id, err := s.Start(spec); err != nil || id != "th-new" {
		t.Fatalf("resume %q %v", id, err)
	}
	_ = s.Stop()
	f.mu.Lock()
	f.resumeE = true
	f.mu.Unlock()
	if id, err := s.Start(spec); err != nil || id != "th-new" {
		t.Fatalf("a lost thread starts anew: %q %v", id, err)
	}
	_ = s.Stop()

	p, err := s.Pair(home)
	if err != nil || p.Manual != "AB12-CD34" || p.Code != "123456" || p.Expires.Year() < 2030 {
		t.Fatalf("pair %+v %v", p, err)
	}
}

func TestCodexWithoutADaemon(t *testing.T) {
	s := &Session{Bin: "/no/codex"}
	if _, err := s.Start(ports.ChatSpec{}); err == nil {
		t.Fatal("no codex")
	}
	dir := t.TempDir()
	if es, err := s.Transcript(dir, "th", dir, 0); err != nil || len(es) != 0 {
		t.Fatal("no daemon: an empty conversation")
	}
	if es, _ := s.Transcript(dir, "", dir, 0); len(es) != 0 {
		t.Fatal("no thread yet")
	}
	bin := filepath.Join(dir, "codex")
	_ = os.WriteFile(bin, []byte("#!/bin/sh\necho 'not logged in' >&2\nexit 2\n"), 0o755)
	s.Bin = bin
	if _, err := s.Start(ports.ChatSpec{ConfigDir: dir}); err == nil || !strings.Contains(err.Error(), "remote-control start") {
		t.Fatalf("got %v", err)
	}
	if _, err := s.Pair(dir); err == nil {
		t.Fatal("pair fails with the daemon")
	}
}
