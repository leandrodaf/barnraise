package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// fakeLogin is a scripted provider login: it prints what it is told, takes
// the code, and ends when the test says so.
type fakeLogin struct {
	mu       sync.Mutex
	started  []ports.LoginSpec
	procs    []*fakeLoginProc
	startErr error
	loggedIn bool
	checkErr error
}

type fakeLoginProc struct {
	out    chan string
	end    chan error
	inputs chan string
	once   sync.Once
	inErr  error
}

func (p *fakeLoginProc) Output() <-chan string { return p.out }
func (p *fakeLoginProc) Input(line string) error {
	if p.inErr != nil {
		return p.inErr
	}
	p.inputs <- line
	return nil
}
func (p *fakeLoginProc) Wait() error { return <-p.end }
func (p *fakeLoginProc) Stop()       { p.finish(errors.New("killed")) }
func (p *fakeLoginProc) finish(err error) {
	p.once.Do(func() {
		close(p.out)
		p.end <- err
	})
}

func (f *fakeLogin) Start(_ context.Context, spec ports.LoginSpec) (ports.LoginProcess, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return nil, f.startErr
	}
	p := &fakeLoginProc{out: make(chan string, 8), end: make(chan error, 1), inputs: make(chan string, 4)}
	f.started = append(f.started, spec)
	f.procs = append(f.procs, p)
	return p, nil
}

func (f *fakeLogin) LoggedIn(context.Context, ports.LoginSpec) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loggedIn, f.checkErr
}

func (f *fakeLogin) last() *fakeLoginProc {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.procs[len(f.procs)-1]
}

func loginEnv(t *testing.T, opts ...opt) (*env, *fakeLogin) {
	t.Helper()
	e := newEnv(t, opts...)
	f := &fakeLogin{loggedIn: true}
	e.svc.d.Login = f
	// The scripted provider stands for the CLIs whose login can be followed.
	e.svc.d.Providers["claude"] = e.svc.d.Providers["fake"]
	e.svc.d.Providers["codex"] = e.svc.d.Providers["fake"]
	return e, f
}

func waitLogin(t *testing.T, e *env, account string, want func(domain.LoginSession) bool) domain.LoginSession {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, err := e.svc.LoginState(owner(), account)
		if err == nil && want(s) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("login of %s never got there: %+v (%v)", account, s, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestLoginOfANewAccountEntersTheRotation(t *testing.T) {
	e, f := loginEnv(t)
	id, err := e.svc.LoginAccountID("", "claude")
	if err != nil || id != "claude-1" {
		t.Fatalf("id %q, %v", id, err)
	}
	s, err := e.svc.Login(e.ctx, owner(), LoginOpStart, id, "claude", "")
	if err != nil {
		t.Fatal(err)
	}
	if s.State != domain.LoginRunning || s.Machine != "m1" || s.Account != "claude-1" || s.Provider != "claude" {
		t.Fatalf("started: %+v", s)
	}
	dir := filepath.Join(e.svc.d.DataDir, domain.AccountsDir, "claude-1")
	if f.started[0].Dir != dir || f.started[0].Provider != "claude" {
		t.Errorf("spec: %+v", f.started[0])
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("the login folder: %v %v", fi, err)
	}
	// Starting again while it runs answers the same login.
	if again, _ := e.svc.StartLogin(e.ctx, owner(), id, ""); again.StartedAt != s.StartedAt || len(f.started) != 1 {
		t.Errorf("a second start opened another login: %+v", again)
	}
	p := f.last()
	p.out <- "visit:\nhttps://claude.ai/oauth/authorize?x=1\nPaste code here if prompted > "
	s = waitLogin(t, e, id, func(s domain.LoginSession) bool { return s.WantsCode })
	if s.URL != "https://claude.ai/oauth/authorize?x=1" {
		t.Errorf("url %q", s.URL)
	}
	if s, err = e.svc.Login(e.ctx, owner(), LoginOpAnswer, id, "", "abc\ndef"); err == nil {
		t.Errorf("a code of two lines was taken: %+v", s)
	}
	if s, err = e.svc.Login(e.ctx, owner(), LoginOpAnswer, id, "", " abc#def "); err != nil || s.WantsCode {
		t.Fatalf("answer: %+v, %v", s, err)
	}
	if got := <-p.inputs; got != "abc#def" {
		t.Errorf("the login got %q", got)
	}
	p.finish(nil)
	s = waitLogin(t, e, id, func(s domain.LoginSession) bool { return !s.Running() })
	if s.State != domain.LoginDone || s.Error != "" || s.EndedAt.IsZero() {
		t.Fatalf("ended: %+v", s)
	}
	acc := e.p.Snapshot().Accounts["claude-1"]
	if acc == nil || acc.Provider != "claude" || acc.ConfigDir != "accounts/claude-1" || acc.Status != domain.AccountOK ||
		!domain.Contains(acc.Machines, "m1") || acc.Machine != "" || acc.CeilingPct != 100 {
		t.Fatalf("account: %+v", acc)
	}
	if got := acc.DirOn("m1", e.svc.d.DataDir); got != dir {
		t.Errorf("sessions would look in %q, the login is in %q", got, dir)
	}
	// The next new account takes the next id.
	if id, _ := e.svc.LoginAccountID("", "claude"); id != "claude-2" {
		t.Errorf("next id %q", id)
	}
	// A finished login is forgotten once nobody reads it any more.
	e.clock.Advance(loginKeep + time.Minute)
	if _, err := e.svc.LoginState(owner(), id); !errors.Is(err, ErrNotFound) {
		t.Errorf("kept after %s: %v", loginKeep, err)
	}
	if s, err := e.svc.Login(e.ctx, owner(), LoginOpState, id, "", ""); err != nil || s.State != "" || s.Machine != "m1" {
		t.Errorf("the state of no login: %+v, %v", s, err)
	}
}

func TestLoginOfAKnownAccountOnAnotherMachine(t *testing.T) {
	e, f := loginEnv(t, withMachine("m2"))
	pinned := domain.Account{ID: "claude-7", Provider: "claude", CeilingPct: 60, Machine: "m1", ConfigDir: "/home/ana/.claude", Status: domain.AccountOK,
		Usage: domain.Usage{WeeklyPct: 40}}
	if _, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), pinned); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StartLogin(e.ctx, owner(), "claude-7", "codex"); err == nil || !strings.Contains(err.Error(), "an account of claude") {
		t.Errorf("another provider: %v", err)
	}
	if _, err := e.svc.StartLogin(e.ctx, owner(), "claude-7", ""); err != nil {
		t.Fatal(err)
	}
	// The other machine keeps its own login, under its data dir.
	if want := filepath.Join(e.svc.d.DataDir, domain.AccountsDir, "claude-7"); f.started[0].Dir != want {
		t.Errorf("dir %q, want %q", f.started[0].Dir, want)
	}
	f.last().finish(nil)
	waitLogin(t, e, "claude-7", func(s domain.LoginSession) bool { return s.State == domain.LoginDone })
	acc := e.p.Snapshot().Accounts["claude-7"]
	if acc.Machine != "m1" || acc.ConfigDir != "/home/ana/.claude" || len(acc.Machines) != 1 || acc.Machines[0] != "m2" ||
		acc.CeilingPct != 60 || acc.Usage.WeeklyPct != 40 {
		t.Fatalf("account: %+v", acc)
	}
	if !acc.UsableOn("m1") || !acc.UsableOn("m2") || acc.UsableOn("m3") {
		t.Errorf("usable: %+v", acc)
	}
}

func TestLoginAgainPutsAnExpiredAccountInOrder(t *testing.T) {
	e, f := loginEnv(t)
	if _, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: "codex-1", Provider: "codex", CeilingPct: 100, Status: domain.AccountExpired}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StartLogin(e.ctx, owner(), "codex-1", ""); err != nil {
		t.Fatal(err)
	}
	// It names no machine: its login is the CLI's own, on every machine.
	if f.started[0].Dir != "" {
		t.Errorf("dir %q", f.started[0].Dir)
	}
	f.last().out <- "1. Open this link\n https://auth.openai.com/codex/device\n2. Enter this one-time code\n ABCD-12345\n"
	s := waitLogin(t, e, "codex-1", func(s domain.LoginSession) bool { return s.Code != "" })
	if s.Code != "ABCD-12345" || s.WantsCode {
		t.Errorf("session: %+v", s)
	}
	f.last().finish(nil)
	waitLogin(t, e, "codex-1", func(s domain.LoginSession) bool { return s.State == domain.LoginDone })
	acc := e.p.Snapshot().Accounts["codex-1"]
	if acc.Status != domain.AccountOK || len(acc.Machines) != 0 || acc.Machine != "" {
		t.Fatalf("account: %+v", acc)
	}
	// A record another machine overwrote is written again at the next heartbeat.
	lost := *acc
	lost.Status = domain.AccountExpired
	if _, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), lost); err != nil {
		t.Fatal(err)
	}
	e.svc.reconcileLogins(e.ctx)
	if got := e.p.Snapshot().Accounts["codex-1"].Status; got != domain.AccountOK {
		t.Errorf("after the heartbeat: %s", got)
	}
}

func TestLoginThatDoesNotSignIn(t *testing.T) {
	cases := []struct {
		name string
		set  func(*fakeLogin)
		end  error
		want string
	}{
		{"the login fails", func(*fakeLogin) {}, errors.New("exit status 1"), "exit status 1"},
		{"it ends without a login", func(f *fakeLogin) { f.loggedIn = false }, nil, "without signing in"},
		{"it cannot be checked", func(f *fakeLogin) { f.checkErr = errors.New("no claude") }, nil, "could not be checked: no claude"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, f := loginEnv(t)
			c.set(f)
			if _, err := e.svc.StartLogin(e.ctx, owner(), "claude-1", "claude"); err != nil {
				t.Fatal(err)
			}
			f.last().finish(c.end)
			s := waitLogin(t, e, "claude-1", func(s domain.LoginSession) bool { return !s.Running() })
			if s.State != domain.LoginFailed || !strings.Contains(s.Error, c.want) {
				t.Errorf("session: %+v", s)
			}
			if e.p.Snapshot().Accounts["claude-1"] != nil {
				t.Error("the account entered the rotation")
			}
			if _, err := e.svc.AnswerLogin(owner(), "claude-1", "code"); !errors.Is(err, ErrNotFound) {
				t.Errorf("a code for a login that ended: %v", err)
			}
		})
	}
	t.Run("signed in, but no project knows the provider", func(t *testing.T) {
		e, f := loginEnv(t)
		delete(e.svc.d.Providers, "claude")
		// StartLogin does not need the provider; the rotation does.
		if _, err := e.svc.StartLogin(e.ctx, owner(), "claude-1", "claude"); err != nil {
			t.Fatal(err)
		}
		f.last().finish(nil)
		s := waitLogin(t, e, "claude-1", func(s domain.LoginSession) bool { return !s.Running() })
		if s.State != domain.LoginFailed || !strings.Contains(s.Error, "did not enter the rotation") {
			t.Errorf("session: %+v", s)
		}
	})
}

func TestCancelLogin(t *testing.T) {
	e, f := loginEnv(t)
	if _, err := e.svc.CancelLogin(owner(), "claude-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("nothing to cancel: %v", err)
	}
	if _, err := e.svc.StartLogin(e.ctx, owner(), "claude-1", "claude"); err != nil {
		t.Fatal(err)
	}
	f.last().inErr = errors.New("closed pipe")
	if _, err := e.svc.AnswerLogin(owner(), "claude-1", "code"); err == nil || !strings.Contains(err.Error(), "closed pipe") {
		t.Errorf("a code the login did not take: %v", err)
	}
	if _, err := e.svc.Login(e.ctx, owner(), LoginOpCancel, "claude-1", "", ""); err != nil {
		t.Fatal(err)
	}
	s := waitLogin(t, e, "claude-1", func(s domain.LoginSession) bool { return !s.Running() })
	if s.State != domain.LoginCancelled || s.Error != "" {
		t.Errorf("session: %+v", s)
	}
	if e.p.Snapshot().Accounts["claude-1"] != nil {
		t.Error("a cancelled login entered the rotation")
	}
	// A daemon that stops leaves no login behind.
	if _, err := e.svc.StartLogin(e.ctx, owner(), "codex-1", "codex"); err != nil {
		t.Fatal(err)
	}
	e.svc.StopLogins()
	if s := waitLogin(t, e, "codex-1", func(s domain.LoginSession) bool { return !s.Running() }); s.State != domain.LoginCancelled {
		t.Errorf("after the daemon stopped: %+v", s)
	}
	// Cancelling what already ended changes nothing.
	if s, err := e.svc.CancelLogin(owner(), "claude-1"); err != nil || s.State != domain.LoginCancelled {
		t.Errorf("again: %+v, %v", s, err)
	}
}

func TestLoginRefusals(t *testing.T) {
	e, f := loginEnv(t)
	reader := Actor{Kind: ActorToken, Name: "reader", Scopes: []string{ScopeItemsRead}}
	for name, call := range map[string]func() error{
		"start":  func() error { _, err := e.svc.StartLogin(e.ctx, reader, "claude-1", "claude"); return err },
		"state":  func() error { _, err := e.svc.LoginState(reader, "claude-1"); return err },
		"answer": func() error { _, err := e.svc.AnswerLogin(reader, "claude-1", "x"); return err },
		"cancel": func() error { _, err := e.svc.CancelLogin(reader, "claude-1"); return err },
	} {
		if err := call(); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s by a token without admin: %v", name, err)
		}
	}
	for _, id := range []string{"", "../x", "a/b", ".hidden", strings.Repeat("a", 80)} {
		if _, err := e.svc.StartLogin(e.ctx, owner(), id, "claude"); !errors.Is(err, ErrInvalid) {
			t.Errorf("id %q: %v", id, err)
		}
	}
	if _, err := e.svc.StartLogin(e.ctx, owner(), "gemini-1", "gemini"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "barnraise account add") {
		t.Errorf("gemini: %v", err)
	}
	if _, err := e.svc.LoginAccountID("", "gemini"); !errors.Is(err, ErrInvalid) {
		t.Errorf("a new account of gemini: %v", err)
	}
	if id, err := e.svc.LoginAccountID("codex-9", ""); err != nil || id != "codex-9" {
		t.Errorf("a named account: %q, %v", id, err)
	}
	if _, err := e.svc.Login(e.ctx, owner(), "dance", "claude-1", "", ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown step: %v", err)
	}
	if _, err := e.svc.AnswerLogin(owner(), "claude-1", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a code for no login: %v", err)
	}
	f.startErr = errors.New("claude is not installed on this machine")
	if _, err := e.svc.StartLogin(e.ctx, owner(), "claude-1", "claude"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("start error: %v", err)
	}
	// A folder that is already there takes the id.
	if err := os.MkdirAll(filepath.Join(e.svc.d.DataDir, domain.AccountsDir, "codex-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if id := e.svc.NextAccountID("codex"); id != "codex-2" {
		t.Errorf("next id %q", id)
	}
	e.svc.d.Login = nil
	if _, err := e.svc.StartLogin(e.ctx, owner(), "claude-1", "claude"); !errors.Is(err, ErrInvalid) {
		t.Errorf("no login runner: %v", err)
	}
}

func TestLoggedInHere(t *testing.T) {
	if _, changed := loggedInHere(&domain.Account{ID: "a-1", Provider: "claude", Status: domain.AccountOK, Machine: "m1"}, "a-1", "claude", "m1"); changed {
		t.Error("the pinned machine logging in again changes nothing")
	}
	next, changed := loggedInHere(&domain.Account{ID: "a-1", Provider: "claude", Status: domain.AccountOK, Machines: []string{"m3"}, Outlook: &domain.AccountOutlook{}}, "a-1", "claude", "m1")
	if !changed || strings.Join(next.Machines, ",") != "m1,m3" || next.Outlook != nil {
		t.Errorf("a second machine: %+v", next)
	}
}
