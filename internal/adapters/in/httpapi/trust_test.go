package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/gen/barnraise/v1/barnraisev1connect"
	"github.com/leandrodaf/barnraise/internal/adapters/out/sqlite"
	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/config"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

type fixture struct {
	srv   *httptest.Server
	svc   *app.Service
	store *sqlite.Store
	root  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := app.New(app.Deps{Store: st, Clock: testkit.NewClock(time.Now()), IDs: testkit.NewIDs("e"), Log: log, Machine: "m1", DataDir: t.TempDir()})
	root := t.TempDir()
	cfg, err := config.Compile(root, []byte("project: demo\nchecks:\n  main: { run: \"true\" }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddProject(context.Background(), cfg, app.ProjectPorts{}); err != nil {
		t.Fatal(err)
	}
	s := New(svc, Options{OwnerToken: "owner-secret", Store: st, Log: log, VerifyAnchor: func(domain.ChainAnchor) error { return nil }})
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	return &fixture{srv: hs, svc: svc, store: st, root: root}
}

func (f *fixture) client(token string) barnraisev1connect.BarnraiseServiceClient {
	return barnraisev1connect.NewBarnraiseServiceClient(f.srv.Client(), f.srv.URL, connect.WithInterceptors(connect.UnaryInterceptorFunc(
		func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, r connect.AnyRequest) (connect.AnyResponse, error) {
				r.Header().Set("Authorization", "Bearer "+token)
				return next(ctx, r)
			}
		})))
}

func (f *fixture) get(t *testing.T, method, path, token string, out any) int {
	t.Helper()
	r, _ := http.NewRequest(method, f.srv.URL+path, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	resp, err := f.srv.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func code(err error) connect.Code {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code()
	}
	return 0
}

func TestSessionTokenGuard(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := app.Actor{Kind: app.ActorAgent, Name: "dev-1", Role: "implementer", Project: "demo", ItemID: "I-1", Step: "implement",
		Scopes: app.RoleScopes("implementer", false), Session: "ses_1"}
	tok := f.svc.Tokens().Issue(a, time.Hour)
	cl := f.client(tok)

	// Allowed: reading the project.
	if _, err := cl.Status(ctx, connect.NewRequest(&pb.ProjectRef{Project: "demo"})); err != nil {
		t.Fatalf("status: %v", err)
	}
	// Another item, even though the role may deliver.
	_, err := cl.Deliver(ctx, connect.NewRequest(&pb.DeliverRequest{Project: "demo", Delivery: &pb.Delivery{ItemId: "I-2", Step: "implement", Status: "DONE"}}))
	if code(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "works on I-1, not I-2") {
		t.Fatalf("other item: %v", err)
	}
	// A tool the role was not given.
	if _, err := cl.SubmitReview(ctx, connect.NewRequest(&pb.ReviewRequest{Project: "demo", Review: &pb.Review{ItemId: "I-1"}})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("review: %v", err)
	}
	// Owner-only procedures.
	if _, err := cl.ListTokens(ctx, connect.NewRequest(&pb.Empty{})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("tokens: %v", err)
	}
	// Another project.
	if _, err := cl.Status(ctx, connect.NewRequest(&pb.ProjectRef{Project: "other"})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("other project: %v", err)
	}
	// The event stream outside the session's project.
	if c := f.get(t, "GET", "/v1/events?project=other", tok, nil); c != http.StatusForbidden {
		t.Fatalf("stream: %d", c)
	}
	evs, _ := f.store.Query(ctx, ports.EventQuery{Project: "demo", Types: []string{domain.EvTokenDenied}})
	if len(evs) != 5 {
		t.Fatalf("denial events %d", len(evs))
	}
	audit, _ := f.store.ListAudit(ctx, sqlite.AuditQuery{Project: "demo"})
	if len(audit) == 0 || audit[0].Action != "token.denied" {
		t.Fatalf("audit %+v", audit)
	}
	// After the session the token is refused outright.
	f.svc.Tokens().Revoke(tok)
	if _, err := cl.Status(ctx, connect.NewRequest(&pb.ProjectRef{Project: "demo"})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("revoked: %v", err)
	}
	// The owner is not restricted by the guard.
	if _, err := f.client("owner-secret").ListTokens(ctx, connect.NewRequest(&pb.Empty{})); err != nil {
		t.Fatalf("owner: %v", err)
	}
}

func TestCallRefs(t *testing.T) {
	r := callRefs(&pb.SendMessageRequest{Project: "demo", Message: &pb.Message{ItemId: "I-4", To: "x"}})
	if r.Project != "demo" || len(r.ItemIDs) != 1 || r.ItemIDs[0] != "I-4" {
		t.Fatalf("message refs %+v", r)
	}
	if r := callRefs(&pb.ApproveRequest{Project: "demo", Target: "I-9"}); r.ID != "I-9" {
		t.Fatalf("target %+v", r)
	}
	if r := callRefs(&pb.ItemRef{Project: "demo", Id: "I-3"}); r.ID != "I-3" {
		t.Fatalf("id %+v", r)
	}
}

func TestVerifyAndTrustEndpoints(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	agentTok := f.svc.Tokens().Issue(app.Actor{Kind: app.ActorAgent, Name: "a", Project: "demo", Scopes: []string{app.ScopeAll}}, time.Hour)
	_ = os.MkdirAll(filepath.Join(f.root, ".barnraise"), 0o755)
	_ = os.WriteFile(filepath.Join(f.root, ".barnraise", "constitution.md"), []byte("v1"), 0o644)
	if err := f.svc.TrustOnLoad(ctx, "demo"); err != nil {
		t.Fatal(err)
	}

	var rep domain.ChainReport
	if c := f.get(t, "GET", "/v1/events/verify", "owner-secret", &rep); c != 200 || !rep.OK() || rep.Checked == 0 {
		t.Fatalf("verify %d %+v", c, rep)
	}
	if c := f.get(t, "GET", "/v1/events/verify", agentTok, nil); c != http.StatusForbidden {
		t.Fatalf("agent verify %d", c)
	}
	if _, err := f.store.DB().Exec(`UPDATE events SET actor = 'x' WHERE seq = 1`); err != nil {
		t.Fatal(err)
	}
	if c := f.get(t, "GET", "/v1/events/verify?from=1", "owner-secret", &rep); c != 200 || rep.OK() || rep.Break.Seq != 1 {
		t.Fatalf("tampered %d %+v", c, rep)
	}

	_ = os.WriteFile(filepath.Join(f.root, ".barnraise", "constitution.md"), []byte("v2"), 0o644)
	var tr app.TrustReport
	if c := f.get(t, "GET", "/v1/trust?project=demo", "owner-secret", &tr); c != 200 || len(tr.Problems) != 1 {
		t.Fatalf("status %d %+v", c, tr)
	}
	if c := f.get(t, "POST", "/v1/trust?project=demo", agentTok, nil); c != http.StatusForbidden {
		t.Fatalf("agent trust %d", c)
	}
	if c := f.get(t, "POST", "/v1/trust?project=demo", "owner-secret", &tr); c != 200 || len(tr.Problems) != 0 || len(tr.Recorded) != 1 {
		t.Fatalf("trust %d %+v", c, tr)
	}
	if c := f.get(t, "POST", "/v1/trust?project=demo&item=zzz", "owner-secret", nil); c != http.StatusBadRequest {
		t.Fatalf("bad item %d", c)
	}
	if c := f.get(t, "GET", "/v1/trust?project=nope", "owner-secret", nil); c != http.StatusNotFound {
		t.Fatalf("unknown project %d", c)
	}
}
