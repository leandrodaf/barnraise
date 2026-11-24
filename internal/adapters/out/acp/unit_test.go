package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/provcommon"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// peer is the other end of a Conn, driven by the test.
type peer struct {
	toConn *io.PipeWriter
	lines  *bufio.Scanner
}

func pair(t *testing.T, onReq RequestHandler, onNote NotificationHandler) (*Conn, *peer) {
	t.Helper()
	inR, inW := io.Pipe()   // peer -> conn
	outR, outW := io.Pipe() // conn -> peer
	c := NewConn(outW, onReq, onNote, nil)
	go c.Serve(inR)
	t.Cleanup(func() { _ = inW.Close(); _ = outR.Close() })
	return c, &peer{toConn: inW, lines: bufio.NewScanner(outR)}
}

func (p *peer) send(t *testing.T, s string) {
	t.Helper()
	if _, err := io.WriteString(p.toConn, s+"\n"); err != nil {
		t.Fatal(err)
	}
}

func (p *peer) read(t *testing.T) map[string]any {
	t.Helper()
	if !p.lines.Scan() {
		t.Fatal("no message")
	}
	var m map[string]any
	if err := json.Unmarshal(p.lines.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestConnCallsAndAnswers(t *testing.T) {
	notes := make(chan string, 1)
	c, p := pair(t, func(_ context.Context, method string, params json.RawMessage) (any, *RPCError) {
		switch method {
		case "echo":
			return json.RawMessage(params), nil
		case "boom":
			panic("bug")
		}
		return nil, &RPCError{Code: codeMethodNotFound, Message: "nope"}
	}, func(method string, _ json.RawMessage) { notes <- method })

	done := make(chan error, 1)
	var out struct{ V int }
	go func() { done <- c.Call(context.Background(), "ask", map[string]int{"v": 1}, &out) }()
	req := p.read(t)
	if req["method"] != "ask" || req["jsonrpc"] != "2.0" {
		t.Fatalf("request %v", req)
	}
	p.send(t, `{"jsonrpc":"2.0","id":`+jsonNum(req["id"])+`,"result":{"V":5}}`)
	if err := <-done; err != nil || out.V != 5 {
		t.Fatalf("call: %v %+v", err, out)
	}
	go func() { done <- c.Call(context.Background(), "fail", nil, nil) }()
	req = p.read(t)
	p.send(t, `{"jsonrpc":"2.0","id":`+jsonNum(req["id"])+`,"error":{"code":-1,"message":"no"}}`)
	var rerr *RPCError
	if err := <-done; !errors.As(err, &rerr) || rerr.Code != -1 || !strings.Contains(rerr.Error(), "no (code -1)") {
		t.Fatalf("error: %v", err)
	}

	p.send(t, `{"jsonrpc":"2.0","id":"a","method":"echo","params":{"x":1}}`)
	if m := p.read(t); m["id"] != "a" || m["result"].(map[string]any)["x"] != 1.0 {
		t.Fatalf("echo: %v", m)
	}
	p.send(t, `{"jsonrpc":"2.0","id":2,"method":"boom"}`)
	if m := p.read(t); !strings.Contains(m["error"].(map[string]any)["message"].(string), "panic") {
		t.Fatalf("panic: %v", m)
	}
	p.send(t, `not json`)
	if m := p.read(t); m["error"].(map[string]any)["code"] != float64(codeParseError) {
		t.Fatalf("parse: %v", m)
	}
	p.send(t, `{"jsonrpc":"2.0"}`)
	if m := p.read(t); m["error"].(map[string]any)["code"] != float64(codeInvalidRequest) {
		t.Fatalf("invalid: %v", m)
	}
	p.send(t, `{"jsonrpc":"2.0","method":"note"}`)
	if <-notes != "note" {
		t.Fatal("notification")
	}
	if err := c.Notify("hello", map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if m := p.read(t); m["method"] != "hello" || m["id"] != nil {
		t.Fatalf("notify: %v", m)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- c.Call(ctx, "slow", nil, nil) }()
	p.read(t)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	go func() { done <- c.Call(context.Background(), "late", nil, nil) }()
	p.read(t)
	_ = p.toConn.Close()
	if err := <-done; !errors.Is(err, ErrClosed) {
		t.Fatalf("closed: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := c.Call(context.Background(), "after", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("after close: %v", err)
	}
}

func jsonNum(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestIDKey(t *testing.T) {
	if idKey(json.RawMessage(`7`)) == idKey(json.RawMessage(`"7"`)) {
		t.Fatal("a number and a string are different ids")
	}
	if idKey(json.RawMessage(`{"a":1}`)) != "r:{\"a\":1}" {
		t.Fatal("odd id")
	}
}

func TestReadLineDropsHugeLines(t *testing.T) {
	big := strings.Repeat("x", maxLine+10)
	br := bufio.NewReaderSize(strings.NewReader(big+"\n{}\n"), 64<<10)
	line, err := readLine(br)
	if err != nil || line != nil {
		t.Fatalf("huge line: %d %v", len(line), err)
	}
	if line, _ := readLine(br); string(line) != "{}" {
		t.Fatalf("next line %q", line)
	}
}

func TestSliceLinesAndLimitBuffer(t *testing.T) {
	s := "a\nb\nc\n"
	for _, c := range []struct {
		line, limit int
		want        string
	}{{0, 0, s}, {2, 0, "b\nc\n"}, {2, 1, "b\n"}, {9, 1, ""}, {1, 2, "a\nb\n"}} {
		if got := sliceLines(s, c.line, c.limit); got != c.want {
			t.Errorf("sliceLines(%d,%d) = %q", c.line, c.limit, got)
		}
	}
	b := &limitBuffer{limit: 4}
	_, _ = b.Write([]byte("ab"))
	_, _ = b.Write([]byte("cé!"))
	out, trunc := b.snapshot()
	if !trunc || out != "cé!" {
		t.Fatalf("buffer %q %v", out, trunc)
	}
	_, _ = b.Write([]byte("é"))
	if out, _ := b.snapshot(); out != "!é" {
		t.Fatalf("cut inside a character: %q", out)
	}
	if shellJoin("echo", []string{"a b", "", "it's", "plain"}) != `echo 'a b' '' 'it'\''s' plain` {
		t.Fatal(shellJoin("echo", []string{"a b", "", "it's", "plain"}))
	}
}

func TestV1Details(t *testing.T) {
	d := v1{}
	s, err := d.ParseNewSession(json.RawMessage(`{"sessionId":"s","configOptions":[{"id":"mode","category":"mode"},{"id":"mdl","category":"model","options":[{"group":"g","options":[{"value":"a"},{"value":"b"}]},{"value":"c"}]}]}`))
	if err != nil || s.ModelOption != "mdl" || !slices.Equal(s.ModelOptionValues, []string{"a", "b", "c"}) {
		t.Fatalf("%+v %v", s, err)
	}
	if m, p, ok := d.SelectModel(s, "b"); !ok || m != "session/set_config_option" || p.(map[string]string)["configId"] != "mdl" {
		t.Fatalf("select: %v %v", m, p)
	}
	if _, _, ok := d.SelectModel(s, "zzz"); ok {
		t.Fatal("an unknown model was selected")
	}
	if u := d.ParseUpdate("session/update", json.RawMessage(`{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"image","data":"x"}}}`)); u != nil {
		t.Fatalf("image chunk: %+v", u)
	}
	if u := d.ParseUpdate("other", nil); u != nil {
		t.Fatal("other method")
	}
	if u := d.ParseUpdate("session/update", json.RawMessage(`{"sessionId":"s","update":{"sessionUpdate":"usage_update","used":1,"size":2,"cost":{"amount":3,"currency":"EUR"}}}`)); len(u) != 1 || u[0].HasCost {
		t.Fatalf("non-USD cost: %+v", u)
	}
	servers := v1Servers([]mcpServer{{Name: "h", URL: "https://x", Headers: map[string]string{"A": "1"}}, {Name: "s", Command: "c"}}, agentInfo{MCPHTTP: true})
	b, _ := json.Marshal(servers)
	if string(b) != `[{"type":"http","name":"h","url":"https://x","headers":[{"name":"A","value":"1"}]},{"name":"s","command":"c","args":[],"env":[]}]` {
		t.Fatal(string(b))
	}
	if _, err := dialectFor(1); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := d.Serve(context.Background(), nil, "nope", nil); ok {
		t.Fatal("unknown method handled")
	}
	if _, rerr, _ := d.Serve(context.Background(), nil, "fs/read_text_file", json.RawMessage(`[`)); rerr == nil || rerr.Code != codeInvalidParams {
		t.Fatal("bad params")
	}
}

func testClient(t *testing.T, worktree string) (*client, *[]ports.ProviderEvent) {
	t.Helper()
	pol := domain.DefaultPolicy()
	pol.Home = t.TempDir()
	pol.Worktree = worktree
	var evs []ports.ProviderEvent
	c := newClient(provcommon.DefaultCommand, pol, func(rel string) bool { return rel == "x_test.go" }, ports.SessionSpec{Workdir: worktree}, false, provcommon.MinimalEnv(), func(e ports.ProviderEvent) { evs = append(evs, e) })
	return c, &evs
}

func TestClientRules(t *testing.T) {
	dir := t.TempDir()
	c, evs := testClient(t, dir)
	ctx := context.Background()
	// Locked existing tests cannot change; new ones can.
	if rerr := c.writeFile(ctx, "x_test.go", "x"); rerr == nil || rerr.Code != codePolicy {
		t.Fatalf("locked: %v", rerr)
	}
	if rerr := c.writeFile(ctx, "y_test.go", "y"); rerr != nil {
		t.Fatalf("new test: %v", rerr)
	}
	if rerr := c.writeFile(ctx, "/etc/passwd", "y"); rerr == nil {
		t.Fatal("outside the worktree")
	}
	if _, rerr := c.readFile(ctx, "", 0, 0); rerr == nil {
		t.Fatal("empty path")
	}
	if rerr := c.writeFile(ctx, "", ""); rerr == nil {
		t.Fatal("empty path")
	}
	if got, rerr := c.readFile(ctx, "y_test.go", 0, 0); rerr != nil || got != "y" {
		t.Fatalf("read: %q %v", got, rerr)
	}
	if _, rerr := c.readFile(ctx, "missing", 0, 0); rerr == nil || rerr.Code != codeInternal {
		t.Fatal("missing file")
	}
	if len(*evs) != 2 {
		t.Fatalf("violations: %+v", *evs)
	}
	// Permissions: no option of the right kind is a cancel.
	if _, ok := c.permission(ctx, toolCall{ID: "1", Kind: "read", Paths: []string{"a.go"}}, []permOption{{ID: "r", Kind: "reject_once"}}); ok {
		t.Fatal("allowed without an allow option")
	}
	if id, ok := c.permission(ctx, toolCall{ID: "2", Kind: "fetch", RawInput: json.RawMessage(`{"url":"https://x"}`)}, []permOption{{ID: "a", Kind: "allow_always"}}); !ok || id != "a" {
		t.Fatal("fetch")
	}
	// The command comes from the raw input, as a list with args.
	tc := c.call(toolCall{Kind: "execute", RawInput: json.RawMessage(`{"command":["git","push"],"args":["origin","main"]}`)})
	if tc.Command != "git push origin main" {
		t.Fatalf("command %q", tc.Command)
	}
	if tc := c.call(toolCall{Kind: "execute", Title: "`ls -la`"}); tc.Command != "ls -la" {
		t.Fatalf("title command %q", tc.Command)
	}
	if tc := c.call(toolCall{Kind: "move", RawInput: json.RawMessage(`{"source":"a","destination":"b"}`)}); !tc.Write || len(tc.Paths) != 2 {
		t.Fatalf("move %+v", tc)
	}
	if tc := c.call(toolCall{Kind: "custom", RawInput: json.RawMessage(`{"cmd":"make"}`)}); tc.Command != "make" || tc.Tool != "custom" {
		t.Fatalf("other %+v", tc)
	}
	for kind, want := range map[string]string{"search": "Grep", "delete": "Delete", "move": "Move", "think": "Think", "": "tool"} {
		if got := toolName(toolCall{Kind: kind}); got != want {
			t.Errorf("toolName(%q) = %s", kind, got)
		}
	}
}

func TestClientContainerSeesOnlyMounts(t *testing.T) {
	dir := t.TempDir()
	c, _ := testClient(t, dir)
	c.container = &ports.ContainerSpec{Mounts: []string{dir, "/opt/ro:ro"}}
	if _, rerr := c.readFile(context.Background(), "/usr/share/dict/words", 0, 0); rerr == nil || !strings.Contains(rerr.Message, "not mounted") {
		t.Fatalf("outside the mounts: %v", rerr)
	}
	if !c.visible("/opt/ro/x") || c.visible("/opt/rox") {
		t.Fatal("mount prefix")
	}
	if _, rerr := c.createTerminal(context.Background(), "ls", nil, nil, "/usr", 0); rerr == nil {
		t.Fatal("a terminal outside the mounts")
	}
}

// sudoless runs `sudo -n -u user [--preserve-env=…] -- cmd…` as cmd itself,
// to test the paths of an isolated user without sudo.
func sudoless(ctx context.Context, name string, args ...string) *exec.Cmd {
	if name == "sudo" {
		for i, a := range args {
			if a == "--" {
				return exec.Command(args[i+1], args[i+2:]...)
			}
		}
	}
	return provcommon.DefaultCommand(ctx, name, args...)
}

func TestClientAsIsolatedUser(t *testing.T) {
	dir := t.TempDir()
	c, _ := testClient(t, dir)
	c.runAs, c.command = "barnraise-x", sudoless
	ctx := context.Background()
	if rerr := c.writeFile(ctx, filepath.Join(dir, "sub", "a.txt"), "hi\n"); rerr != nil {
		t.Fatal(rerr)
	}
	if got, rerr := c.readFile(ctx, filepath.Join(dir, "sub", "a.txt"), 0, 0); rerr != nil || got != "hi\n" {
		t.Fatalf("read %q %v", got, rerr)
	}
	if _, rerr := c.readFile(ctx, filepath.Join(dir, "none"), 0, 0); rerr == nil {
		t.Fatal("missing file as user")
	}
	id, rerr := c.createTerminal(ctx, "pwd", nil, nil, "", 0)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if st, _ := c.waitTerminal(ctx, id); st.Code == nil || *st.Code != 0 {
		t.Fatalf("status %+v", st)
	}
	if out, _, _, _ := c.terminalOutput(id); strings.TrimSpace(out) != dir {
		t.Fatalf("pwd %q", out)
	}
}

func TestTerminalLifecycle(t *testing.T) {
	dir := t.TempDir()
	c, _ := testClient(t, dir)
	ctx := context.Background()
	if _, rerr := c.createTerminal(ctx, " ", nil, nil, "", 0); rerr == nil {
		t.Fatal("empty command")
	}
	if _, rerr := c.createTerminal(ctx, filepath.Join(dir, "nope"), nil, nil, "", 0); rerr == nil {
		t.Fatal("missing program")
	}
	id, rerr := c.createTerminal(ctx, "sh", []string{"-c", "printf 0123456789; sleep 30"}, map[string]string{"BARNRAISE_TOKEN": "stolen", "FOO": "bar"}, "sub/..", 4)
	if rerr != nil {
		t.Fatal(rerr)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, trunc, st, _ := c.terminalOutput(id)
		if out == "6789" && trunc && st == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("output %q %v %v", out, trunc, st)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if rerr := c.killTerminal(id); rerr != nil {
		t.Fatal(rerr)
	}
	st, rerr := c.waitTerminal(ctx, id)
	if rerr != nil || st.Signal == nil || st.Code != nil {
		t.Fatalf("killed: %+v %v", st, rerr)
	}
	if _, _, st, _ := c.terminalOutput(id); st == nil {
		t.Fatal("no exit status after the end")
	}
	cctx, cancel := context.WithCancel(ctx)
	id2, _ := c.createTerminal(ctx, "sleep", []string{"30"}, nil, "", 0)
	cancel()
	if _, rerr := c.waitTerminal(cctx, id2); rerr == nil {
		t.Fatal("wait outlived its context")
	}
	for _, f := range []func(string) *RPCError{c.killTerminal, c.releaseTerminal} {
		if f("nope") == nil {
			t.Fatal("unknown terminal")
		}
	}
	if _, rerr := c.waitTerminal(ctx, "nope"); rerr == nil {
		t.Fatal("unknown terminal")
	}
	c.closeTerminals()
	if len(c.terms.m) != 0 {
		t.Fatal("terminals left")
	}
	for i := 0; i < maxTerminals; i++ {
		c.terms.m[string(rune('a'+i))] = &terminal{}
	}
	if _, rerr := c.createTerminal(ctx, "true", nil, nil, "", 0); rerr == nil || !strings.Contains(rerr.Message, "too many") {
		t.Fatalf("limit: %v", rerr)
	}
}

func TestSandboxArgs(t *testing.T) {
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	_ = os.WriteFile(filepath.Join(home, ".netrc"), nil, 0o600)
	repo := t.TempDir()
	wt := filepath.Join(t.TempDir(), "wt")
	_ = os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755)
	_ = os.MkdirAll(wt, 0o755)
	_ = os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644)
	sock := filepath.Join(t.TempDir(), "s.sock")
	_ = os.WriteFile(sock, nil, 0o600)
	pol := domain.DefaultPolicy()
	pol.Home = home
	name, args := sandboxArgs(pol, wt, sock, wt, "go", []string{"test", "./..."})
	line := name + " " + strings.Join(args, " ")
	for _, want := range []string{
		"--ro-bind / /", "--tmpfs /tmp", "--tmpfs " + filepath.Join(home, ".ssh"), "--ro-bind /dev/null " + filepath.Join(home, ".netrc"),
		"--bind " + wt + " " + wt, "--bind " + filepath.Join(repo, ".git") + " " + filepath.Join(repo, ".git"),
		"--bind " + sock + " " + sock, "--chdir " + wt + " -- go test ./...",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %q in %s", want, line)
		}
	}
	if strings.Index(line, "--tmpfs "+filepath.Join(home, ".ssh")) > strings.Index(line, "--bind "+wt) {
		t.Error("the worktree must be bound after the hidden folders")
	}
	if gitCommonDir(t.TempDir()) != "" {
		t.Error("no .git file")
	}
	_ = os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: ../elsewhere\n"), 0o644)
	if g := gitCommonDir(wt); g != filepath.Join(filepath.Dir(wt), "elsewhere") {
		t.Errorf("relative gitdir %s", g)
	}
	pol.Home = ""
	for _, p := range hiddenPaths(pol) {
		if strings.Contains(p, "~") || strings.ContainsAny(p, "*") {
			t.Errorf("hidden %s", p)
		}
	}
}

func TestSandboxedTerminalOnThisMachine(t *testing.T) {
	if testing.Short() || !probeSandbox() {
		t.Skip("bubblewrap cannot start here")
	}
	dir := t.TempDir()
	c, _ := testClient(t, dir)
	c.sandbox = true
	id, rerr := c.createTerminal(context.Background(), "sh", []string{"-c", "echo ok > inside.txt; touch " + filepath.Join(c.pol.Home, "outside") + " && echo escaped"}, nil, "", 0)
	if rerr != nil {
		t.Fatal(rerr)
	}
	st, _ := c.waitTerminal(context.Background(), id)
	out, _, _, _ := c.terminalOutput(id)
	if strings.Contains(out, "escaped") || st.Code == nil {
		t.Fatalf("the sandbox let a write out: %q %+v", out, st)
	}
	if _, err := os.Stat(filepath.Join(dir, "inside.txt")); err != nil {
		t.Fatal("the worktree is not writable in the sandbox")
	}
}

func TestLoadPolicyAndMCP(t *testing.T) {
	dir := t.TempDir()
	pol, err := loadPolicy(ports.SessionSpec{Workdir: dir})
	if err != nil || pol.Worktree != dir || len(pol.DenyWrite) == 0 {
		t.Fatalf("default policy %+v %v", pol.Worktree, err)
	}
	if _, err := loadPolicy(ports.SessionSpec{Env: map[string]string{"BARNRAISE_POLICY": filepath.Join(dir, "none")}}); err == nil {
		t.Fatal("missing policy file")
	}
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte("{"), 0o600)
	if _, err := loadPolicy(ports.SessionSpec{Env: map[string]string{"BARNRAISE_POLICY": bad}}); err == nil {
		t.Fatal("bad policy file")
	}
	if _, err := mcpServers(bad); err == nil {
		t.Fatal("bad mcp file")
	}
	if _, err := mcpServers(filepath.Join(dir, "none")); err == nil {
		t.Fatal("missing mcp file")
	}
	if s, err := mcpServers(""); err != nil || s != nil {
		t.Fatal("no mcp file")
	}
	p := New(Options{})
	exists := p.existsOnMain(dir, "")
	if exists("x") {
		t.Fatal("not a repository")
	}
	if safeName("My Agent!") != "my_agent_" {
		t.Fatal(safeName("My Agent!"))
	}
}

// Without bubblewrap, a terminal runs inside the session's Landlock
// confinement when Barnraise confines the session; a microVM wrapper is not
// applied per command.
func TestTerminalUsesLandlockWithoutBubblewrap(t *testing.T) {
	dir := t.TempDir()
	c, _ := testClient(t, dir)
	var wrapped []string
	c.confine = &ports.Confine{Backend: "landlock", Wrap: func(name string, args []string) (string, []string, error) {
		wrapped = append([]string{name}, args...)
		return "sh", []string{"-c", "echo confined"}, nil
	}}
	ctx := context.Background()
	id, rerr := c.createTerminal(ctx, "echo", []string{"plain"}, nil, "", 0)
	if rerr != nil {
		t.Fatal(rerr)
	}
	_, _ = c.waitTerminal(ctx, id)
	if out, _, _, _ := c.terminalOutput(id); strings.TrimSpace(out) != "confined" || len(wrapped) != 2 || wrapped[0] != "echo" {
		t.Fatalf("the terminal must go through the confiner: %q %v", out, wrapped)
	}
	c.confine.Backend, wrapped = "microvm", nil
	id, _ = c.createTerminal(ctx, "echo", []string{"plain"}, nil, "", 0)
	_, _ = c.waitTerminal(ctx, id)
	if out, _, _, _ := c.terminalOutput(id); strings.TrimSpace(out) != "plain" || wrapped != nil {
		t.Fatalf("a microVM does not wrap each terminal: %q %v", out, wrapped)
	}
}
