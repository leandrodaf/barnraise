package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/ports"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

type chatStub struct {
	mu      sync.Mutex
	running bool
	sent    []string
	calls   []string
}

func (c *chatStub) Start(spec ports.ChatSpec) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.running = true
	c.calls = append(c.calls, "start")
	return spec.SessionID, nil
}
func (c *chatStub) Send(t string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, t)
	return nil
}
func (c *chatStub) Interrupt() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, "interrupt")
	return nil
}
func (c *chatStub) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.running = false
	c.calls = append(c.calls, "stop")
	return nil
}
func (c *chatStub) Status() ports.ChatStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ports.ChatStatus{Running: c.running}
}
func (c *chatStub) Transcript(string, string, string, int) ([]ports.ChatEntry, error) {
	return []ports.ChatEntry{{Role: "assistant", Text: "hello"}}, nil
}

func TestConversationEndpoints(t *testing.T) {
	chat := &chatStub{}
	svc := app.New(app.Deps{Store: testkit.NewStore(), Clock: testkit.NewClock(), Machine: "m1", DataDir: t.TempDir(), Chat: chat,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	srv := httptest.NewServer(New(svc, Options{OwnerToken: "owner-token"}).Handler())
	defer srv.Close()
	do := func(method, path, body string) (int, []byte) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer owner-token")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b
	}
	code, body := do("GET", "/v1/conversation?after=0", "")
	var v app.ConversationView
	if code != 200 || json.Unmarshal(body, &v) != nil || !v.Available || len(v.Entries) != 1 || v.SessionID == "" {
		t.Fatalf("view %d %s", code, body)
	}
	if code, body := do("POST", "/v1/conversation/messages", `{"text":"what is stuck?"}`); code != 200 {
		t.Fatalf("send %d %s", code, body)
	}
	if code, _ := do("POST", "/v1/conversation/messages", `{"text":"  "}`); code != http.StatusBadRequest {
		t.Fatalf("empty: %d", code)
	}
	if code, _ := do("POST", "/v1/conversation/messages", `not json`); code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", code)
	}
	for _, a := range []string{"interrupt", "stop", "start", "new", "keep", "release"} {
		if code, body := do("POST", "/v1/conversation/"+a, "{}"); code != 200 {
			t.Fatalf("%s: %d %s", a, code, body)
		}
	}
	if code, _ := do("POST", "/v1/conversation/explode", "{}"); code != http.StatusNotFound {
		t.Fatalf("unknown action: %d", code)
	}
	if len(chat.sent) != 1 || chat.sent[0] != "what is stuck?" {
		t.Fatalf("sent %q", chat.sent)
	}
}
