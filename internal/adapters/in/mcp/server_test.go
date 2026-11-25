package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
)

// fakeAPI implements the methods the tests use; the others panic, which the
// server turns into a tool error.
type fakeAPI struct {
	API

	mu         sync.Mutex
	deliveries []*pb.DeliverRequest
	reviews    []*pb.ReviewRequest
	resumed    int
	paused     []string
	deliverErr error
	none       bool
	updates    []*pb.UpdateItemRequest

	statusMu      sync.Mutex
	statusWaiting int
	statusWant    int
	statusAll     chan struct{}
}

func (f *fakeAPI) NextItem(_ context.Context, r *connect.Request[pb.NextItemRequest]) (*connect.Response[pb.Assignment], error) {
	if f.none {
		return connect.NewResponse(&pb.Assignment{None: true, Note: noWork}), nil
	}
	step, _ := structpb.NewStruct(map[string]any{"name": "implement", "type": "agent", "role": "implementer"})
	return connect.NewResponse(&pb.Assignment{Project: r.Msg.Project, New: true, Step: step, Item: &pb.Item{
		Id: "I-7", Title: "Add login", Status: "in_progress", Step: "implement", Area: "auth", Priority: 1,
		Criteria: []*pb.Criterion{{Id: "C1", Text: "When the user signs in, the app shall show the home screen."}},
	}}), nil
}

func (f *fakeAPI) UpdateItem(_ context.Context, r *connect.Request[pb.UpdateItemRequest]) (*connect.Response[pb.ItemResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, r.Msg)
	crit, _ := r.Msg.Patch.AsMap()["criteria"].([]any)
	item := &pb.Item{Id: r.Msg.Id, Title: "Refined", Status: "ready"}
	for range crit {
		item.Criteria = append(item.Criteria, &pb.Criterion{})
	}
	return connect.NewResponse(&pb.ItemResponse{Item: item}), nil
}

func (f *fakeAPI) Context(_ context.Context, r *connect.Request[pb.ContextRequest]) (*connect.Response[pb.ContextResponse], error) {
	return connect.NewResponse(&pb.ContextResponse{ItemId: r.Msg.Id, Text: "CONTEXT OF " + r.Msg.Id, Tokens: 10, Budget: 100}), nil
}

func (f *fakeAPI) Deliver(_ context.Context, r *connect.Request[pb.DeliverRequest]) (*connect.Response[pb.ItemResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deliverErr != nil {
		return nil, f.deliverErr
	}
	f.deliveries = append(f.deliveries, r.Msg)
	return connect.NewResponse(&pb.ItemResponse{Item: &pb.Item{Id: r.Msg.Delivery.ItemId, Status: "in_progress", Step: "review"}}), nil
}

func (f *fakeAPI) SubmitReview(_ context.Context, r *connect.Request[pb.ReviewRequest]) (*connect.Response[pb.ItemResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reviews = append(f.reviews, r.Msg)
	return connect.NewResponse(&pb.ItemResponse{Item: &pb.Item{Id: r.Msg.Review.ItemId, Status: "queued"}}), nil
}

func (f *fakeAPI) Pause(_ context.Context, r *connect.Request[pb.PauseRequest]) (*connect.Response[pb.Empty], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paused = append(f.paused, r.Msg.Why)
	return connect.NewResponse(&pb.Empty{}), nil
}

func (f *fakeAPI) Resume(context.Context, *connect.Request[pb.ProjectRef]) (*connect.Response[pb.Empty], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumed++
	return connect.NewResponse(&pb.Empty{}), nil
}

// Status blocks until statusWant calls are in flight, proving concurrency.
func (f *fakeAPI) Status(ctx context.Context, r *connect.Request[pb.ProjectRef]) (*connect.Response[pb.StatusResponse], error) {
	if f.statusWant > 0 {
		f.statusMu.Lock()
		f.statusWaiting++
		if f.statusWaiting == f.statusWant {
			close(f.statusAll)
		}
		f.statusMu.Unlock()
		select {
		case <-f.statusAll:
		case <-time.After(3 * time.Second):
			return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("calls were not concurrent"))
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return connect.NewResponse(&pb.StatusResponse{Project: r.Msg.Project, Counts: map[string]int32{"ready": 2}}), nil
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type callResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent map[string]any `json:"structuredContent"`
	IsError           bool           `json:"isError"`
}

// text joins the text content, lower-cased so checks ignore sentence case.
func (c callResult) text() string {
	var parts []string
	for _, x := range c.Content {
		parts = append(parts, x.Text)
	}
	return strings.ToLower(strings.Join(parts, "\n"))
}

// run feeds the lines to a server and returns the responses by id.
func run(t *testing.T, api API, opts Options, lines ...string) (map[string]response, []string) {
	t.Helper()
	var out bytes.Buffer
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := Serve(ctx, in, &out, api, opts); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	byID := map[string]response{}
	var raw []string
	sc := bufio.NewScanner(&out)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		raw = append(raw, sc.Text())
		var r response
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("output line is not JSON: %q: %v", sc.Text(), err)
		}
		if r.JSONRPC != "2.0" {
			t.Fatalf("bad jsonrpc in %q", sc.Text())
		}
		byID[string(r.ID)] = r
	}
	return byID, raw
}

func req(id any, method string, params any) string {
	m := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		m["id"] = id
	}
	if params != nil {
		m["params"] = params
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func call(id any, name string, args any) string {
	return req(id, "tools/call", map[string]any{"name": name, "arguments": args})
}

func toolResultOf(t *testing.T, r response) callResult {
	t.Helper()
	if r.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %+v", r.Error)
	}
	var c callResult
	if err := json.Unmarshal(r.Result, &c); err != nil {
		t.Fatalf("result: %v", err)
	}
	return c
}

func TestInitializePingAndNotifications(t *testing.T) {
	got, raw := run(t, &fakeAPI{}, Options{Version: "1.2.3"},
		req(1, "initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "x"}}),
		req(nil, "notifications/initialized", nil),
		req(2, "initialize", map[string]any{"protocolVersion": "1999-01-01"}),
		req("p", "ping", nil),
		req(3, "resources/list", nil),
	)
	if len(raw) != 4 {
		t.Fatalf("want 4 responses (no reply to the notification), got %d: %v", len(raw), raw)
	}
	var init struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Capabilities    map[string]any `json:"capabilities"`
		ServerInfo      struct{ Name, Version string }
		Instructions    string `json:"instructions"`
	}
	if err := json.Unmarshal(got["1"].Result, &init); err != nil {
		t.Fatal(err)
	}
	if init.ProtocolVersion != "2025-03-26" || init.ServerInfo.Name != "barnraise" || init.ServerInfo.Version != "1.2.3" || init.Instructions == "" {
		t.Fatalf("bad initialize: %+v", init)
	}
	tools, _ := init.Capabilities["tools"].(map[string]any)
	if tools == nil || tools["listChanged"] != false {
		t.Fatalf("bad capabilities: %v", init.Capabilities)
	}
	if err := json.Unmarshal(got["2"].Result, &init); err != nil || init.ProtocolVersion != "2025-06-18" {
		t.Fatalf("unknown version must fall back to 2025-06-18, got %q", init.ProtocolVersion)
	}
	if p := got[`"p"`]; p.Error != nil || string(p.Result) != "{}" {
		t.Fatalf("bad ping: %+v", p)
	}
	if e := got["3"].Error; e == nil || e.Code != -32601 {
		t.Fatalf("unknown method must be -32601, got %+v", got["3"])
	}
}

func TestParseError(t *testing.T) {
	got, _ := run(t, &fakeAPI{}, Options{}, `{not json`)
	if e := got["null"].Error; e == nil || e.Code != codeParseError {
		t.Fatalf("want parse error, got %+v", got)
	}
}

func listTools(t *testing.T, role string) map[string]map[string]any {
	t.Helper()
	got, _ := run(t, &fakeAPI{}, Options{Role: role}, req(1, "tools/list", nil))
	var res struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(got["1"].Result, &res); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, x := range res.Tools {
		if x.Description == "" {
			t.Fatalf("%s has no description", x.Name)
		}
		out[x.Name] = x.InputSchema
	}
	return out
}

func TestToolVisibilityByRole(t *testing.T) {
	impl := listTools(t, "implementer")
	for _, n := range []string{"next_item", "deliver", "propose", "ask_owner", "message", "status", "context", "find"} {
		if impl[n] == nil {
			t.Errorf("implementer should see %s", n)
		}
	}
	for _, n := range []string{"approve", "review", "focus", "pause", "answer", "create_item", "edit_item"} {
		if impl[n] != nil {
			t.Errorf("implementer must not see %s", n)
		}
	}

	for _, role := range []string{"reviewer", "security-reviewer", "critic"} {
		rev := listTools(t, role)
		if rev["review"] == nil || rev["deliver"] != nil || rev["approve"] != nil || rev["ask_owner"] != nil {
			t.Errorf("%s sees the wrong tools: %v", role, keys(rev))
		}
	}

	analyst := listTools(t, "analyst")
	if analyst["approve"] == nil || analyst["review"] == nil || analyst["edit_item"] == nil || analyst["close_item"] == nil || analyst["answer"] != nil {
		t.Errorf("analyst sees the wrong tools: %v", keys(analyst))
	}

	owner := listTools(t, "")
	all := []string{"next_item", "deliver", "review", "propose", "ask_owner", "message", "status", "context", "find",
		"approve", "focus", "pause", "answer", "create_item", "edit_item", "close_item"}
	if len(owner) != len(all) {
		t.Errorf("owner should see %d tools, sees %v", len(all), keys(owner))
	}
	for _, n := range all {
		s := owner[n]
		if s == nil {
			t.Errorf("owner should see %s", n)
			continue
		}
		if s["type"] != "object" || s["additionalProperties"] != false {
			t.Errorf("%s schema is not a strict object: %v", n, s)
		}
		if _, ok := s["required"].([]any); !ok {
			t.Errorf("%s schema has no required list", n)
		}
		if props, _ := s["properties"].(map[string]any); props == nil {
			t.Errorf("%s schema has no properties", n)
		} else {
			for p, v := range props {
				if d, _ := v.(map[string]any)["description"].(string); d == "" {
					t.Errorf("%s.%s has no description", n, p)
				}
			}
		}
	}
	status, _ := owner["deliver"]["properties"].(map[string]any)["status"].(map[string]any)
	if fmt.Sprint(status["enum"]) != "[DONE DONE_WITH_CONCERNS NEEDS_CONTEXT BLOCKED]" {
		t.Errorf("deliver status enum: %v", status["enum"])
	}
	if got := ToolNames("implementer"); len(got) < 6 || len(got) > 8 {
		t.Errorf("implementer should get 6 to 8 tools, got %v", got)
	}
}

func keys(m map[string]map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestDeliver(t *testing.T) {
	f := &fakeAPI{}
	got, _ := run(t, f, Options{Project: "app", Role: "implementer", Item: "I-1"},
		call(1, "deliver", map[string]any{
			"status": "DONE", "summary": "Added the login screen.",
			"criteria": []any{map[string]any{"id": "C1", "met": true, "evidence": "checks.unit passed"}},
			"files":    []string{"app/login.go"},
		}),
		call(2, "deliver", map[string]any{"status": "BLOCKED", "summary": "Stuck."}),
		call(3, "deliver", map[string]any{"status": "DONE", "summary": "x", "criteria": []any{map[string]any{"id": "C1", "met": true}}}),
		call(4, "deliver", map[string]any{"status": "FINISHED", "summary": "x"}),
		call(5, "deliver", map[string]any{"status": "DONE", "summary": "x", "colour": "blue"}),
		call(6, "deliver", map[string]any{"status": "DONE"}),
		call(7, "deliver", map[string]any{"status": "DONE", "summary": "x", "files": "a.go"}),
	)
	ok := toolResultOf(t, got["1"])
	if ok.IsError || !strings.Contains(ok.text(), strings.ToLower("Delivered I-1 as DONE")) || ok.StructuredContent["item"] == nil {
		t.Fatalf("valid delivery: %+v", ok)
	}
	wants := map[string]string{
		"2": "needs a reason",
		"3": "without evidence",
		"4": "status must be",
		"5": `unknown argument "colour"`,
		"6": "summary",
		"7": "files must be a list",
	}
	for id, want := range wants {
		r := toolResultOf(t, got[id])
		if !r.IsError || !strings.Contains(r.text(), strings.ToLower(want)) {
			t.Errorf("call %s: want an error containing %q, got %+v", id, want, r)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.deliveries) != 1 {
		t.Fatalf("only the valid delivery should reach the daemon, got %d", len(f.deliveries))
	}
	d := f.deliveries[0]
	if d.Project != "app" || d.Delivery.ItemId != "I-1" || d.Delivery.Criteria[0].Evidence == "" || d.Delivery.Files[0] != "app/login.go" {
		t.Fatalf("bad delivery sent: %+v", d)
	}
}

func TestDaemonErrorIsToolError(t *testing.T) {
	f := &fakeAPI{deliverErr: connect.NewError(connect.CodeFailedPrecondition, errors.New("I-1 is not at a delivery step"))}
	got, _ := run(t, f, Options{Role: "implementer", Item: "I-1"},
		call(1, "deliver", map[string]any{"status": "DONE", "summary": "ok"}))
	r := toolResultOf(t, got["1"])
	if !r.IsError || !strings.Contains(r.text(), strings.ToLower("I-1 is not at a delivery step")) {
		t.Fatalf("want daemon error as tool error, got %+v", r)
	}
}

func TestReview(t *testing.T) {
	f := &fakeAPI{}
	got, _ := run(t, f, Options{Role: "reviewer", Item: "I-2"},
		call(1, "review", map[string]any{"criteria_verdict": "pass", "decision": "APPROVE", "summary": "Good."}),
		call(2, "review", map[string]any{"criteria_verdict": "pass", "code_verdict": "fail", "decision": "APPROVE", "summary": "x"}),
		call(3, "review", map[string]any{"criteria_verdict": "pass", "code_verdict": "fail", "decision": "REWORK", "summary": "x"}),
		call(4, "review", map[string]any{"criteria_verdict": "pass", "code_verdict": "fail", "decision": "REWORK", "summary": "Fix the error path.",
			"findings": []any{map[string]any{"file": "a.go", "line": 12, "severity": "major", "text": "error ignored"}}}),
		call(5, "deliver", map[string]any{"status": "DONE", "summary": "x"}),
	)
	if r := toolResultOf(t, got["1"]); !r.IsError || !strings.Contains(r.text(), strings.ToLower("two verdicts")) {
		t.Errorf("review without code_verdict must be refused: %+v", r)
	}
	if r := toolResultOf(t, got["2"]); !r.IsError || !strings.Contains(r.text(), strings.ToLower("APPROVE needs both verdicts")) {
		t.Errorf("inconsistent APPROVE must be refused: %+v", r)
	}
	if r := toolResultOf(t, got["3"]); !r.IsError || !strings.Contains(r.text(), strings.ToLower("finding")) {
		t.Errorf("REWORK without findings must be refused: %+v", r)
	}
	if r := toolResultOf(t, got["4"]); r.IsError || !strings.Contains(r.text(), strings.ToLower("REWORK")) {
		t.Errorf("valid review: %+v", r)
	}
	if e := got["5"].Error; e == nil || e.Code != codeInvalidParams {
		t.Errorf("a reviewer calling deliver must get unknown tool, got %+v", got["5"])
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reviews) != 1 || f.reviews[0].Review.ItemId != "I-2" || f.reviews[0].Review.Findings[0].Line != 12 {
		t.Fatalf("bad reviews sent: %+v", f.reviews)
	}
}

func TestNextItemAndContext(t *testing.T) {
	got, _ := run(t, &fakeAPI{}, Options{Role: "implementer"},
		call(1, "next_item", map[string]any{}),
	)
	r := toolResultOf(t, got["1"])
	for _, want := range []string{"I-7", "step implement", "C1", "deliver", "CONTEXT OF I-7"} {
		if !strings.Contains(r.text(), strings.ToLower(want)) {
			t.Errorf("next_item text lacks %q: %s", want, r.text())
		}
	}
	if r.StructuredContent["assignment"] == nil || r.StructuredContent["context"] == nil {
		t.Errorf("next_item structured content: %v", r.StructuredContent)
	}

	got, _ = run(t, &fakeAPI{none: true}, Options{Role: "implementer"}, call(1, "next_item", nil), call(2, "context", map[string]any{}))
	if r := toolResultOf(t, got["1"]); r.IsError || !strings.Contains(r.text(), strings.ToLower("do not create work")) {
		t.Errorf("no work: %+v", r)
	}
	if r := toolResultOf(t, got["2"]); !r.IsError || !strings.Contains(r.text(), strings.ToLower("item_id")) {
		t.Errorf("context without an item must ask for item_id: %+v", r)
	}
}

func TestCurrentItemFromNextItem(t *testing.T) {
	f := &fakeAPI{}
	var out bytes.Buffer
	s := newServer(f, Options{Role: "implementer"}, &out)
	ctx := context.Background()
	s.call(ctx, s.byNm["next_item"], json.RawMessage(`{}`))
	r := s.call(ctx, s.byNm["deliver"], json.RawMessage(`{"status":"DONE","summary":"done"}`))
	if r.IsError || f.deliveries[0].Delivery.ItemId != "I-7" {
		t.Fatalf("deliver should default to the item from next_item: %+v", r)
	}
}

func TestOwnerTools(t *testing.T) {
	f := &fakeAPI{}
	got, _ := run(t, f, Options{},
		call(1, "pause", map[string]any{"why": "release freeze"}),
		call(2, "pause", map[string]any{"resume": true}),
		call(3, "pause", map[string]any{}),
		call(4, "approve", map[string]any{"target": "I-4", "what": "proposal", "approve": true}),
		call(5, "ask_owner", map[string]any{"context": "c", "decision": "d", "if_no_answer": "x",
			"options": []any{map[string]any{"label": "a", "recommended": true}, map[string]any{"label": "b", "recommended": true}}}),
		call(6, "nope", map[string]any{}),
	)
	for _, id := range []string{"1", "2"} {
		if r := toolResultOf(t, got[id]); r.IsError {
			t.Errorf("call %s: %+v", id, r)
		}
	}
	if r := toolResultOf(t, got["3"]); !r.IsError || !strings.Contains(r.text(), strings.ToLower("why")) {
		t.Errorf("pause without why: %+v", r)
	}
	if r := toolResultOf(t, got["4"]); !r.IsError || !strings.Contains(r.text(), strings.ToLower("P-7")) {
		t.Errorf("approve proposal with an item target: %+v", r)
	}
	if r := toolResultOf(t, got["5"]); !r.IsError || !strings.Contains(r.text(), strings.ToLower("exactly one option")) {
		t.Errorf("two recommended options: %+v", r)
	}
	if e := got["6"].Error; e == nil || e.Code != codeInvalidParams {
		t.Errorf("unknown tool: %+v", got["6"])
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resumed != 1 || len(f.paused) != 1 || f.paused[0] != "release freeze" {
		t.Fatalf("pause/resume calls: paused=%v resumed=%d", f.paused, f.resumed)
	}
}

func TestUnimplementedMethodBecomesToolError(t *testing.T) {
	got, _ := run(t, &fakeAPI{}, Options{}, call(1, "find", map[string]any{"query": "login"}))
	if r := toolResultOf(t, got["1"]); !r.IsError {
		t.Fatalf("a panicking call must be a tool error: %+v", r)
	}
}

func TestConcurrentCalls(t *testing.T) {
	const n = 5
	f := &fakeAPI{statusWant: n, statusAll: make(chan struct{})}
	lines := make([]string, 0, n+1)
	for i := range n {
		lines = append(lines, call(100+i, "status", map[string]any{}))
	}
	lines = append(lines, req("last", "ping", nil))
	got, raw := run(t, f, Options{Project: "app", Role: "implementer"}, lines...)
	if len(raw) != n+1 {
		t.Fatalf("want %d responses, got %d", n+1, len(raw))
	}
	for i := range n {
		r := toolResultOf(t, got[fmt.Sprint(100+i)])
		if r.IsError || !strings.Contains(r.text(), strings.ToLower("Project app")) || !strings.Contains(r.text(), strings.ToLower("ready 2")) {
			t.Errorf("call %d: %+v", 100+i, r)
		}
	}
}

func TestEditItem(t *testing.T) {
	f := &fakeAPI{}
	got, _ := run(t, f, Options{Project: "app", Role: "analyst", Item: "I-28"},
		call(1, "edit_item", map[string]any{
			"title": "Keep the defeat checks through the rebase",
			"criteria": []any{
				map[string]any{"id": "C1", "text": "WHEN the keeper falls in horde 1, THE SYSTEM SHALL keep best horde 0", "check": "main"},
			},
			"deps": []string{"I-4"}, "clear_clarifications": true, "reason": "refined",
		}),
		call(2, "edit_item", map[string]any{"item_id": "I-3"}),
		call(3, "edit_item", map[string]any{"criteria": []any{map[string]any{"id": "C1", "text": " "}}}),
		call(4, "edit_item", map[string]any{"priority": "P9"}),
	)
	ok := toolResultOf(t, got["1"])
	if ok.IsError || !strings.Contains(ok.text(), "1 criteria") {
		t.Fatalf("valid edit: %+v", ok)
	}
	for id, want := range map[string]string{"2": "at least one field", "3": "needs an id and a text", "4": "priority must be"} {
		if r := toolResultOf(t, got[id]); !r.IsError || !strings.Contains(r.text(), want) {
			t.Errorf("call %s: want an error containing %q, got %+v", id, want, r)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.updates) != 1 {
		t.Fatalf("only the valid edit reaches the daemon: %d", len(f.updates))
	}
	u := f.updates[0]
	m := u.Patch.AsMap()
	if u.Id != "I-28" || u.Project != "app" || fmt.Sprint(m["deps"]) != "[I-4]" || fmt.Sprint(m["clarifications"]) != "[]" || m["reason"] != "refined" || m["body"] != nil {
		t.Fatalf("patch sent: %s %v", u.Id, m)
	}
}
