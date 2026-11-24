package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/gen/barnraise/v1/barnraisev1connect"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// loginDaemon answers the login calls as a daemon with two machines would:
// m1 waits for the code its page shows, m2 gives a one-time code and fails.
type loginDaemon struct {
	barnraisev1connect.UnimplementedBarnraiseServiceHandler
	mu      sync.Mutex
	started *pb.AccountLoginRequest
	codes   []string
	polls   int
}

func (d *loginDaemon) logins() *pb.AccountLoginResponse {
	m1 := &pb.AccountLogin{Machine: "m1", State: domain.LoginRunning, Url: "https://claude.example/x", WantsCode: len(d.codes) == 0}
	m2 := &pb.AccountLogin{Machine: "m2", State: domain.LoginRunning, Url: "https://auth.example/device", Code: "ABCD-12345"}
	if len(d.codes) > 0 {
		m1.State = domain.LoginDone
	}
	if d.polls > 1 {
		m2.State, m2.Error = domain.LoginFailed, "the login ended without signing in"
	}
	return &pb.AccountLoginResponse{Account: "claude-3", Logins: []*pb.AccountLogin{m1, m2}}
}

func (d *loginDaemon) StartAccountLogin(_ context.Context, r *connect.Request[pb.AccountLoginRequest]) (*connect.Response[pb.AccountLoginResponse], error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.started = r.Msg
	return connect.NewResponse(d.logins()), nil
}

func (d *loginDaemon) GetAccountLogin(context.Context, *connect.Request[pb.AccountLoginRequest]) (*connect.Response[pb.AccountLoginResponse], error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.polls++
	return connect.NewResponse(d.logins()), nil
}

func (d *loginDaemon) AnswerAccountLogin(_ context.Context, r *connect.Request[pb.AccountLoginRequest]) (*connect.Response[pb.AccountLoginResponse], error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.codes = append(d.codes, strings.Join(r.Msg.Machines, ",")+"="+r.Msg.Code)
	return connect.NewResponse(d.logins()), nil
}

func TestAccountLoginFollowsEveryMachine(t *testing.T) {
	old := loginPoll
	loginPoll = time.Millisecond
	t.Cleanup(func() { loginPoll = old })
	d := &loginDaemon{}
	mux := http.NewServeMux()
	mux.Handle(barnraisev1connect.NewBarnraiseServiceHandler(d))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("BARNRAISE_TOKEN", "test")
	run := func(in string, args ...string) (string, error) {
		var out bytes.Buffer
		e := &env{out: &out, errOut: &out, in: strings.NewReader(in)}
		root := e.rootCmd()
		root.SetArgs(append(args, "--url", srv.URL))
		root.SetOut(&out)
		root.SetErr(&out)
		err := root.Execute()
		return out.String(), err
	}
	out, err := run("abc#def\n", "account", "login", "claude", "--all")
	if err == nil || !strings.Contains(err.Error(), "not logged in on 1 machine") {
		t.Fatalf("err %v\n%s", err, out)
	}
	if d.started.Provider != "claude" || d.started.Account != "" || !d.started.All {
		t.Errorf("started: %+v", d.started)
	}
	if len(d.codes) != 1 || d.codes[0] != "m1=abc#def" {
		t.Errorf("codes: %v", d.codes)
	}
	for _, want := range []string{"m1: open https://claude.example/x", "m2: open https://auth.example/device", "one-time code there: ABCD-12345",
		"✔ m1: claude-3 is logged in", "✘ m2: failed the login ended without signing in"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// An account id logs that account in; --all and --machine do not go together.
	if _, err := run("", "account", "login", "claude-3", "--all", "--machine", "m1"); err == nil {
		t.Error("--all with --machine was taken")
	}
	d.codes, d.polls = []string{"done"}, 5
	if _, err := run("", "account", "login", "claude-3", "--machine", "m1,m2"); err == nil || d.started.Account != "claude-3" || d.started.Provider != "" || len(d.started.Machines) != 2 {
		t.Errorf("by id: %v %+v", err, d.started)
	}
}
