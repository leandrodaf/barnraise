package otlp

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/plugin"
	"github.com/leandrodaf/barnraise/internal/domain"
)

var t0 = time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)

func ev(id, typ, subject string, at time.Time, p domain.Payload) domain.Event {
	return domain.NewEvent(id, typ, "shop", subject, "desk", "barnraise", at, p)
}

// lifecycle is one item from creation to integration, with a ledger entry
// and a failed check on the way.
func lifecycle() []domain.Event {
	t1, t3, t4, t5 := t0.Add(time.Minute), t0.Add(10*time.Minute), t0.Add(12*time.Minute), t0.Add(30*time.Minute)
	item := func(step string, started time.Time, status domain.Status) *domain.Item {
		return &domain.Item{ID: "I-1", Kind: "feature", Priority: 1, Area: "api", CreatedAt: t0, Step: step, StepStartedAt: started, Status: status}
	}
	return []domain.Event{
		ev("e1", domain.EvItemCreated, "I-1", t0, domain.Payload{Item: item("", time.Time{}, domain.StatusReady)}),
		ev("e2", domain.EvItemClaimed, "I-1", t1, domain.Payload{Item: item("implement", t1, domain.StatusInProgress)}),
		ev("e3", domain.EvAgentUsage, "I-1", t1.Add(5*time.Minute), domain.Payload{Usage: &domain.TokenUsage{Agent: "claude-1", Account: "work", Provider: "claude", Model: "claude-opus",
			Role: "implementer", ItemID: "I-1", Step: "implement", Input: 1200, Output: 300, CacheRead: 50, CostUSD: 0.42, Duration: 4 * time.Minute}}),
		ev("e4", domain.EvItemStepFinished, "I-1", t3, domain.Payload{Item: item("check", t3, domain.StatusInProgress), Extra: map[string]any{"step": "implement", "outcome": "done"}}),
		ev("e5", domain.EvCheckCompleted, "I-1", t4, domain.Payload{Check: &domain.CheckResult{Check: "main", Passed: false, RunsOn: "local", Duration: 90 * time.Second, Summary: "2 tests failed"}}),
		ev("e6", domain.EvItemStepFinished, "I-1", t4, domain.Payload{Item: item("implement", t4, domain.StatusInProgress), Note: "check main failed", Extra: map[string]any{"step": "check", "outcome": "fail"}}),
		ev("e7", domain.EvItemIntegrated, "I-1", t5, domain.Payload{Item: item("", time.Time{}, domain.StatusIntegrated)}),
		ev("e8", domain.EvAgentUsage, "", t5, domain.Payload{Usage: &domain.TokenUsage{Role: "analyst", Model: "haiku", Input: 10, Duration: time.Second}}),
		ev("e9", domain.EvItemUpdated, "I-2", t5, domain.Payload{}),
	}
}

func attr(s Span, key string) any {
	for _, a := range s.Attrs {
		if a.Key == key {
			return a.Value
		}
	}
	return nil
}

func TestTracerBuildsTheItemTrace(t *testing.T) {
	var spans []Span
	tr := NewTracer(func(s Span) { spans = append(spans, s) })
	for _, e := range lifecycle() {
		tr.Handle(e)
	}
	if tr.Tracked() != 0 {
		t.Fatal("an integrated item leaves no state behind")
	}
	byName := map[string]Span{}
	for _, s := range spans {
		byName[s.Name] = s
	}
	if len(spans) != 6 {
		t.Fatalf("spans: %v", byName)
	}
	trace := TraceID("shop", "I-1")
	root, impl, check, agent, chk := byName["item I-1"], byName["step implement"], byName["step check"], byName["agent implementer"], byName["check main"]
	for _, s := range []Span{root, impl, check, agent, chk} {
		if s.TraceID != trace {
			t.Fatalf("%s is in another trace", s.Name)
		}
	}
	if root.Parent != ([8]byte{}) || !root.Start.Equal(t0) || !root.End.Equal(t0.Add(30*time.Minute)) || attr(root, "barnraise.item.priority") != "P1" {
		t.Fatalf("root %+v", root)
	}
	if impl.Parent != root.SpanID || !impl.Start.Equal(t0.Add(time.Minute)) || !impl.End.Equal(t0.Add(10*time.Minute)) || impl.Error || attr(impl, "barnraise.start_unknown") != nil {
		t.Fatalf("implement %+v", impl)
	}
	if agent.Parent != impl.SpanID || attr(agent, "gen_ai.usage.input_tokens") != int64(1200) || attr(agent, "barnraise.cost_usd") != 0.42 ||
		!agent.Start.Equal(t0.Add(2*time.Minute)) || attr(agent, "gen_ai.request.model") != "claude-opus" {
		t.Fatalf("the ledger entry belongs to its step: %+v", agent)
	}
	if chk.Parent != check.SpanID || !chk.Error || chk.Message != "2 tests failed" || attr(chk, "barnraise.check.passed") != false {
		t.Fatalf("check %+v", chk)
	}
	if !check.Error || check.Message != "check main failed" || attr(check, "barnraise.outcome") != "fail" {
		t.Fatalf("failed step %+v", check)
	}
	triage := byName["agent analyst"]
	if triage.TraceID == trace || triage.Parent != ([8]byte{}) {
		t.Fatalf("a ledger entry without an item is its own trace: %+v", triage)
	}

	// A plugin that starts in the middle of a step still closes it.
	spans = nil
	tr = NewTracer(func(s Span) { spans = append(spans, s) })
	tr.Handle(lifecycle()[3])
	if len(spans) != 1 || attr(spans[0], "barnraise.start_unknown") != true {
		t.Fatalf("%+v", spans)
	}
	// Garbage is ignored.
	tr.Handle(domain.Event{Type: domain.EvItemStepFinished, Data: []byte("{")})
}

// collector is a fake OTLP/HTTP receiver.
type collector struct {
	mu       sync.Mutex
	requests []map[string]any
	headers  []http.Header
	fail     int
}

func (c *collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r.URL.Path != "/v1/traces" || r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "wrong path or type", 404)
		return
	}
	if c.fail > 0 {
		c.fail--
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	var m map[string]any
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	c.requests = append(c.requests, m)
	c.headers = append(c.headers, r.Header.Clone())
	_, _ = w.Write([]byte("{}"))
}

func (c *collector) spans() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	for _, r := range c.requests {
		for _, rs := range r["resourceSpans"].([]any) {
			for _, ss := range rs.(map[string]any)["scopeSpans"].([]any) {
				for _, s := range ss.(map[string]any)["spans"].([]any) {
					out = append(out, s.(map[string]any))
				}
			}
		}
	}
	return out
}

func newCollector(t *testing.T) (*collector, *httptest.Server) {
	c := &collector{}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	return c, srv
}

func TestExporterWireFormatAndRetry(t *testing.T) {
	c, srv := newCollector(t)
	c.fail = 1
	x := &Exporter{URL: srv.URL + "/v1/traces", Headers: map[string]string{"X-Api-Key": "k"}, Scope: "barnraise", Version: "1.0",
		Resource: []Attr{{"service.name", "barnraise"}}, MaxQueue: 3}
	for i := 0; i < 4; i++ {
		x.Add(Span{TraceID: TraceID("shop", "I-1"), SpanID: ItemSpanID("shop", "I-1"), Name: "s", Start: t0, End: t0.Add(time.Second),
			Attrs: []Attr{{"n", 5}, {"f", 1.5}, {"b", true}, {"s", "x"}, {"empty", ""}, {"i64", int64(7)}, {"other", time.Second}}, Error: i == 0, Message: "bad"})
	}
	if x.Pending() != 3 || x.Dropped() != 1 || x.Dropped() != 0 {
		t.Fatal("the queue is bounded")
	}
	if err := x.Flush(context.Background()); err == nil || !strings.Contains(err.Error(), "answered 503") || x.Pending() != 3 {
		t.Fatalf("a refused batch stays queued: %v", err)
	}
	if err := x.Flush(context.Background()); err != nil || x.Pending() != 0 {
		t.Fatal(err)
	}
	if err := x.Flush(context.Background()); err != nil {
		t.Fatal("an empty flush sends nothing")
	}
	spans := c.spans()
	if len(spans) != 3 || len(c.requests) != 1 || c.headers[0].Get("X-Api-Key") != "k" {
		t.Fatalf("%d spans, %d requests", len(spans), len(c.requests))
	}
	s := spans[0]
	if id, _ := s["traceId"].(string); len(id) != 32 || s["startTimeUnixNano"] != "1795514400000000000" || s["kind"] != float64(1) {
		t.Fatalf("span %v", s)
	}
	attrs := map[string]any{}
	for _, a := range s["attributes"].([]any) {
		kv := a.(map[string]any)
		attrs[kv["key"].(string)] = kv["value"]
	}
	if attrs["n"].(map[string]any)["intValue"] != "5" || attrs["f"].(map[string]any)["doubleValue"] != 1.5 || attrs["b"].(map[string]any)["boolValue"] != true ||
		attrs["empty"] != nil || attrs["other"].(map[string]any)["stringValue"] != "1s" {
		t.Fatalf("attributes %v", attrs)
	}
	scope := c.requests[0]["resourceSpans"].([]any)[0].(map[string]any)["scopeSpans"].([]any)[0].(map[string]any)["scope"].(map[string]any)
	if scope["name"] != "barnraise" || scope["version"] != "1.0" {
		t.Fatalf("scope %v", scope)
	}
	x.URL = "http://127.0.0.1:1/v1/traces"
	x.Add(Span{Name: "lost"})
	if err := x.Flush(context.Background()); err == nil {
		t.Fatal("unreachable collector")
	}
}

func TestResolve(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	s, err := Config{}.Resolve(get)
	if err != nil || s.URL != "http://localhost:4318/v1/traces" || s.Service != "barnraise" || s.Interval != 5*time.Second || s.Batch != 256 {
		t.Fatalf("%+v %v", s, err)
	}
	env["OTEL_EXPORTER_OTLP_ENDPOINT"] = "https://otel.example:4318/"
	env["OTEL_EXPORTER_OTLP_HEADERS"] = "Authorization=Bearer%20abc, x-team = shop,broken"
	s, _ = Config{Headers: map[string]string{"x-team": "web"}, Interval: "1s", Batch: 10, Timeout: "2s"}.Resolve(get)
	if s.URL != "https://otel.example:4318/v1/traces" || s.Headers["Authorization"] != "Bearer abc" || s.Headers["x-team"] != "web" || s.Interval != time.Second || s.Batch != 10 || s.Timeout != 2*time.Second {
		t.Fatalf("%+v", s)
	}
	env["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"] = "https://traces.example/custom"
	if s, _ = (Config{}).Resolve(get); s.URL != "https://traces.example/custom" {
		t.Fatal(s.URL)
	}
	if s, _ = (Config{Endpoint: "http://c:4318/v1/traces"}).Resolve(get); s.URL != "http://c:4318/v1/traces" {
		t.Fatal(s.URL)
	}
	for _, bad := range []Config{{Endpoint: "ftp://x"}, {Endpoint: "::"}, {Interval: "soon"}, {Timeout: "0s"}} {
		if _, err := bad.Resolve(get); err == nil {
			t.Fatalf("%+v should fail", bad)
		}
	}
}

func rpc(t *testing.T, w io.Writer, id int, method string, params any) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if _, err := w.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

func TestServeOverPipes(t *testing.T) {
	c, srv := newCollector(t)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- Serve(context.Background(), inR, outW, ServeOptions{Version: "9", Getenv: func(string) string { return "" }})
		outW.Close()
	}()
	replies := make(chan map[string]any, 64)
	go func() {
		dec := json.NewDecoder(outR)
		for {
			var m map[string]any
			if dec.Decode(&m) != nil {
				close(replies)
				return
			}
			if _, ok := m["id"]; ok {
				replies <- m
			}
		}
	}()
	next := func() map[string]any {
		select {
		case m := <-replies:
			return m
		case <-time.After(5 * time.Second):
			t.Fatal("no reply")
			return nil
		}
	}
	rpc(t, inW, 1, "hook.event", map[string]any{})
	if m := next(); m["error"] == nil {
		t.Fatal("hook.event before initialize")
	}
	rpc(t, inW, 2, "initialize", map[string]any{"protocol_version": 1, "barnraise_version": "1.2.3", "project": "shop", "options": map[string]any{"endpoint": "nope://x"}})
	if m := next(); !strings.Contains(m["error"].(map[string]any)["message"].(string), "not an http(s) URL") {
		t.Fatalf("bad endpoint %v", m)
	}
	rpc(t, inW, 3, "initialize", map[string]any{"protocol_version": 1, "barnraise_version": "1.2.3", "project": "shop", "options": map[string]any{"endpoint": srv.URL, "interval": "1h", "batch": 3}})
	if m := next(); m["result"].(map[string]any)["kinds"].([]any)[0] != "hook" || m["result"].(map[string]any)["protocol_version"] != float64(1) {
		t.Fatalf("initialize %v", m)
	}
	_, _ = inW.Write([]byte(`{"jsonrpc":"2.0","method":"$/cancel","params":{"id":1}}` + "\n"))
	for i, e := range lifecycle() {
		rpc(t, inW, 10+i, "hook.event", map[string]any{"event": e})
		if m := next(); m["error"] != nil {
			t.Fatalf("event %d: %v", i, m)
		}
	}
	// Batch 3: some spans went out before shutdown, without waiting an hour.
	deadline := time.Now().Add(5 * time.Second)
	for len(c.spans()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(c.spans()) == 0 {
		t.Fatal("a full batch is sent at once")
	}
	rpc(t, inW, 50, "nope", nil)
	if m := next(); m["error"].(map[string]any)["code"] != float64(-32601) {
		t.Fatalf("unknown method %v", m)
	}
	_, _ = inW.Write([]byte("not json\n"))
	if m := next(); m["id"] != nil || m["error"].(map[string]any)["code"] != float64(-32700) {
		t.Fatalf("parse error must have id null: %v", m)
	}
	rpc(t, inW, 51, "shutdown", nil)
	if m := next(); m["result"] == nil {
		t.Fatalf("shutdown %v", m)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := len(c.spans()); got != 6 {
		t.Fatalf("every span is sent by shutdown: %d", got)
	}
	res := c.requests[0]["resourceSpans"].([]any)[0].(map[string]any)["resource"].(map[string]any)["attributes"].([]any)
	if b, _ := json.Marshal(res); !strings.Contains(string(b), `"service.version","value":{"stringValue":"1.2.3"}`) || !strings.Contains(string(b), `"barnraise.project"`) {
		t.Fatalf("resource %s", b)
	}
}

var pluginBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "barnraise-otel-test")
	if err != nil {
		panic(err)
	}
	pluginBin = filepath.Join(dir, "otelplugin")
	if runtime.GOOS == "windows" {
		pluginBin += ".exe"
	}
	build := exec.Command("go", "build", "-o", pluginBin, "./testdata/otelplugin")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic("build the otel plugin: " + err.Error())
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// The plugin as the daemon runs it: the real plugin host starts it,
// delivers the events of its `events:` list and stops it; the collector
// gets the item's trace.
func TestWithThePluginHost(t *testing.T) {
	c, srv := newCollector(t)
	env := map[string]string{"PATH": os.Getenv("PATH"), "HOME": t.TempDir(), "OTEL_HEADERS_SECRET": "x-api-key=s3cret"}
	h := plugin.New(plugin.Options{
		Name: "otel", Project: "shop", Root: t.TempDir(), Version: "1.0.0",
		Def: domain.PluginDef{Command: pluginBin, Timeout: 5 * time.Second, Events: []string{"item.*", "agent.usage", "check.completed"},
			Options: map[string]any{"endpoint": srv.URL, "interval": "1h"}, Secrets: map[string]string{"OTEL_EXPORTER_OTLP_HEADERS": "env:OTEL_HEADERS_SECRET"}},
		Getenv: func(k string) string { return env[k] },
		Secret: func(ref string) (string, error) { return env[strings.TrimPrefix(ref, "env:")], nil },
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	info, err := h.Info(context.Background())
	if err != nil || info.Name != "barnraise-otel" || !info.Implements("hook") {
		t.Fatalf("%+v %v", info, err)
	}
	for _, e := range lifecycle() {
		if !h.Wants(e.Type) {
			continue
		}
		if err := h.Event(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	spans := c.spans()
	if len(spans) != 6 {
		t.Fatalf("spans %d", len(spans))
	}
	want := hex.EncodeToString(func() []byte { id := TraceID("shop", "I-1"); return id[:] }())
	n := 0
	for _, s := range spans {
		if s["traceId"] == want {
			n++
		}
	}
	if n != 5 || c.headers[0].Get("X-Api-Key") != "s3cret" {
		t.Fatalf("%d spans in the item's trace; headers %v", n, c.headers[0])
	}
}
