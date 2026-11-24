package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/adapters/out/sqlite"
	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

// stubLogin prints a link, waits for a code and ends when stopped.
type stubLogin struct{ codes chan string }

type stubLoginProc struct {
	out   chan string
	end   chan error
	codes chan string
}

func (p *stubLoginProc) Output() <-chan string { return p.out }
func (p *stubLoginProc) Input(s string) error  { p.codes <- s; return nil }
func (p *stubLoginProc) Wait() error           { return <-p.end }
func (p *stubLoginProc) Stop()                 { close(p.out); p.end <- errors.New("killed") }

func (s stubLogin) Start(context.Context, ports.LoginSpec) (ports.LoginProcess, error) {
	p := &stubLoginProc{out: make(chan string, 1), end: make(chan error, 1), codes: s.codes}
	p.out <- "visit https://login.example/x\nPaste code here if prompted > "
	return p, nil
}
func (stubLogin) LoggedIn(context.Context, ports.LoginSpec) (bool, error) { return true, nil }

func loginServer(t *testing.T, opts Options) (*fixture, chan string) {
	t.Helper()
	st, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	codes := make(chan string, 2)
	svc := app.New(app.Deps{Store: st, Clock: testkit.NewClock(time.Now()), IDs: testkit.NewIDs("e"), Log: log, Machine: "m1",
		DataDir: t.TempDir(), Login: stubLogin{codes: codes}})
	opts.OwnerToken, opts.Store, opts.Log = "owner-secret", st, log
	hs := httptest.NewServer(New(svc, opts).Handler())
	t.Cleanup(hs.Close)
	return &fixture{srv: hs, svc: svc, store: st}, codes
}

func TestAccountLoginOnThisMachine(t *testing.T) {
	f, codes := loginServer(t, Options{})
	ctx := context.Background()
	owner := f.client("owner-secret")
	// Nothing runs yet: the state of no login is an empty answer.
	if res, err := owner.GetAccountLogin(ctx, connect.NewRequest(&pb.AccountLoginRequest{Account: "claude-1"})); err != nil || len(res.Msg.Logins) != 0 {
		t.Fatalf("no login: %+v %v", res, err)
	}
	res, err := owner.StartAccountLogin(ctx, connect.NewRequest(&pb.AccountLoginRequest{Provider: "claude"}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.Account != "claude-1" || len(res.Msg.Logins) != 1 || res.Msg.Logins[0].Machine != "m1" || res.Msg.Logins[0].State != domain.LoginRunning {
		t.Fatalf("start: %+v", res.Msg)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, err = owner.GetAccountLogin(ctx, connect.NewRequest(&pb.AccountLoginRequest{Account: "claude-1", Machines: []string{"m1"}}))
		if err != nil {
			t.Fatal(err)
		}
		if l := res.Msg.Logins[0]; l.WantsCode {
			if l.Url != "https://login.example/x" || l.StartedAt == "" || len(l.Lines) == 0 {
				t.Fatalf("state: %+v", l)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never asked for the code: %+v", res.Msg)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The code goes to one machine, named.
	if _, err := owner.AnswerAccountLogin(ctx, connect.NewRequest(&pb.AccountLoginRequest{Account: "claude-1", Code: "abc"})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("a code for no machine: %v", err)
	}
	if _, err := owner.AnswerAccountLogin(ctx, connect.NewRequest(&pb.AccountLoginRequest{Account: "claude-1", Machines: []string{"m1"}, Code: "abc"})); err != nil {
		t.Fatal(err)
	}
	if got := <-codes; got != "abc" {
		t.Fatalf("the login got %q", got)
	}
	res, err = owner.CancelAccountLogin(ctx, connect.NewRequest(&pb.AccountLoginRequest{Account: "claude-1"}))
	if err != nil || len(res.Msg.Logins) != 1 {
		t.Fatalf("cancel: %+v %v", res, err)
	}
	// This daemon reaches no other machine, and says so.
	if _, err := owner.StartAccountLogin(ctx, connect.NewRequest(&pb.AccountLoginRequest{Provider: "codex", Machines: []string{"m2"}})); code(err) != connect.CodeUnimplemented {
		t.Fatalf("another machine: %v", err)
	}
	if _, err := owner.StartAccountLogin(ctx, connect.NewRequest(&pb.AccountLoginRequest{})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("neither account nor provider: %v", err)
	}
	if _, err := owner.StartAccountLogin(ctx, connect.NewRequest(&pb.AccountLoginRequest{Account: "gemini-1", Provider: "gemini"})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("gemini: %v", err)
	}
	// A session token never starts a login, whatever its scopes.
	tok := f.svc.Tokens().Issue(app.Actor{Kind: app.ActorAgent, Name: "dev-1", Role: "implementer", Scopes: app.AllScopes, Session: "s"}, time.Hour)
	if _, err := f.client(tok).StartAccountLogin(ctx, connect.NewRequest(&pb.AccountLoginRequest{Provider: "claude"})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("session login: %v", err)
	}
}

func TestAccountLoginFanOutOption(t *testing.T) {
	var gotOp string
	var gotActor app.Actor
	f, _ := loginServer(t, Options{
		AccountLogin: func(_ context.Context, a app.Actor, op string, r *pb.AccountLoginRequest) (*pb.AccountLoginResponse, error) {
			gotOp, gotActor = op, a
			if r.Account == "broken" {
				return nil, app.ErrInvalid
			}
			return &pb.AccountLoginResponse{Account: "codex-1", Logins: []*pb.AccountLogin{
				{Machine: "m1", State: domain.LoginRunning, Code: "ABCD-12345"}, {Machine: "m2", State: domain.LoginFailed, Error: "unreachable"}}}, nil
		},
	})
	ctx := context.Background()
	owner := f.client("owner-secret")
	res, err := owner.StartAccountLogin(ctx, connect.NewRequest(&pb.AccountLoginRequest{Provider: "codex", All: true}))
	if err != nil || len(res.Msg.Logins) != 2 || res.Msg.Logins[1].Error == "" || gotOp != app.LoginOpStart || gotActor.Kind != app.ActorOwner {
		t.Fatalf("%+v %v %q %+v", res, err, gotOp, gotActor)
	}
	if _, err := owner.StartAccountLogin(ctx, connect.NewRequest(&pb.AccountLoginRequest{Account: "broken"})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("an error of the daemon: %v", err)
	}
	if p := AccountLoginPB(domain.LoginSession{Machine: "m1", Account: "a-1", State: domain.LoginDone, Lines: []string{"x"}}); p.Machine != "m1" || p.State != domain.LoginDone || p.Lines[0] != "x" {
		t.Fatalf("%+v", p)
	}
}

func TestSetMaxAgents(t *testing.T) {
	var got int
	f, _ := loginServer(t, Options{SetMaxAgents: func(_ context.Context, n int, machines []string) (*pb.MaxAgentsResponse, error) {
		got = n
		return &pb.MaxAgentsResponse{Machines: []*pb.PeerResult{{Name: "m1", Ok: true}}}, nil
	}})
	ctx := context.Background()
	owner := f.client("owner-secret")
	if _, err := owner.SetMaxAgents(ctx, connect.NewRequest(&pb.MaxAgentsRequest{MaxAgents: 65})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("65: %v", err)
	}
	if res, err := owner.SetMaxAgents(ctx, connect.NewRequest(&pb.MaxAgentsRequest{MaxAgents: 5})); err != nil || got != 5 || !res.Msg.Machines[0].Ok {
		t.Fatalf("%v %d", err, got)
	}
	bare, _ := loginServer(t, Options{})
	if _, err := bare.client("owner-secret").SetMaxAgents(ctx, connect.NewRequest(&pb.MaxAgentsRequest{MaxAgents: 2})); code(err) != connect.CodeUnimplemented {
		t.Fatalf("no option: %v", err)
	}
}
