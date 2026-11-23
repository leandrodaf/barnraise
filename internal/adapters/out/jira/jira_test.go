package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
)

type call struct {
	Method, Path, Query string
	Body                map[string]any
}

type fake struct {
	srv    *httptest.Server
	mu     sync.Mutex
	calls  []call
	routes map[string]http.HandlerFunc
}

func newFake(t *testing.T) *fake {
	f := &fake{routes: map[string]http.HandlerFunc{}}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("bot@acme.com:atl-tok"))
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != wantAuth {
			t.Errorf("%s %s: auth = %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		raw, _ := io.ReadAll(r.Body)
		c := call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &c.Body); err != nil {
				t.Errorf("body not JSON: %s", raw)
			}
		}
		f.mu.Lock()
		f.calls = append(f.calls, c)
		h := f.routes[r.Method+" "+r.URL.Path]
		f.mu.Unlock()
		if h == nil {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.Error(w, "{}", 404)
			return
		}
		h(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) on(k string, h http.HandlerFunc) { f.routes[k] = h }

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

func reply(t *testing.T, status int, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if name != "" {
			b, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			w.Write(b)
		}
	}
}

func (f *fake) tracker(t *testing.T, mod func(*Options)) *Tracker {
	o := Options{BaseURL: f.srv.URL, Email: "bot@acme.com", Token: "atl-tok", ProjectKey: "APP",
		Backoff: func(int) time.Duration { return time.Millisecond }}
	if mod != nil {
		mod(&o)
	}
	tr, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func sampleItem() *domain.Item {
	return &domain.Item{
		ID: "I-42", Title: "Login screen remembers the last email", Body: "Users retype it.", Kind: "bug",
		Priority: domain.P1, Area: "auth", Status: domain.StatusReady,
		Criteria: []domain.Criterion{{ID: "C1", Text: "WHEN the app opens, THE SYSTEM SHALL fill the last email", Check: "LoginTest"}},
		Preserve: []string{"Password is never stored"},
	}
}

func TestListPaginatesFiltersAndSanitizes(t *testing.T) {
	f := newFake(t)
	f.on("POST /rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		if len(f.callsTo("POST", "/rest/api/3/search/jql")) == 2 {
			reply(t, 200, "search_page2.json")(w, r)
			return
		}
		reply(t, 200, "search_page1.json")(w, r)
	})
	since := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	items, err := f.tracker(t, nil).List(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	calls := f.callsTo("POST", "/rest/api/3/search/jql")
	if len(calls) != 2 {
		t.Fatalf("calls = %d", len(calls))
	}
	if jql := calls[0].Body["jql"]; jql != `project = "APP" AND updated >= "2026-11-09 08:00" ORDER BY updated ASC` {
		t.Errorf("jql = %v", jql)
	}
	if calls[0].Body["nextPageToken"] != nil || calls[1].Body["nextPageToken"] != "CAEaAggB" {
		t.Errorf("tokens = %v / %v", calls[0].Body["nextPageToken"], calls[1].Body["nextPageToken"])
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	a := items[0]
	if a.Ref != "APP-17" || a.Number != 17 || a.Status != "In Progress" || a.Author != "Maria Silva" || a.Closed ||
		a.URL != f.srv.URL+"/browse/APP-17" || !a.Updated.Equal(time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC)) {
		t.Errorf("item 0 = %+v", a)
	}
	if a.Body != "## Goal\n\nUsers retype it.\n\n- first\n- second" {
		t.Errorf("body = %q", a.Body)
	}
	if b := items[1]; b.Ref != "APP-18" || !b.Closed || b.Author != "João" {
		t.Errorf("item 1 = %+v", b)
	}
}

func TestCreateWithADFAndTransition(t *testing.T) {
	f := newFake(t)
	f.on("POST /rest/api/3/issue", reply(t, 201, "issue_created.json"))
	f.on("GET /rest/api/3/issue/APP-17/transitions", reply(t, 200, "transitions.json"))
	f.on("POST /rest/api/3/issue/APP-17/transitions", reply(t, 204, ""))
	tr := f.tracker(t, func(o *Options) {
		o.IssueType = "Bug"
		o.Columns = map[string]string{"ready": "To Do", "in_progress": "In Progress"}
	})
	it := sampleItem()
	it.Status = domain.StatusInProgress
	ti, err := tr.Create(context.Background(), it, "pt-BR")
	if err != nil {
		t.Fatal(err)
	}
	if ti.Ref != "APP-17" || ti.URL != f.srv.URL+"/browse/APP-17" {
		t.Errorf("created = %+v", ti)
	}
	fields := f.callsTo("POST", "/rest/api/3/issue")[0].Body["fields"].(map[string]any)
	if fields["summary"] != "[I-42] Login screen remembers the last email" ||
		fields["project"].(map[string]any)["key"] != "APP" || fields["issuetype"].(map[string]any)["name"] != "Bug" {
		t.Errorf("fields = %v", fields)
	}
	raw, _ := json.Marshal(fields["description"])
	var doc Block
	json.Unmarshal(raw, &doc)
	if doc.Type != "doc" || doc.Version != 1 || doc.Content[0].Type != "heading" || doc.Content[0].Content[0].Text != "Objetivo" {
		t.Errorf("doc = %s", raw)
	}
	text := PlainText(&doc)
	for _, want := range []string{"## Critérios", "- ☐ C1: WHEN the app opens", "## O que não pode mudar", "- Password is never stored", "## Área"} {
		if !strings.Contains(text, want) {
			t.Errorf("description misses %q:\n%s", want, text)
		}
	}
	tb := f.callsTo("POST", "/rest/api/3/issue/APP-17/transitions")[0].Body
	if tb["transition"].(map[string]any)["id"] != "21" {
		t.Errorf("transition = %v", tb)
	}
}

func TestUpdateKeepsLabelsAndUsesDoneFallback(t *testing.T) {
	f := newFake(t)
	f.on("GET /rest/api/3/issue/APP-17", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("fields") != "labels,status" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		reply(t, 200, "issue_current.json")(w, r)
	})
	f.on("PUT /rest/api/3/issue/APP-17", reply(t, 204, ""))
	f.on("GET /rest/api/3/issue/APP-17/transitions", reply(t, 200, "transitions.json"))
	f.on("POST /rest/api/3/issue/APP-17/transitions", reply(t, 204, ""))
	it := sampleItem()
	it.TrackerRef = "APP-17"
	it.Status = domain.StatusIntegrated
	if err := f.tracker(t, nil).Update(context.Background(), it, "en"); err != nil {
		t.Fatal(err)
	}
	fields := f.callsTo("PUT", "/rest/api/3/issue/APP-17")[0].Body["fields"].(map[string]any)
	var labels []string
	for _, l := range fields["labels"].([]any) {
		labels = append(labels, l.(string))
	}
	slices.Sort(labels)
	if want := []string{"P1", "area:auth", "barnraise", "customer-reported", "kind:bug", "status:integrated"}; !slices.Equal(labels, want) {
		t.Errorf("labels = %v", labels)
	}
	if tb := f.callsTo("POST", "/rest/api/3/issue/APP-17/transitions")[0].Body; tb["transition"].(map[string]any)["id"] != "31" {
		t.Errorf("transition = %v", tb)
	}
}

func TestUpdateUnmappedOrUnreachable(t *testing.T) {
	f := newFake(t)
	f.on("GET /rest/api/3/issue/APP-17", reply(t, 200, "issue_current.json"))
	f.on("PUT /rest/api/3/issue/APP-17", reply(t, 204, ""))
	f.on("GET /rest/api/3/issue/APP-17/transitions", reply(t, 200, "transitions.json"))
	it := sampleItem()
	it.TrackerRef = "APP-17"
	it.Status = domain.StatusWaitingOwner
	if err := f.tracker(t, nil).Update(context.Background(), it, "en"); err != nil {
		t.Fatalf("unmapped status: %v", err)
	}
	if n := len(f.callsTo("POST", "/rest/api/3/issue/APP-17/transitions")); n != 0 {
		t.Errorf("transitions posted: %d", n)
	}
	tr := f.tracker(t, func(o *Options) { o.Columns = map[string]string{"waiting_owner": "Waiting for owner"} })
	if err := tr.Update(context.Background(), it, "en"); err == nil || !strings.Contains(err.Error(), "Waiting for owner") {
		t.Errorf("err = %v", err)
	}
	// Already in the target status: no transition lookup.
	before := len(f.callsTo("GET", "/rest/api/3/issue/APP-17/transitions"))
	tr = f.tracker(t, func(o *Options) { o.Columns = map[string]string{"waiting_owner": "to do"} })
	if err := tr.Update(context.Background(), it, "en"); err != nil {
		t.Fatal(err)
	}
	if len(f.callsTo("GET", "/rest/api/3/issue/APP-17/transitions")) != before {
		t.Error("looked up transitions for the current status")
	}
}

func TestCommentAndColumns(t *testing.T) {
	f := newFake(t)
	f.on("POST /rest/api/3/issue/APP-17/comment", reply(t, 201, ""))
	f.on("GET /rest/api/3/project/APP/statuses", reply(t, 200, "statuses.json"))
	tr := f.tracker(t, nil)
	if err := tr.Comment(context.Background(), "APP-17", "First line\nsecond\n\nNew paragraph"); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(f.callsTo("POST", "/rest/api/3/issue/APP-17/comment")[0].Body["body"])
	want := `{"content":[{"content":[{"text":"First line","type":"text"},{"type":"hardBreak"},{"text":"second","type":"text"}],"type":"paragraph"},{"content":[{"text":"New paragraph","type":"text"}],"type":"paragraph"}],"type":"doc","version":1}`
	if string(raw) != want {
		t.Errorf("comment = %s", raw)
	}
	cols, err := tr.Columns(context.Background())
	if err != nil || !slices.Equal(cols, []string{"To Do", "In Progress", "Done"}) {
		t.Errorf("cols = %v %v", cols, err)
	}
}

func TestRetry429RetryAfter(t *testing.T) {
	f := newFake(t)
	n := 0
	f.on("POST /rest/api/3/issue/APP-1/comment", func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		w.WriteHeader(201)
	})
	var slept []time.Duration
	tr := f.tracker(t, func(o *Options) {
		o.Sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	})
	if err := tr.Comment(context.Background(), "APP-1", "x"); err != nil {
		t.Fatal(err)
	}
	if n != 2 || len(slept) != 1 || slept[0] != time.Second {
		t.Errorf("n=%d slept=%v", n, slept)
	}
}

func TestNewValidates(t *testing.T) {
	if _, err := New(Options{ProjectKey: "A"}); err == nil {
		t.Error("missing base URL accepted")
	}
	if _, err := New(Options{BaseURL: "https://x"}); err == nil {
		t.Error("missing project accepted")
	}
	tr, _ := New(Options{BaseURL: "https://x/", ProjectKey: "A"})
	if tr.opts.IssueType != "Task" || tr.Name() != "jira" {
		t.Error("defaults")
	}
}
