package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
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
	"github.com/leandrodaf/barnraise/internal/ports"
)

type call struct {
	Method string
	Path   string
	Query  string
	Body   map[string]any
	GQL    string
	Vars   map[string]any
}

// fake is a GitHub stand-in: routes are "METHOD /path" for REST and
// "GQL <keyword>" for GraphQL, matched by a keyword of the query.
type fake struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	calls  []call
	routes map[string]http.HandlerFunc
}

func newFake(t *testing.T) *fake {
	f := &fake{t: t, routes: map[string]http.HandlerFunc{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	if got := r.Header.Get("Authorization"); got != "Bearer tok" {
		f.t.Errorf("%s %s: Authorization = %q", r.Method, r.URL.Path, got)
	}
	if r.URL.Path != "/graphql" && r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
		f.t.Errorf("%s %s: missing API version header", r.Method, r.URL.Path)
	}
	raw, _ := io.ReadAll(r.Body)
	c := call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &c.Body); err != nil {
			f.t.Errorf("body is not JSON: %s", raw)
		}
	}
	key := r.Method + " " + r.URL.Path
	if r.URL.Path == "/graphql" {
		c.GQL, _ = c.Body["query"].(string)
		c.Vars, _ = c.Body["variables"].(map[string]any)
		key = ""
		for k := range f.routes {
			if kw, ok := strings.CutPrefix(k, "GQL "); ok && strings.Contains(c.GQL, kw) {
				key = k
			}
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	h := f.routes[key]
	f.mu.Unlock()
	if h == nil {
		f.t.Errorf("unexpected request %s %s %s", r.Method, r.URL.Path, c.GQL)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	h(w, r)
}

func (f *fake) on(key string, h http.HandlerFunc) { f.routes[key] = h }

func (f *fake) callsTo(method, path string) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if c.Method == method && c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

func (f *fake) gqlCalls(kw string) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if c.GQL != "" && strings.Contains(c.GQL, kw) {
			out = append(out, c)
		}
	}
	return out
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func reply(t *testing.T, status int, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if name != "" {
			w.Write(fixture(t, name))
		}
	}
}

func (f *fake) client(t *testing.T, mod func(*Options)) *Client {
	opts := Options{Repo: "acme/app", Token: "tok", BaseURL: f.srv.URL, Backoff: func(int) time.Duration { return time.Millisecond }}
	if mod != nil {
		mod(&opts)
	}
	c, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func sampleItem() *domain.Item {
	return &domain.Item{
		ID: "I-42", Title: "Login screen remembers the last email", Body: "Users retype their email every time.",
		Kind: "bug", Priority: domain.P1, Area: "auth", Size: "2h-6h", Status: domain.StatusReady,
		Criteria: []domain.Criterion{{ID: "C1", Text: "WHEN the app opens, THE SYSTEM SHALL fill the last email", Check: "LoginTest"}},
		Preserve: []string{"Password is never stored"},
	}
}

func TestNewDefaults(t *testing.T) {
	c, err := New(Options{Repo: "acme/app"})
	if err != nil {
		t.Fatal(err)
	}
	if c.opts.BaseURL != "https://api.github.com" || c.graphqlURL != "https://api.github.com/graphql" || c.opts.Owner != "acme" {
		t.Fatalf("defaults: %+v %s", c.opts, c.graphqlURL)
	}
	ghes, _ := New(Options{Repo: "a/b", BaseURL: "https://git.corp/api/v3/"})
	if ghes.graphqlURL != "https://git.corp/api/graphql" {
		t.Fatalf("GHES graphql = %s", ghes.graphqlURL)
	}
	for _, bad := range []string{"", "acme", "a/b/c", "/b"} {
		if _, err := New(Options{Repo: bad}); err == nil {
			t.Errorf("repo %q accepted", bad)
		}
	}
	if c.Tracker().Name() != "github" || c.CodeHost().Name() != "github" {
		t.Fatal("name")
	}
}

func TestParseRef(t *testing.T) {
	for in, want := range map[string]int{"#1608": 1608, "1608": 1608, "acme/app#7": 7, "https://github.com/acme/app/issues/12": 12} {
		if n, err := ParseRef(in); err != nil || n != want {
			t.Errorf("ParseRef(%q) = %d, %v", in, n, err)
		}
	}
	if _, err := ParseRef("I-42"); !errors.Is(err, ErrBadRef) {
		t.Errorf("I-42 accepted: %v", err)
	}
}

func TestListPaginatesSkipsPRsAndSanitizes(t *testing.T) {
	f := newFake(t)
	f.on("GET /repos/acme/app/issues", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			reply(t, 200, "issues_page2.json")(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("since") != "2026-11-01T00:00:00Z" || q.Get("state") != "all" || q.Get("per_page") != "100" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		w.Header().Set("Link", `<`+f.srv.URL+`/repos/acme/app/issues?page=2&per_page=100>; rel="next", <`+f.srv.URL+`/repos/acme/app/issues?page=2>; rel="last"`)
		reply(t, 200, "issues_page1.json")(w, r)
	})
	items, err := f.client(t, nil).Tracker().List(context.Background(), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items: %+v", len(items), items)
	}
	a := items[0]
	if a.Ref != "#1608" || a.Number != 1608 || a.Author != "maria" || a.Status != "in_progress" || a.Closed ||
		a.URL != "https://github.com/acme/app/issues/1608" || !slices.Contains(a.Labels, "area:auth") {
		t.Errorf("item 0 = %+v", a)
	}
	if strings.Contains(a.Body, "ignore previous") || strings.Contains(a.Body, "\u200b") || a.Body != "Please fix this.\nThanks!" {
		t.Errorf("body not sanitized: %q", a.Body)
	}
	b := items[1]
	if b.Ref != "#1611" || !b.Closed || b.Status != "closed" || !b.Updated.Equal(time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("item 1 = %+v", b)
	}
	if n := len(f.callsTo("GET", "/repos/acme/app/issues")); n != 2 {
		t.Errorf("pages fetched = %d", n)
	}
}

func TestCreatePortuguese(t *testing.T) {
	f := newFake(t)
	f.on("POST /repos/acme/app/issues", reply(t, 201, "issue_created.json"))
	ti, err := f.client(t, nil).Tracker().Create(context.Background(), sampleItem(), "pt-BR")
	if err != nil {
		t.Fatal(err)
	}
	if ti.Ref != "#1608" || ti.URL != "https://github.com/acme/app/issues/1608" {
		t.Errorf("created = %+v", ti)
	}
	c := f.callsTo("POST", "/repos/acme/app/issues")[0]
	if c.Body["title"] != "[I-42] Login screen remembers the last email" {
		t.Errorf("title = %v", c.Body["title"])
	}
	body := c.Body["body"].(string)
	for _, want := range []string{"## Objetivo", "## Critérios", "- [ ] C1: WHEN the app opens", "(verificação: LoginTest)", "## Área", "auth", "## Tamanho", "## O que não pode mudar", "- Password is never stored"} {
		if !strings.Contains(body, want) {
			t.Errorf("body misses %q:\n%s", want, body)
		}
	}
	labels := toStrings(c.Body["labels"])
	for _, want := range []string{"barnraise", "area:auth", "kind:bug", "P1", "status:ready"} {
		if !slices.Contains(labels, want) {
			t.Errorf("labels %v miss %s", labels, want)
		}
	}
}

func TestCreateEnglishHeadings(t *testing.T) {
	f := newFake(t)
	f.on("POST /repos/acme/app/issues", reply(t, 201, "issue_created.json"))
	if _, err := f.client(t, nil).Tracker().Create(context.Background(), sampleItem(), "en"); err != nil {
		t.Fatal(err)
	}
	body := f.callsTo("POST", "/repos/acme/app/issues")[0].Body["body"].(string)
	for _, want := range []string{"## Goal", "## Criteria", "## Area", "## Size", "## What must not change", "(check: LoginTest)"} {
		if !strings.Contains(body, want) {
			t.Errorf("body misses %q", want)
		}
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func projectFake(t *testing.T, itemsFixture string) *fake {
	f := newFake(t)
	f.on("GET /repos/acme/app/issues/1608", reply(t, 200, "issue_current.json"))
	f.on("PATCH /repos/acme/app/issues/1608", reply(t, 200, "issue_created.json"))
	f.on("GQL repositoryOwner", reply(t, 200, "gql_project.json"))
	f.on("GQL projectItems", reply(t, 200, itemsFixture))
	f.on("GQL addProjectV2ItemById", reply(t, 200, "gql_add_item.json"))
	f.on("GQL updateProjectV2ItemFieldValue", reply(t, 200, "gql_set_status.json"))
	return f
}

func TestUpdateSetsLabelsStateAndProjectStatus(t *testing.T) {
	f := projectFake(t, "gql_issue_items_empty.json")
	c := f.client(t, func(o *Options) {
		o.Project = 3
		o.Owner = "acme-org"
		o.Columns = map[string]string{"in_progress": "In Progress", "integrated": "Done"}
	})
	it := sampleItem()
	it.TrackerRef = "#1608"
	it.Status = domain.StatusInProgress
	if err := c.Tracker().Update(context.Background(), it, "en"); err != nil {
		t.Fatal(err)
	}
	patch := f.callsTo("PATCH", "/repos/acme/app/issues/1608")[0].Body
	if patch["state"] != "open" || patch["title"] != "[I-42] Login screen remembers the last email" {
		t.Errorf("patch = %v", patch)
	}
	labels := toStrings(patch["labels"])
	slices.Sort(labels)
	want := []string{"P1", "area:auth", "barnraise", "kind:bug", "needs-design", "status:in_progress"}
	if !slices.Equal(labels, want) {
		t.Errorf("labels = %v, want %v", labels, want)
	}
	pq := f.gqlCalls("repositoryOwner")
	if len(pq) != 1 || pq[0].Vars["owner"] != "acme-org" || pq[0].Vars["number"] != float64(3) {
		t.Fatalf("project query = %+v", pq)
	}
	iq := f.gqlCalls("projectItems")[0]
	if iq.Vars["owner"] != "acme" || iq.Vars["name"] != "app" || iq.Vars["number"] != float64(1608) {
		t.Errorf("issue query vars = %v", iq.Vars)
	}
	add := f.gqlCalls("addProjectV2ItemById")
	if len(add) != 1 || add[0].Vars["project"] != "PVT_kwDOAbcProj" || add[0].Vars["content"] != "I_kwDOAbc1608" {
		t.Fatalf("add = %+v", add)
	}
	set := f.gqlCalls("updateProjectV2ItemFieldValue")
	if len(set) != 1 {
		t.Fatal("status not set")
	}
	v := set[0].Vars
	if v["project"] != "PVT_kwDOAbcProj" || v["item"] != "PVTI_new" || v["field"] != "PVTSSF_status" || v["option"] != "47fc9ee4" {
		t.Errorf("set vars = %v", v)
	}

	// Second update: project info is cached, card exists, closed when integrated.
	f2 := projectFake(t, "gql_issue_items_present.json")
	c2 := f2.client(t, func(o *Options) { o.Project = 3; o.Columns = map[string]string{"integrated": "Done"} })
	it.Status = domain.StatusIntegrated
	if err := c2.Tracker().Update(context.Background(), it, "en"); err != nil {
		t.Fatal(err)
	}
	if err := c2.Tracker().Update(context.Background(), it, "en"); err != nil {
		t.Fatal(err)
	}
	p2 := f2.callsTo("PATCH", "/repos/acme/app/issues/1608")[0].Body
	if p2["state"] != "closed" || p2["state_reason"] != "completed" {
		t.Errorf("closed patch = %v", p2)
	}
	if len(f2.gqlCalls("repositoryOwner")) != 1 {
		t.Error("project info not cached")
	}
	if len(f2.gqlCalls("addProjectV2ItemById")) != 0 {
		t.Error("existing card added again")
	}
	s2 := f2.gqlCalls("updateProjectV2ItemFieldValue")[0].Vars
	if s2["item"] != "PVTI_existing" || s2["option"] != "a1b2c3d4" {
		t.Errorf("set vars = %v", s2)
	}
}

func TestUpdateUnmappedStatusLeavesProjectAlone(t *testing.T) {
	f := projectFake(t, "gql_issue_items_present.json")
	c := f.client(t, func(o *Options) { o.Project = 3 })
	it := sampleItem()
	it.TrackerRef = "#1608"
	it.Status = domain.StatusWaitingOwner
	if err := c.Tracker().Update(context.Background(), it, "en"); err != nil {
		t.Fatal(err)
	}
	if len(f.gqlCalls("updateProjectV2ItemFieldValue")) != 0 {
		t.Error("unmapped status moved the card")
	}
	// A mapping to a missing option is an error.
	c = f.client(t, func(o *Options) { o.Project = 3; o.Columns = map[string]string{"waiting_owner": "Blocked by owner"} })
	if err := c.Tracker().Update(context.Background(), it, "en"); err == nil || !strings.Contains(err.Error(), "Blocked by owner") {
		t.Errorf("err = %v", err)
	}
}

func TestColumns(t *testing.T) {
	f := projectFake(t, "gql_issue_items_present.json")
	cols, err := f.client(t, func(o *Options) { o.Project = 3 }).Tracker().Columns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cols, []string{"Todo", "In Progress", "Review", "Done"}) {
		t.Errorf("cols = %v", cols)
	}
	def, _ := f.client(t, nil).Tracker().Columns(context.Background())
	if len(def) != 9 || def[0] != "draft" {
		t.Errorf("default cols = %v", def)
	}
}

func TestTrackerComment(t *testing.T) {
	f := newFake(t)
	f.on("POST /repos/acme/app/issues/1608/comments", reply(t, 201, ""))
	if err := f.client(t, nil).Tracker().Comment(context.Background(), "#1608", "hello"); err != nil {
		t.Fatal(err)
	}
	if b := f.callsTo("POST", "/repos/acme/app/issues/1608/comments")[0].Body; b["body"] != "hello" {
		t.Errorf("body = %v", b)
	}
}

func TestGraphQLErrorsSurface(t *testing.T) {
	f := newFake(t)
	f.on("GQL repositoryOwner", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"repositoryOwner":null},"errors":[{"type":"NOT_FOUND","message":"Could not resolve to a ProjectV2 with the number 9."}]}`))
	})
	_, err := f.client(t, func(o *Options) { o.Project = 9 }).Tracker().Columns(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Could not resolve") {
		t.Errorf("err = %v", err)
	}
}

func TestOpenDraftPRReusesExistingHead(t *testing.T) {
	f := newFake(t)
	f.on("GET /repos/acme/app/pulls", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("head") != "acme:br/I-42-remember-email" || r.URL.Query().Get("state") != "open" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		w.Write([]byte("[" + string(fixture(t, "pull_draft.json")) + "]"))
	})
	pr, err := f.client(t, nil).CodeHost().OpenDraftPR(context.Background(), "br/I-42-remember-email", "main", "t", "b")
	if err != nil {
		t.Fatal(err)
	}
	want := ports.PR{Number: 1610, URL: "https://github.com/acme/app/pull/1610", State: "open", Draft: true, Head: "br/I-42-remember-email"}
	if pr != want {
		t.Errorf("pr = %+v", pr)
	}
	if len(f.callsTo("POST", "/repos/acme/app/pulls")) != 0 {
		t.Error("created a second PR")
	}
}

func TestOpenDraftPRCreates(t *testing.T) {
	f := newFake(t)
	f.on("GET /repos/acme/app/pulls", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("[]")) })
	f.on("POST /repos/acme/app/pulls", reply(t, 201, "pull_draft.json"))
	pr, err := f.client(t, nil).CodeHost().OpenDraftPR(context.Background(), "br/I-42-remember-email", "main", "[I-42] Remember last email", "body")
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 1610 || !pr.Draft {
		t.Errorf("pr = %+v", pr)
	}
	b := f.callsTo("POST", "/repos/acme/app/pulls")[0].Body
	if b["draft"] != true || b["head"] != "br/I-42-remember-email" || b["base"] != "main" || b["title"] != "[I-42] Remember last email" || b["body"] != "body" {
		t.Errorf("create body = %v", b)
	}
}

func TestOpenDraftPRFallsBackWithoutDraftSupport(t *testing.T) {
	f := newFake(t)
	f.on("GET /repos/acme/app/pulls", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("[]")) })
	n := 0
	f.on("POST /repos/acme/app/pulls", func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(422)
			w.Write([]byte(`{"message":"Validation Failed","errors":[{"message":"Draft pull requests are not supported in this repository."}]}`))
			return
		}
		reply(t, 201, "pull_draft.json")(w, r)
	})
	if _, err := f.client(t, nil).CodeHost().OpenDraftPR(context.Background(), "br/x", "main", "t", "b"); err != nil {
		t.Fatal(err)
	}
	if calls := f.callsTo("POST", "/repos/acme/app/pulls"); len(calls) != 2 || calls[1].Body["draft"] != false {
		t.Errorf("calls = %+v", calls)
	}
}

func TestMarkReadyUsesNodeID(t *testing.T) {
	f := newFake(t)
	f.on("GET /repos/acme/app/pulls/1610", reply(t, 200, "pull_draft.json"))
	f.on("GQL markPullRequestReadyForReview", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"markPullRequestReadyForReview":{"pullRequest":{"isDraft":false}}}}`))
	})
	if err := f.client(t, nil).CodeHost().MarkReady(context.Background(), ports.PR{Number: 1610}); err != nil {
		t.Fatal(err)
	}
	m := f.gqlCalls("markPullRequestReadyForReview")
	if len(m) != 1 || m[0].Vars["id"] != "PR_kwDOAbc1610" {
		t.Errorf("mutation = %+v", m)
	}
}

func TestReviewFallsBackToCommentOn422(t *testing.T) {
	f := newFake(t)
	f.on("POST /repos/acme/app/pulls/1610/reviews", func(w http.ResponseWriter, r *http.Request) {
		if len(f.callsTo("POST", "/repos/acme/app/pulls/1610/reviews")) == 1 {
			reply(t, 422, "review_422.json")(w, r)
			return
		}
		w.Write([]byte(`{"id":80,"state":"COMMENTED"}`))
	})
	if err := f.client(t, nil).CodeHost().Review(context.Background(), ports.PR{Number: 1610}, true, "All criteria met."); err != nil {
		t.Fatal(err)
	}
	calls := f.callsTo("POST", "/repos/acme/app/pulls/1610/reviews")
	if len(calls) != 2 || calls[0].Body["event"] != "APPROVE" || calls[1].Body["event"] != "COMMENT" {
		t.Fatalf("calls = %+v", calls)
	}
	if body := calls[1].Body["body"].(string); !strings.Contains(body, "Approved") || !strings.Contains(body, "All criteria met.") {
		t.Errorf("fallback body = %q", body)
	}
}

func TestReviewRequestChanges(t *testing.T) {
	f := newFake(t)
	f.on("POST /repos/acme/app/pulls/1610/reviews", reply(t, 200, ""))
	if err := f.client(t, nil).CodeHost().Review(context.Background(), ports.PR{Number: 1610}, false, "C1 not met"); err != nil {
		t.Fatal(err)
	}
	if b := f.callsTo("POST", "/repos/acme/app/pulls/1610/reviews")[0].Body; b["event"] != "REQUEST_CHANGES" || b["body"] != "C1 not met" {
		t.Errorf("body = %v", b)
	}
}

func TestReportCheck(t *testing.T) {
	f := newFake(t)
	sha := "6dcb09b5b57875f334f61aebed695e2e4193db5e"
	f.on("POST /repos/acme/app/statuses/"+sha, reply(t, 201, ""))
	h := f.client(t, nil).CodeHost()
	if err := h.ReportCheck(context.Background(), sha, "unit", true, "42 passed"); err != nil {
		t.Fatal(err)
	}
	if err := h.ReportCheck(context.Background(), sha, "lint", false, strings.Repeat("x", 300)); err != nil {
		t.Fatal(err)
	}
	calls := f.callsTo("POST", "/repos/acme/app/statuses/"+sha)
	if calls[0].Body["state"] != "success" || calls[0].Body["context"] != "barnraise/unit" || calls[0].Body["description"] != "42 passed" {
		t.Errorf("status 0 = %v", calls[0].Body)
	}
	if calls[1].Body["state"] != "failure" || len([]rune(calls[1].Body["description"].(string))) != 140 {
		t.Errorf("status 1 = %v", calls[1].Body)
	}
}

func TestMergeCloseDispatchUpdate(t *testing.T) {
	f := newFake(t)
	f.on("PUT /repos/acme/app/pulls/1610/merge", reply(t, 200, "merge_ok.json"))
	f.on("POST /repos/acme/app/issues/1610/comments", reply(t, 201, ""))
	f.on("PATCH /repos/acme/app/pulls/1610", reply(t, 200, "pull_draft.json"))
	f.on("POST /repos/acme/app/actions/workflows/ci.yml/dispatches", reply(t, 204, ""))
	h := f.client(t, nil).CodeHost()
	ctx := context.Background()
	pr := ports.PR{Number: 1610}
	sha, err := h.Merge(ctx, pr, "")
	if err != nil || sha != "6dcb09b5b57875f334f61aebed695e2e4193db5e" {
		t.Fatalf("merge = %s, %v", sha, err)
	}
	if b := f.callsTo("PUT", "/repos/acme/app/pulls/1610/merge")[0].Body; b["merge_method"] != "squash" {
		t.Errorf("merge body = %v", b)
	}
	if _, err := h.Merge(ctx, pr, "octopus"); err == nil {
		t.Error("bad method accepted")
	}
	if err := h.Close(ctx, pr, "Split into I-43 and I-44."); err != nil {
		t.Fatal(err)
	}
	if b := f.callsTo("POST", "/repos/acme/app/issues/1610/comments")[0].Body; b["body"] != "Split into I-43 and I-44." {
		t.Errorf("close comment = %v", b)
	}
	if b := f.callsTo("PATCH", "/repos/acme/app/pulls/1610")[0].Body; b["state"] != "closed" {
		t.Errorf("close patch = %v", b)
	}
	if err := h.UpdatePR(ctx, pr, "new title", ""); err != nil {
		t.Fatal(err)
	}
	if b := f.callsTo("PATCH", "/repos/acme/app/pulls/1610")[1].Body; b["title"] != "new title" || b["body"] != nil {
		t.Errorf("update body = %v", b)
	}
	if err := h.Comment(ctx, pr, "prints attached"); err != nil {
		t.Fatal(err)
	}
	if err := h.DispatchWorkflow(ctx, "ci.yml", "br/I-42-remember-email", map[string]string{"item": "I-42"}); err != nil {
		t.Fatal(err)
	}
	b := f.callsTo("POST", "/repos/acme/app/actions/workflows/ci.yml/dispatches")[0].Body
	if b["ref"] != "br/I-42-remember-email" || b["inputs"].(map[string]any)["item"] != "I-42" {
		t.Errorf("dispatch body = %v", b)
	}
}

func TestRetriesOn502And429(t *testing.T) {
	f := newFake(t)
	n := 0
	f.on("POST /repos/acme/app/issues/1608/comments", func(w http.ResponseWriter, r *http.Request) {
		n++
		switch n {
		case 1:
			w.WriteHeader(http.StatusBadGateway)
		case 2:
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.WriteHeader(http.StatusCreated)
		}
	})
	var slept []time.Duration
	c := f.client(t, func(o *Options) {
		o.Sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	})
	if err := c.Tracker().Comment(context.Background(), "#1608", "x"); err != nil {
		t.Fatal(err)
	}
	if n != 3 || len(slept) != 2 || slept[0] != time.Millisecond || slept[1] != 3*time.Second {
		t.Errorf("attempts = %d, slept = %v", n, slept)
	}
}

func sign(secret, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	body := fixture(t, "hook_push.json")
	secret := []byte("It's a Secret to Everybody")
	good := sign(secret, body)
	if !VerifySignature(secret, body, good) {
		t.Error("valid signature rejected")
	}
	// Known vector from GitHub's documentation.
	if !VerifySignature(secret, []byte("Hello, World!"), "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17") {
		t.Error("documented vector rejected")
	}
	for _, bad := range []string{"", "sha256=", "sha1=" + good[7:], good[:len(good)-2] + "00", "sha256=zz"} {
		if VerifySignature(secret, body, bad) {
			t.Errorf("accepted %q", bad)
		}
	}
	if VerifySignature([]byte("other"), body, good) || VerifySignature(nil, body, good) {
		t.Error("wrong or empty secret accepted")
	}
	if VerifySignature(secret, append(body, ' '), good) {
		t.Error("tampered body accepted")
	}
}

func TestParseEvent(t *testing.T) {
	ev, err := ParseEvent("push", fixture(t, "hook_push.json"))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Ref != "refs/heads/main" || ev.Branch != "main" || ev.SHA != "6dcb09b5b57875f334f61aebed695e2e4193db5e" || ev.Repo != "acme/app" ||
		!slices.Equal(ev.Files, []string{"README.md", "app/build.gradle", "app/src/Login.kt", "old/Thing.kt"}) {
		t.Errorf("push = %+v", ev)
	}

	ev, err = ParseEvent("check_run", fixture(t, "hook_check_run.json"))
	if err != nil || ev.Name != "unit" || ev.SHA != "ce587453ced02b1526dfb4cb910479d431683101" || ev.Conclusion != "failure" ||
		ev.Action != "completed" || ev.Branch != "br/I-42-remember-email" {
		t.Errorf("check_run = %+v, %v", ev, err)
	}

	ev, err = ParseEvent("workflow_run", fixture(t, "hook_workflow_run.json"))
	if err != nil || ev.Name != "CI" || ev.Branch != "br/I-42-remember-email" || ev.SHA != "acb5820ced9479c074f688cc328bf03f341a511d" || ev.Conclusion != "success" {
		t.Errorf("workflow_run = %+v, %v", ev, err)
	}

	ev, err = ParseEvent("issue_comment", fixture(t, "hook_issue_comment.json"))
	if err != nil || ev.Number != 1610 || ev.Author != "maria" || !ev.IsPR || ev.Body != "Looks good but rename the button" {
		t.Errorf("issue_comment = %+v, %v", ev, err)
	}

	ev, err = ParseEvent("issues", fixture(t, "hook_issues.json"))
	if err != nil || ev.Action != "labeled" || ev.Number != 1612 || ev.Author != "joao" || ev.IsPR ||
		ev.Title != "Dark mode for settings" || ev.Body != "We need dark mode.\n\nThanks" || !slices.Equal(ev.Labels, []string{"enhancement", "barnraise"}) {
		t.Errorf("issues = %+v, %v", ev, err)
	}
	if strings.Contains(ev.Body, "admin") {
		t.Error("hidden comment leaked")
	}

	if ev, err := ParseEvent("ping", []byte(`{"zen":"x","repository":{"full_name":"acme/app"}}`)); err != nil || ev.Type != "ping" {
		t.Errorf("ping = %+v, %v", ev, err)
	}
	if _, err := ParseEvent("deployment", []byte(`{}`)); !errors.Is(err, ErrUnsupportedEvent) {
		t.Errorf("deployment err = %v", err)
	}
	if _, err := ParseEvent("push", []byte(`{not json`)); err == nil {
		t.Error("bad json accepted")
	}
}
