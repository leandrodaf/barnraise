package gitlab

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
	"github.com/leandrodaf/barnraise/internal/ports"
)

type call struct {
	Method, Path, Query string
	Body                map[string]any
}

type fake struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	calls  []call
	routes map[string]http.HandlerFunc
}

const prefix = "/api/v4/projects/acme%2Fapp"

func newFake(t *testing.T) *fake {
	f := &fake{t: t, routes: map[string]http.HandlerFunc{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "glpat-tok" {
			t.Errorf("%s %s: token = %q", r.Method, r.URL.Path, r.Header.Get("PRIVATE-TOKEN"))
		}
		// The project path must stay escaped on the wire.
		path := r.URL.EscapedPath()
		if !strings.HasPrefix(path, prefix) {
			t.Errorf("path %s is not under %s", path, prefix)
		}
		path = strings.TrimPrefix(path, prefix)
		raw, _ := io.ReadAll(r.Body)
		c := call{Method: r.Method, Path: path, Query: r.URL.RawQuery}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &c.Body); err != nil {
				t.Errorf("body not JSON: %s", raw)
			}
		}
		f.mu.Lock()
		f.calls = append(f.calls, c)
		h := f.routes[r.Method+" "+path]
		f.mu.Unlock()
		if h == nil {
			t.Errorf("unexpected %s %s", r.Method, path)
			http.Error(w, `{"message":"404 Not found"}`, 404)
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

func (f *fake) client(t *testing.T, mod func(*Options)) *Client {
	o := Options{BaseURL: f.srv.URL + "/api/v4", Project: "acme/app", Token: "glpat-tok", Backoff: func(int) time.Duration { return time.Millisecond }}
	if mod != nil {
		mod(&o)
	}
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func sampleItem() *domain.Item {
	return &domain.Item{
		ID: "I-42", Title: "Login screen remembers the last email", Body: "Users retype it.", Kind: "bug",
		Priority: domain.P1, Area: "auth", Status: domain.StatusInProgress, TrackerRef: "#17",
		Criteria: []domain.Criterion{{ID: "C1", Text: "WHEN the app opens, THE SYSTEM SHALL fill the last email", Check: "LoginTest"}},
	}
}

func TestListPaginatesAndSanitizes(t *testing.T) {
	f := newFake(t)
	f.on("GET /issues", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("page") == "2" {
			reply(t, 200, "issues_page2.json")(w, r)
			return
		}
		if q.Get("updated_after") != "2026-11-01T00:00:00Z" || q.Get("per_page") != "100" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		w.Header().Set("X-Next-Page", "2")
		reply(t, 200, "issues_page1.json")(w, r)
	})
	tr := f.client(t, func(o *Options) { o.Columns = map[string]string{"in_progress": "Doing"} }).Tracker()
	items, err := tr.List(context.Background(), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	if a := items[0]; a.Ref != "#17" || a.Author != "maria" || a.Status != "in_progress" || a.Body != "Fix it." || a.Closed {
		t.Errorf("item 0 = %+v", a)
	}
	if b := items[1]; b.Ref != "#18" || !b.Closed || b.Status != "in_progress" {
		t.Errorf("item 1 = %+v", b)
	}
}

func TestCreateAndUpdate(t *testing.T) {
	f := newFake(t)
	f.on("POST /issues", reply(t, 201, "issue.json"))
	f.on("GET /issues/17", reply(t, 200, "issue.json"))
	f.on("PUT /issues/17", reply(t, 200, "issue.json"))
	tr := f.client(t, func(o *Options) { o.Columns = map[string]string{"in_progress": "Doing", "queued": "Review"} }).Tracker()
	ctx := context.Background()
	ti, err := tr.Create(ctx, sampleItem(), "pt-BR")
	if err != nil {
		t.Fatal(err)
	}
	if ti.Ref != "#17" || ti.URL != "https://gitlab.com/acme/app/-/issues/17" {
		t.Errorf("created = %+v", ti)
	}
	cb := f.callsTo("POST", "/issues")[0].Body
	if cb["title"] != "[I-42] Login screen remembers the last email" || cb["labels"] != "barnraise,area:auth,kind:bug,P1,Doing" ||
		!strings.Contains(cb["description"].(string), "## O que não pode mudar") {
		t.Errorf("create body = %v", cb)
	}

	it := sampleItem()
	it.Status = domain.StatusQueued
	if err := tr.Update(ctx, it, "en"); err != nil {
		t.Fatal(err)
	}
	ub := f.callsTo("PUT", "/issues/17")[0].Body
	labels := strings.Split(ub["labels"].(string), ",")
	slices.Sort(labels)
	if want := []string{"P1", "Review", "area:auth", "barnraise", "kind:bug", "needs-design"}; !slices.Equal(labels, want) {
		t.Errorf("labels = %v want %v", labels, want)
	}
	if _, ok := ub["state_event"]; ok {
		t.Errorf("open item changed state: %v", ub)
	}
	it.Status = domain.StatusClosed
	if err := tr.Update(ctx, it, "en"); err != nil {
		t.Fatal(err)
	}
	if ub := f.callsTo("PUT", "/issues/17")[1].Body; ub["state_event"] != "close" || !strings.Contains(ub["labels"].(string), "status:closed") {
		t.Errorf("close body = %v", ub)
	}
}

func TestCommentAndColumns(t *testing.T) {
	f := newFake(t)
	f.on("POST /issues/17/notes", reply(t, 201, ""))
	f.on("GET /boards", reply(t, 200, "boards.json"))
	tr := f.client(t, nil).Tracker()
	if err := tr.Comment(context.Background(), "#17", "hi"); err != nil {
		t.Fatal(err)
	}
	if b := f.callsTo("POST", "/issues/17/notes")[0].Body; b["body"] != "hi" {
		t.Errorf("note = %v", b)
	}
	cols, err := tr.Columns(context.Background())
	if err != nil || !slices.Equal(cols, []string{"To do", "Doing", "Review"}) {
		t.Errorf("cols = %v, %v", cols, err)
	}
}

func TestOpenDraftMR(t *testing.T) {
	f := newFake(t)
	existing := true
	f.on("GET /merge_requests", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("source_branch") != "br/I-42-remember-email" || r.URL.Query().Get("state") != "opened" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		if !existing {
			w.Write([]byte("[]"))
			return
		}
		b, _ := os.ReadFile("testdata/mr.json")
		w.Write([]byte("[" + string(b) + "]"))
	})
	f.on("POST /merge_requests", reply(t, 201, "mr.json"))
	h := f.client(t, nil).CodeHost()
	pr, err := h.OpenDraftPR(context.Background(), "br/I-42-remember-email", "main", "[I-42] Remember last email", "body")
	if err != nil {
		t.Fatal(err)
	}
	want := ports.PR{Number: 5, URL: "https://gitlab.com/acme/app/-/merge_requests/5", State: "opened", Draft: true, Head: "br/I-42-remember-email"}
	if pr != want || len(f.callsTo("POST", "/merge_requests")) != 0 {
		t.Errorf("pr = %+v", pr)
	}
	existing = false
	if _, err := h.OpenDraftPR(context.Background(), "br/I-42-remember-email", "main", "[I-42] Remember last email", "body"); err != nil {
		t.Fatal(err)
	}
	b := f.callsTo("POST", "/merge_requests")[0].Body
	if b["title"] != "Draft: [I-42] Remember last email" || b["source_branch"] != "br/I-42-remember-email" || b["target_branch"] != "main" || b["description"] != "body" {
		t.Errorf("create = %v", b)
	}
}

func TestMarkReadyUpdateReviewClose(t *testing.T) {
	f := newFake(t)
	f.on("GET /merge_requests/5", reply(t, 200, "mr.json"))
	f.on("PUT /merge_requests/5", reply(t, 200, "mr.json"))
	f.on("POST /merge_requests/5/notes", reply(t, 201, ""))
	f.on("POST /merge_requests/5/approve", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"message":"401 Unauthorized"}`))
	})
	f.on("POST /merge_requests/5/unapprove", reply(t, 404, ""))
	h := f.client(t, nil).CodeHost()
	ctx := context.Background()
	pr := ports.PR{Number: 5, Draft: true}
	if err := h.MarkReady(ctx, pr); err != nil {
		t.Fatal(err)
	}
	if b := f.callsTo("PUT", "/merge_requests/5")[0].Body; b["title"] != "[I-42] Remember last email" {
		t.Errorf("ready = %v", b)
	}
	if err := h.UpdatePR(ctx, pr, "[I-42] New name", "desc"); err != nil {
		t.Fatal(err)
	}
	if b := f.callsTo("PUT", "/merge_requests/5")[1].Body; b["title"] != "Draft: [I-42] New name" || b["description"] != "desc" {
		t.Errorf("update = %v", b)
	}
	if err := h.Review(ctx, pr, true, "Criteria met."); err != nil {
		t.Fatal(err)
	}
	if err := h.Review(ctx, pr, false, "C1 fails."); err != nil {
		t.Fatal(err)
	}
	notes := f.callsTo("POST", "/merge_requests/5/notes")
	if len(notes) != 2 || notes[0].Body["body"] != "**Approved**\n\nCriteria met." || notes[1].Body["body"] != "**Changes requested**\n\nC1 fails." {
		t.Errorf("notes = %+v", notes)
	}
	if err := h.Close(ctx, pr, "Abandoned."); err != nil {
		t.Fatal(err)
	}
	if b := f.callsTo("PUT", "/merge_requests/5")[2].Body; b["state_event"] != "close" {
		t.Errorf("close = %v", b)
	}
}

func TestMergeStatusPipeline(t *testing.T) {
	f := newFake(t)
	sha := "6dcb09b5b57875f334f61aebed695e2e4193db5e"
	f.on("PUT /merge_requests/5/merge", reply(t, 200, "mr_merged.json"))
	f.on("POST /statuses/"+sha, reply(t, 201, ""))
	f.on("POST /pipeline", reply(t, 201, ""))
	rebasing := 2
	f.on("PUT /merge_requests/5/rebase", reply(t, 202, ""))
	f.on("GET /merge_requests/5", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("include_rebase_in_progress") != "true" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		rebasing--
		w.Write([]byte(`{"iid":5,"rebase_in_progress":` + map[bool]string{true: "true", false: "false"}[rebasing > 0] + `,"merge_error":null}`))
	})
	var slept []time.Duration
	h := f.client(t, func(o *Options) {
		o.Sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	}).CodeHost()
	ctx := context.Background()
	pr := ports.PR{Number: 5}
	got, err := h.Merge(ctx, pr, "squash")
	if err != nil || got != "4d1f2a0b9c8e7d6f5a4b3c2d1e0f9a8b7c6d5e4f" {
		t.Fatalf("merge = %s, %v", got, err)
	}
	if b := f.callsTo("PUT", "/merge_requests/5/merge")[0].Body; b["squash"] != true {
		t.Errorf("merge body = %v", b)
	}
	if _, err := h.Merge(ctx, pr, "rebase"); err != nil {
		t.Fatal(err)
	}
	if len(f.callsTo("PUT", "/merge_requests/5/rebase")) != 1 || len(slept) != 1 {
		t.Errorf("rebase polling: slept=%v", slept)
	}
	if b := f.callsTo("PUT", "/merge_requests/5/merge")[1].Body; b["squash"] != false {
		t.Errorf("rebase merge body = %v", b)
	}
	if err := h.ReportCheck(ctx, sha, "unit", false, "2 failed"); err != nil {
		t.Fatal(err)
	}
	if b := f.callsTo("POST", "/statuses/"+sha)[0].Body; b["state"] != "failed" || b["name"] != "barnraise/unit" || b["description"] != "2 failed" {
		t.Errorf("status = %v", b)
	}
	if err := h.DispatchWorkflow(ctx, "maestro", "br/I-42", map[string]string{"ITEM": "I-42"}); err != nil {
		t.Fatal(err)
	}
	b := f.callsTo("POST", "/pipeline")[0].Body
	vars, _ := json.Marshal(b["variables"])
	if b["ref"] != "br/I-42" || string(vars) != `[{"key":"BARNRAISE_WORKFLOW","value":"maestro"},{"key":"ITEM","value":"I-42"}]` {
		t.Errorf("pipeline = %v %s", b, vars)
	}
}

func TestRetryOn502(t *testing.T) {
	f := newFake(t)
	n := 0
	f.on("GET /boards", func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(502)
			return
		}
		w.Write([]byte("[]"))
	})
	cols, err := f.client(t, nil).Tracker().Columns(context.Background())
	if err != nil || n != 2 || len(cols) != 9 {
		t.Errorf("cols=%v err=%v n=%d", cols, err, n)
	}
}

func TestVerifyToken(t *testing.T) {
	if !VerifyToken("s3cret", "s3cret") || VerifyToken("s3cret", "s3cre") || VerifyToken("", "") || VerifyToken("s3cret", "") {
		t.Error("VerifyToken")
	}
}

func TestStripDraft(t *testing.T) {
	for in, want := range map[string]string{"Draft: X": "X", "[Draft] X": "X", "(Draft) X": "X", "WIP: X": "X", "draft: X": "X", "X": "X"} {
		if got := stripDraft(in); got != want {
			t.Errorf("stripDraft(%q) = %q", in, got)
		}
	}
	if _, err := New(Options{}); err == nil {
		t.Error("empty project accepted")
	}
}

func TestHostChecks(t *testing.T) {
	f := newFake(t)
	sha := "6dcb09b5b57875f334f61aebed695e2e4193db5e"
	f.on("GET /repository/commits/"+sha+"/statuses", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"name":"unit","status":"success"},
			{"name":"unit","status":"failed"},
			{"name":"barnraise/lint","status":"failed"},
			{"name":"e2e","status":"running"},
			{"name":"sast","status":"failed"},
			{"name":"deploy","status":"manual"}]`))
	})
	c, err := f.client(t, nil).CodeHost().Checks(context.Background(), sha)
	if err != nil {
		t.Fatal(err)
	}
	// The latest run of a job counts, and Barnraise's own status is left out.
	if c.Total != 4 || c.Pending != 1 || strings.Join(c.Failed, ",") != "sast" || c.Done() {
		t.Fatalf("checks %+v", c)
	}
}
