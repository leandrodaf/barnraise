package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/config"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

func TestDocumentEndpoints(t *testing.T) {
	ctx := context.Background()
	svc := app.New(app.Deps{Store: testkit.NewStore(), Clock: testkit.NewClock(), Machine: "m1", Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	root := t.TempDir()
	cfg, err := config.Compile(root, []byte("project: demo\nchecks:\n  main: { run: \"true\" }\ndocuments: { diagrams: always }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddProject(ctx, cfg, app.ProjectPorts{}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(svc, Options{OwnerToken: "owner-token"}).Handler())
	defer srv.Close()
	do := func(method, path, token, body string) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	code, body := do("GET", "/v1/documents?project=demo", "owner-token", "")
	var list app.DocumentsView
	if code != 200 || json.Unmarshal([]byte(body), &list) != nil || len(list.Documents) != 4 || list.Diagrams != "always" || list.Documents[0].BuiltIn == "" {
		t.Fatalf("list %d %s", code, body)
	}

	// A template that does not render is refused; one that does is written.
	if code, body := do("PUT", "/v1/documents/pr?project=demo", "owner-token", `{"text":"{{.Item.Nope}}"}`); code != http.StatusBadRequest {
		t.Fatalf("broken save %d %s", code, body)
	}
	if code, body := do("PUT", "/v1/documents/pr?project=demo&variant=../x", "owner-token", `{"text":"x"}`); code != http.StatusBadRequest {
		t.Fatalf("bad variant %d %s", code, body)
	}
	if code, body := do("PUT", "/v1/documents/pr?project=demo&variant=bug", "owner-token", `{"text":"Bug {{.Item.ID}}"}`); code != 200 {
		t.Fatalf("save %d %s", code, body)
	}
	if b, err := os.ReadFile(filepath.Join(root, ".barnraise", "documents", "pr.bug.md")); err != nil || string(b) != "Bug {{.Item.ID}}" {
		t.Fatalf("file %q %v", b, err)
	}

	code, body = do("POST", "/v1/documents/plan/preview?project=demo", "owner-token", `{"text":"{{.Item.ID}} {{.DiagramsMode}}"}`)
	if code != 200 || !strings.Contains(body, `"text":"I-42 always\n"`) {
		t.Fatalf("preview %d %s", code, body)
	}
	code, body = do("POST", "/v1/documents/plan/preview?project=demo", "owner-token", `{"text":"{{if}}"}`)
	if code != 200 || !strings.Contains(body, `"error"`) {
		t.Fatalf("broken preview %d %s", code, body)
	}

	// Agents never read or write templates through the API.
	tok := svc.Tokens().Issue(app.Actor{Kind: app.ActorAgent, Name: "a", Project: "demo", Scopes: app.RoleScopes("implementer", false)}, time.Hour)
	for _, c := range [][2]string{{"GET", "/v1/documents?project=demo"}, {"PUT", "/v1/documents/pr?project=demo"}, {"DELETE", "/v1/documents/pr?project=demo&variant=bug"}} {
		if code, _ := do(c[0], c[1], tok, `{"text":"x"}`); code != http.StatusForbidden {
			t.Errorf("agent %s %s: %d", c[0], c[1], code)
		}
	}

	if code, body := do("DELETE", "/v1/documents/pr?project=demo&variant=bug", "owner-token", ""); code != 200 {
		t.Fatalf("delete %d %s", code, body)
	}
	if code, _ := do("DELETE", "/v1/documents/pr?project=demo&variant=bug", "owner-token", ""); code != http.StatusNotFound {
		t.Fatalf("delete again %d", code)
	}
	if code, _ := do("GET", "/v1/items/I-404/documents?project=demo", "owner-token", ""); code != http.StatusNotFound {
		t.Fatalf("unknown item %d", code)
	}
}
