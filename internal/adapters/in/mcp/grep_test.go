package mcp

import (
	"context"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
)

// grepAPI answers find with match hits and records the requests.
type grepAPI struct {
	fakeAPI
	gmu  sync.Mutex
	reqs map[string]*pb.FindRequest // by query
}

func (g *grepAPI) Find(_ context.Context, r *connect.Request[pb.FindRequest]) (*connect.Response[pb.FindResponse], error) {
	g.gmu.Lock()
	if g.reqs == nil {
		g.reqs = map[string]*pb.FindRequest{}
	}
	g.reqs[r.Msg.Query] = r.Msg
	g.gmu.Unlock()
	if strings.Contains(r.Msg.Query, "nothing") {
		return connect.NewResponse(&pb.FindResponse{}), nil
	}
	return connect.NewResponse(&pb.FindResponse{Hits: []*pb.SearchHit{
		{Id: "internal/app/a.go:12", Kind: "match", Title: "internal/app/a.go:12", Summary: "func (s *Service) Find(ctx context.Context) {"},
		{Id: "internal/app/b.go:3", Kind: "match", Title: "internal/app/b.go:3", Summary: "s.Find(ctx)"},
	}}), nil
}

func TestFindInTheFilesText(t *testing.T) {
	g := &grepAPI{}
	got, _ := run(t, g, Options{Project: "app", Role: "implementer"},
		call(1, "find", map[string]any{"query": "Find(ctx", "mode": "substring", "path": "internal/app/", "limit": 2}),
		call(2, "find", map[string]any{"query": `Find\(\w+`, "mode": "regex", "area": "A-core"}),
		call(3, "find", map[string]any{"query": "a.b", "mode": "substring", "ignore_case": true}),
		call(4, "find", map[string]any{"query": "x", "mode": "regex", "ignore_case": true}),
		call(5, "find", map[string]any{"query": "nothing", "mode": "substring"}),
		call(6, "find", map[string]any{"query": "x", "mode": "regex", "path": "a b"}),
		call(7, "find", map[string]any{"query": "x", "mode": "grep"}),
		call(8, "find", map[string]any{"query": "login", "mode": "knowledge", "kind": "decision"}),
	)
	r := toolResultOf(t, got["1"])
	if r.IsError || !strings.Contains(r.text(), "- internal/app/a.go:12: func (s *service) find") || !strings.Contains(r.text(), "limit reached") {
		t.Errorf("text hits: %q", r.text())
	}
	if r := toolResultOf(t, got["5"]); r.IsError || !strings.Contains(r.text(), "nothing found") {
		t.Errorf("no match: %+v", r)
	}
	if r := toolResultOf(t, got["8"]); r.IsError || !strings.Contains(r.text(), "[match]") {
		t.Errorf("knowledge mode keeps the usual lines: %q", r.text())
	}
	if r := toolResultOf(t, got["6"]); !r.IsError {
		t.Errorf("a path with spaces must be refused: %+v", r)
	}
	if e := got["7"]; e.Error == nil && !toolResultOf(t, e).IsError {
		t.Errorf("unknown mode must be refused: %+v", e)
	}
	g.gmu.Lock()
	defer g.gmu.Unlock()
	want := map[string]struct{ kind, area string }{
		"file:internal/app/ Find(ctx": {"grep", ""},
		`Find\(\w+`:                   {"regex", "A-core"},
		`(?i)a\.b`:                    {"regex", ""},
		"(?i)x":                       {"regex", ""},
		"nothing":                     {"grep", ""},
		"login":                       {"decision", ""},
	}
	for q, w := range want {
		r := g.reqs[q]
		if r == nil || r.Kind != w.kind || r.Area != w.area || r.Project != "app" {
			t.Errorf("request %q: %+v", q, r)
		}
	}
}
