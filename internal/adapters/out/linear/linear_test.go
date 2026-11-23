package linear

import (
	"context"
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

type gqlCall struct {
	Query string
	Vars  map[string]any
}

type route struct {
	keyword string
	h       http.HandlerFunc
}

type fake struct {
	srv    *httptest.Server
	mu     sync.Mutex
	calls  []gqlCall
	routes []route
}

func newFake(t *testing.T) *fake {
	f := &fake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/graphql" {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "lin_api_key" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content type = %q", r.Header.Get("Content-Type"))
		}
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("body: %s", raw)
		}
		f.mu.Lock()
		f.calls = append(f.calls, gqlCall{body.Query, body.Variables})
		var h http.HandlerFunc
		for _, rt := range f.routes {
			if strings.Contains(body.Query, rt.keyword) {
				h = rt.h
				break
			}
		}
		f.mu.Unlock()
		if h == nil {
			t.Errorf("unexpected query %s", body.Query)
			w.Write([]byte(`{"errors":[{"message":"unknown"}]}`))
			return
		}
		h(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) on(keyword string, h http.HandlerFunc) { f.routes = append(f.routes, route{keyword, h}) }

func (f *fake) callsWith(kw string) []gqlCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []gqlCall
	for _, c := range f.calls {
		if strings.Contains(c.Query, kw) {
			out = append(out, c)
		}
	}
	return out
}

func reply(t *testing.T, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}
}

func (f *fake) tracker(t *testing.T, mod func(*Options)) *Tracker {
	o := Options{APIKey: "lin_api_key", TeamKey: "ENG", BaseURL: f.srv.URL + "/graphql", Backoff: func(int) time.Duration { return time.Millisecond }}
	if mod != nil {
		mod(&o)
	}
	tr, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func standard(t *testing.T) *fake {
	f := newFake(t)
	f.on("teams(", reply(t, "teams.json"))
	f.on("workflowStates", reply(t, "states.json"))
	f.on("issueLabelCreate", reply(t, "label_create.json"))
	f.on("issueLabels(", reply(t, "labels.json"))
	f.on("issueCreate", reply(t, "issue_create.json"))
	f.on("issueUpdate", reply(t, "issue_update.json"))
	f.on("commentCreate", reply(t, "comment_create.json"))
	f.on("issue(id:", reply(t, "issue_id.json"))
	return f
}

const teamID = "9cfb482a-81e3-4154-b5b9-2c805e70a02d"

func sampleItem() *domain.Item {
	return &domain.Item{
		ID: "I-42", Title: "Login screen remembers the last email", Body: "Users retype it.", Kind: "bug",
		Priority: domain.P1, Area: "auth", Status: domain.StatusReady,
	}
}

func TestListPaginates(t *testing.T) {
	f := newFake(t)
	f.on("teams(", reply(t, "teams.json"))
	f.on("issues(", func(w http.ResponseWriter, r *http.Request) {
		if len(f.callsWith("issues(")) == 2 {
			reply(t, "issues_page2.json")(w, r)
			return
		}
		reply(t, "issues_page1.json")(w, r)
	})
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	items, err := f.tracker(t, nil).List(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	calls := f.callsWith("issues(")
	if len(calls) != 2 || calls[0].Vars["team"] != teamID || calls[0].Vars["since"] != "2026-11-01T00:00:00Z" ||
		calls[0].Vars["after"] != nil || calls[1].Vars["after"] != "cursor-1" {
		t.Fatalf("calls = %+v", calls)
	}
	if tc := f.callsWith("teams("); len(tc) != 1 || tc[0].Vars["key"] != "ENG" {
		t.Errorf("team lookup = %+v", tc)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	a := items[0]
	if a.Ref != "ENG-17" || a.Number != 17 || a.Status != "In Progress" || a.Author != "Maria" || a.Closed ||
		a.Body != "Users retype it." || !slices.Equal(a.Labels, []string{"barnraise", "area:auth"}) {
		t.Errorf("item 0 = %+v", a)
	}
	if b := items[1]; b.Ref != "ENG-18" || !b.Closed {
		t.Errorf("item 1 = %+v", b)
	}
}

func TestCreateResolvesLabelsAndState(t *testing.T) {
	f := standard(t)
	tr := f.tracker(t, func(o *Options) { o.Columns = map[string]string{"ready": "Todo"} })
	ti, err := tr.Create(context.Background(), sampleItem(), "pt-BR")
	if err != nil {
		t.Fatal(err)
	}
	if ti.Ref != "ENG-17" || ti.URL != "https://linear.app/acme/issue/ENG-17/login-screen" {
		t.Errorf("created = %+v", ti)
	}
	lc := f.callsWith("issueLabelCreate")
	if len(lc) != 1 || lc[0].Vars["name"] != "kind:bug" || lc[0].Vars["team"] != teamID {
		t.Errorf("label create = %+v", lc)
	}
	in := f.callsWith("issueCreate")[0].Vars["input"].(map[string]any)
	if in["teamId"] != teamID || in["title"] != "[I-42] Login screen remembers the last email" || in["priority"] != float64(2) || in["stateId"] != "st-todo" {
		t.Errorf("input = %v", in)
	}
	if !strings.Contains(in["description"].(string), "## Objetivo") {
		t.Errorf("description = %v", in["description"])
	}
	var ids []string
	for _, x := range in["labelIds"].([]any) {
		ids = append(ids, x.(string))
	}
	if !slices.Equal(ids, []string{"lb-barnraise", "lb-area-auth", "lb-kind-bug"}) {
		t.Errorf("labelIds = %v", ids)
	}
}

func TestUpdateMapsStatesAndAddsLabels(t *testing.T) {
	f := standard(t)
	tr := f.tracker(t, func(o *Options) { o.TeamID = teamID; o.Columns = map[string]string{"queued": "In Review"} })
	it := sampleItem()
	it.TrackerRef = "ENG-17"
	ctx := context.Background()
	for _, tc := range []struct {
		status domain.Status
		state  any
	}{
		{domain.StatusQueued, "st-review"},
		{domain.StatusInProgress, "st-prog"},
		{domain.StatusIntegrated, "st-done"},
		{domain.StatusClosed, "st-cancel"},
		{domain.StatusWaitingOwner, nil},
	} {
		it.Status = tc.status
		if err := tr.Update(ctx, it, "en"); err != nil {
			t.Fatal(err)
		}
		calls := f.callsWith("issueUpdate")
		c := calls[len(calls)-1]
		in := c.Vars["input"].(map[string]any)
		if c.Vars["id"] != "2f1b7c8e-0000-4000-8000-000000000017" || in["stateId"] != tc.state {
			t.Errorf("%s: vars = %v", tc.status, c.Vars)
		}
		if in["labelIds"] != nil || len(in["addedLabelIds"].([]any)) != 3 {
			t.Errorf("%s: labels = %v", tc.status, in)
		}
	}
	if len(f.callsWith("teams(")) != 0 {
		t.Error("team looked up although TeamID was given")
	}
	if len(f.callsWith("workflowStates")) != 1 || len(f.callsWith("issueLabels(")) != 1 {
		t.Error("states or labels not cached")
	}
	if ref := f.callsWith("issue(id:")[0].Vars["id"]; ref != "ENG-17" {
		t.Errorf("issue lookup = %v", ref)
	}
	tr = f.tracker(t, func(o *Options) { o.Columns = map[string]string{"queued": "Merge queue"} })
	it.Status = domain.StatusQueued
	if err := tr.Update(ctx, it, "en"); err == nil || !strings.Contains(err.Error(), "Merge queue") {
		t.Errorf("err = %v", err)
	}
}

func TestCommentAndColumns(t *testing.T) {
	f := standard(t)
	tr := f.tracker(t, nil)
	if err := tr.Comment(context.Background(), "ENG-17", "hello"); err != nil {
		t.Fatal(err)
	}
	v := f.callsWith("commentCreate")[0].Vars
	if v["issue"] != "2f1b7c8e-0000-4000-8000-000000000017" || v["body"] != "hello" {
		t.Errorf("vars = %v", v)
	}
	cols, err := tr.Columns(context.Background())
	if err != nil || !slices.Equal(cols, []string{"Backlog", "Todo", "In Progress", "In Review", "Done", "Canceled"}) {
		t.Errorf("cols = %v %v", cols, err)
	}
}

func TestGraphQLErrorAndRetry(t *testing.T) {
	f := newFake(t)
	n := 0
	f.on("teams(", func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write([]byte(`{"data":{"teams":{"nodes":[]}}}`))
	})
	_, err := f.tracker(t, nil).Columns(context.Background())
	if err == nil || !strings.Contains(err.Error(), `team "ENG" not found`) || n != 2 {
		t.Errorf("err = %v, n = %d", err, n)
	}
	f.on("issue(id:", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":null,"errors":[{"message":"Entity not found: Issue","extensions":{"code":"INVALID_INPUT"}}]}`))
	})
	if err := f.tracker(t, func(o *Options) { o.TeamID = teamID }).Comment(context.Background(), "ENG-999", "x"); err == nil || !strings.Contains(err.Error(), "Entity not found") {
		t.Errorf("err = %v", err)
	}
	if _, err := New(Options{}); err == nil {
		t.Error("no team accepted")
	}
}
