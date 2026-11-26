package httpapi

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/config"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

// The live view is the owner's: a session token is refused before the
// stream opens, and so is a token without admin. The owner gets the item's
// messages, the state of its session, and new messages as they come.
func TestLiveRoute(t *testing.T) {
	ctx := context.Background()
	svc := app.New(app.Deps{Store: testkit.NewStore(), Clock: testkit.NewClock(), Machine: "m1", Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	cfg, err := config.Compile(t.TempDir(), []byte("project: demo\nchecks:\n  main: { run: \"true\" }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddProject(ctx, cfg, app.ProjectPorts{}); err != nil {
		t.Fatal(err)
	}
	msg := func(id, from, text string) domain.Event {
		m := domain.Message{ID: id, From: from, To: "item:I-1", ItemID: "I-1", Text: text, At: time.Now()}
		return domain.NewEvent("ev-"+id, domain.EvMessageSent, "demo", "I-1", "m2", from, time.Now(), domain.Payload{Message: &m})
	}
	if err := svc.Ingest(ctx, msg("1", "owner", "first")); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(svc, Options{OwnerToken: "owner-token"}).Handler())
	defer srv.Close()
	open := func(path, token string) *http.Response {
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	agentTok := svc.Tokens().Issue(app.Actor{Kind: app.ActorAgent, Name: "impl-1", Project: "demo", ItemID: "I-1", Scopes: app.RoleScopes("implementer", false)}, time.Hour)
	if res := open("/v1/items/I-1/live?project=demo", agentTok); res.StatusCode != http.StatusForbidden {
		t.Fatalf("session token: %d", res.StatusCode)
	} else {
		res.Body.Close()
	}
	readTok := svc.Tokens().Issue(app.Actor{Kind: app.ActorToken, Name: "ro", Scopes: []string{app.ScopeItemsRead, app.ScopeEvents}}, time.Hour)
	if res := open("/v1/items/I-1/live?project=demo", readTok); res.StatusCode != http.StatusForbidden {
		t.Fatalf("read-only token: %d", res.StatusCode)
	} else {
		res.Body.Close()
	}
	if res := open("/v1/items/I-1/live?project=nope", "owner-token"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown project: %d", res.StatusCode)
	} else {
		res.Body.Close()
	}

	res := open("/v1/items/I-1/live?project=demo", "owner-token")
	defer res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("owner: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	r := bufio.NewReader(res.Body)
	expect := func(event, text string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			l, err := r.ReadString('\n')
			if err != nil {
				t.Fatalf("waiting for %s: %v", event, err)
			}
			if strings.TrimSpace(l) != "event: "+event {
				continue
			}
			data, _ := r.ReadString('\n')
			if !strings.Contains(data, text) {
				t.Fatalf("%s: %s", event, data)
			}
			return
		}
		t.Fatalf("no %s", event)
	}
	expect("message", `"first"`)
	expect("idle", "{}")
	if err := svc.Ingest(ctx, msg("2", "impl-1", "an answer")); err != nil {
		t.Fatal(err)
	}
	expect("message", `"an answer"`)

	// A session elsewhere, with no way to reach that machine: the panel is told which one.
	info := &domain.SessionInfo{Agent: "impl-9", ItemID: "I-2", State: "working", Machine: "m2", At: time.Now()}
	e := domain.NewEvent("ev-s", domain.EvAgentSession, "demo", "I-2", "m2", "impl-9", time.Now(), domain.Payload{Session: info})
	if err := svc.Ingest(ctx, e); err != nil {
		t.Fatal(err)
	}
	far := open("/v1/items/I-2/live?project=demo", "owner-token")
	defer far.Body.Close()
	r = bufio.NewReader(far.Body)
	expect("elsewhere", `"machine":"m2"`)
}
