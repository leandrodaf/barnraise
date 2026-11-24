package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/config"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

func TestLessonEndpoints(t *testing.T) {
	ctx := context.Background()
	svc := app.New(app.Deps{Store: testkit.NewStore(), Clock: testkit.NewClock(), Machine: "m1", Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	cfg, err := config.Compile(t.TempDir(), []byte("project: demo\nchecks:\n  main: { run: \"true\" }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddProject(ctx, cfg, app.ProjectPorts{}); err != nil {
		t.Fatal(err)
	}
	l := &domain.Lesson{ID: "L-0000abcd", Text: "In area wallet, step review sent the work back: floats.", Reason: "floats", Step: "review",
		Areas: []string{"wallet"}, Files: []string{"wallet/b.go"}, Sources: []string{"I-1#review@1"}, Count: 1, Version: 1}
	ev := domain.NewEvent("ev-l1", domain.EvLessonLearned, "demo", l.ID, "m2", "barnraise", time.Now(), domain.Payload{Lesson: l})
	if err := svc.Ingest(ctx, ev); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(svc, Options{OwnerToken: "owner-token"}).Handler())
	defer srv.Close()
	do := func(method, path, token string) (int, []byte) {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, body
	}
	if code, _ := do("GET", "/v1/lessons", ""); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	code, body := do("GET", "/v1/lessons?file=wallet/b.go", "owner-token")
	var list LessonsResponse
	if code != 200 || json.Unmarshal(body, &list) != nil || len(list.Lessons) != 1 || list.Lessons[0].ID != l.ID {
		t.Fatalf("list %d %s", code, body)
	}
	code, body = do("GET", "/v1/lessons?project=demo&area=cards", "owner-token")
	if code != 200 || string(body) != "{\"lessons\":[]}\n" {
		t.Fatalf("filtered %d %s", code, body)
	}
	if code, _ := do("GET", "/v1/lessons?project=nope", "owner-token"); code != http.StatusNotFound {
		t.Fatalf("unknown project %d", code)
	}
	if code, _ := do("DELETE", "/v1/lessons/I-1", "owner-token"); code != http.StatusBadRequest {
		t.Fatalf("bad id %d", code)
	}
	if code, body := do("DELETE", "/v1/lessons/l-0000ABCD", "owner-token"); code != 200 {
		t.Fatalf("delete %d %s", code, body)
	}
	if code, _ := do("DELETE", "/v1/lessons/L-0000abcd", "owner-token"); code != http.StatusNotFound {
		t.Fatalf("deleted twice %d", code)
	}
	// An agent token cannot delete.
	tok := svc.Tokens().Issue(app.Actor{Kind: app.ActorAgent, Name: "a", Project: "demo", Scopes: app.RoleScopes("implementer", false)}, time.Hour)
	if code, _ := do("DELETE", "/v1/lessons/L-0000abcd", tok); code != http.StatusForbidden {
		t.Fatalf("agent delete %d", code)
	}
}
