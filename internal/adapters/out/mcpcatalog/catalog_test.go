package mcpcatalog

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// The test binary is also a fake stdio MCP server.
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_MCP_SERVER") == "1" {
		fakeStdio()
		return
	}
	os.Exit(m.Run())
}

func fakeStdio() {
	if os.Getenv("FAKE_MCP_FAIL") != "" {
		fmt.Fprintln(os.Stderr, "\x1b[2m2026-11-27\x1b[0m INFO starting")
		fmt.Fprintln(os.Stderr, "\x1b[31mError: missing FIGMA_TOKEN\x1b[0m")
		fmt.Fprintln(os.Stderr, "bye")
		os.Exit(1)
	}
	if len(os.Getenv("CLAUDE_CODE_SESSION_ID")) != 36 {
		fmt.Fprintln(os.Stderr, "Error: CLAUDE_CODE_SESSION_ID is missing")
		os.Exit(1)
	}
	sc := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	fmt.Println("a log line on stdout")
	for sc.Scan() {
		var msg struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal(sc.Bytes(), &msg)
		if msg.ID == nil {
			continue
		}
		switch msg.Method {
		case "initialize":
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": map[string]any{"protocolVersion": protocolVersion}})
		case "tools/list":
			tools := []map[string]any{{"name": "search"}, {"name": os.Getenv("EXTRA_TOOL")}}
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": map[string]any{"tools": tools}})
		}
	}
}

func selfServer(t *testing.T, env map[string]string) domain.MCPServerDef {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	e := map[string]string{"FAKE_MCP_SERVER": "1"}
	for k, v := range env {
		e[k] = v
	}
	return domain.MCPServerDef{Command: exe, Env: e}
}

func TestStdioTools(t *testing.T) {
	tools, err := New().Tools(context.Background(), selfServer(t, map[string]string{"EXTRA_TOOL": "fetch"}))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tools, []string{"fetch", "search"}) {
		t.Fatalf("tools %v", tools)
	}
}

// A server that dies says why, from its stderr.
func TestStdioToolsFailureSaysWhy(t *testing.T) {
	_, err := New().Tools(context.Background(), selfServer(t, map[string]string{"FAKE_MCP_FAIL": "1"}))
	if err == nil || err.Error() != "Error: missing FIGMA_TOKEN" {
		t.Fatalf("err = %v", err)
	}
}

func TestStdioToolsNotInstalled(t *testing.T) {
	_, err := New().Tools(context.Background(), domain.MCPServerDef{Command: "no-such-mcp-server-xyz"})
	if err == nil || !strings.Contains(err.Error(), "not installed on this machine") {
		t.Fatalf("err = %v", err)
	}
}

// Streamable HTTP: a session id, headers on every request, an answer as JSON
// for initialize and as an event stream for tools/list.
func TestHTTPTools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var msg struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		switch msg.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "abc")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if r.Header.Get("Mcp-Session-Id") != "abc" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"tools\":[{\"name\":\"get_issue\"},{\"name\":\"create_issue\"}]}}\n\n")
		}
	}))
	defer srv.Close()
	def := domain.MCPServerDef{URL: srv.URL, Type: "http", Headers: map[string]string{"Authorization": "Bearer s3cret"}}
	tools, err := New().Tools(context.Background(), def)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tools, []string{"create_issue", "get_issue"}) {
		t.Fatalf("tools %v", tools)
	}
	def.Headers = nil
	if _, err := New().Tools(context.Background(), def); err == nil || !strings.Contains(err.Error(), "no headers") {
		t.Fatalf("err = %v", err)
	}
}

// What is not MCP is named: an OAuth server, a login page in front.
func TestHTTPProblems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth" {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="https://x/.well-known/oauth-protected-resource"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html>Sign in</html>")
	}))
	defer srv.Close()
	for path, want := range map[string]string{"/oauth": "OAuth", "/page": "web page"} {
		_, err := New().Tools(context.Background(), domain.MCPServerDef{URL: srv.URL + path, Type: "http"})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", path, err)
		}
	}
}

// The older HTTP+SSE transport: an endpoint event, answers on the stream.
func TestSSETools(t *testing.T) {
	var mu sync.Mutex
	var stream chan string
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		stream = make(chan string, 4)
		ch := stream
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: endpoint\ndata: /messages?s=1\n\n")
		w.(http.Flusher).Flush()
		for {
			select {
			case m := <-ch:
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", m)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		w.WriteHeader(http.StatusAccepted)
		mu.Lock()
		ch := stream
		mu.Unlock()
		switch msg.Method {
		case "initialize":
			ch <- `{"jsonrpc":"2.0","id":1,"result":{}}`
		case "tools/list":
			ch <- `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"query"}]}}`
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	tools, err := New().Tools(context.Background(), domain.MCPServerDef{URL: srv.URL + "/sse", Type: "sse"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tools, []string{"query"}) {
		t.Fatalf("tools %v", tools)
	}
}

func TestToolsTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // a server that never answers
	}))
	defer srv.Close()
	defer close(release)
	c := &Catalog{Timeout: 200 * time.Millisecond}
	if _, err := c.Tools(context.Background(), domain.MCPServerDef{URL: srv.URL, Type: "http"}); err == nil || !strings.Contains(err.Error(), "no answer") {
		t.Fatalf("err = %v", err)
	}
}

// Find reads the project's .mcp.json and the user's ~/.claude.json, its
// servers for every folder and those for the project's folder only, and
// expands ${VAR} as Claude does.
func TestFind(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, ".mcp.json"), `{"mcpServers":{
		"figma":{"command":"figma-mcp","env":{"FIGMA_TOKEN":"${FIGMA_TOKEN}"}},
		"docs":{"type":"http","url":"${DOCS_URL:-https://docs.example/mcp}"}}}`)
	claude := map[string]any{
		"mcpServers": map[string]any{
			"github": map[string]any{"type": "http", "url": "https://api.githubcopilot.com/mcp/", "headers": map[string]string{"Authorization": "Bearer x"}},
			"figma":  map[string]any{"command": "other-figma"},
		},
		"projects": map[string]any{
			root:           map[string]any{"mcpServers": map[string]any{"db": map[string]any{"command": "pg-mcp", "args": []string{"--ro"}}}},
			"/elsewhere/x": map[string]any{"mcpServers": map[string]any{"secret": map[string]any{"command": "x"}}},
		},
	}
	b, _ := json.Marshal(claude)
	write(filepath.Join(home, ".claude.json"), string(b))
	c := &Catalog{Home: home, Getenv: func(k string) string { return map[string]string{"FIGMA_TOKEN": "tok"}[k] }}
	found, err := c.Find(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]domain.MCPFound{}
	for _, f := range found {
		got[f.Name] = f
	}
	if len(got) != 4 || got["secret"].Name != "" {
		t.Fatalf("found %+v", found)
	}
	if f := got["figma"]; f.Source != domain.MCPFromProject || f.Def.Command != "figma-mcp" || f.Def.Env["FIGMA_TOKEN"] != "tok" {
		t.Errorf("figma %+v: the project's .mcp.json wins over the user's", f)
	}
	if f := got["docs"]; f.Def.URL != "https://docs.example/mcp" || f.Kind() != "remote" {
		t.Errorf("docs %+v", f)
	}
	if f := got["db"]; f.Source != domain.MCPFromLocal || !slices.Equal(f.Def.Args, []string{"--ro"}) {
		t.Errorf("db %+v", f)
	}
	if f := got["github"]; f.Source != domain.MCPFromUser || f.Def.Headers["Authorization"] != "Bearer x" || f.Def.Type != "http" {
		t.Errorf("github %+v", f)
	}
	if st := domain.StatusOf(got["github"]); st.Summary != "api.githubcopilot.com/mcp/" || strings.Contains(fmt.Sprint(st), "Bearer") {
		t.Errorf("status %+v carries more than it should", st)
	}
}

func TestFindWithoutFiles(t *testing.T) {
	found, err := (&Catalog{Home: t.TempDir()}).Find(context.Background(), t.TempDir())
	if err != nil || len(found) != 0 {
		t.Fatalf("%v %v", found, err)
	}
}
