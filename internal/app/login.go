package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// The owner logs an account in on many machines at once: every machine runs
// the provider's own login in the account's folder there, and the panel
// shows each one's link and code side by side. A machine keeps its own
// login; no credential travels between machines.

// loginTimeout ends a login nobody finished; loginKeep is how long a
// finished one stays to be read.
const (
	loginTimeout = 20 * time.Minute
	loginKeep    = 30 * time.Minute
)

// accountIDRe is what an account id may be: it names a folder.
var accountIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)

type loginRegistry struct {
	mu   sync.Mutex
	runs map[string]*loginRun
}

type loginRun struct {
	mu     sync.Mutex
	s      domain.LoginSession
	proc   ports.LoginProcess
	by     Actor
	closed bool // cancelled by the owner
}

func (r *loginRun) state() domain.LoginSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.s
	c.Lines = append([]string(nil), r.s.Lines...)
	return c
}

// get returns the run of an account, dropping the finished ones that
// nobody reads any more.
func (g *loginRegistry) get(account string, now time.Time) *loginRun {
	g.mu.Lock()
	defer g.mu.Unlock()
	for id, r := range g.runs {
		s := r.state()
		if !s.Running() && now.Sub(s.EndedAt) > loginKeep {
			delete(g.runs, id)
		}
	}
	return g.runs[account]
}

// knownAccount finds an account in any project of this machine.
func (s *Service) knownAccount(id string) *domain.Account {
	for _, name := range s.Projects() {
		p, err := s.Project(name)
		if err != nil {
			continue
		}
		if a := p.Snapshot().Accounts[id]; a != nil {
			return a
		}
	}
	return nil
}

// NextAccountID is the first free id of a provider (claude-1, claude-2…):
// no project knows it and no login folder of this machine has its name.
func (s *Service) NextAccountID(provider string) string {
	for i := 1; ; i++ {
		id := fmt.Sprintf("%s-%d", provider, i)
		if s.knownAccount(id) != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(s.d.DataDir, domain.AccountsDir, id)); err == nil {
			continue
		}
		return id
	}
}

// StartLogin starts the provider's login of an account on this machine and
// returns at once: the owner follows it with LoginState. An account no
// project knows yet is added to the rotation when the login ends well.
func (s *Service) StartLogin(ctx context.Context, a Actor, account, provider string) (domain.LoginSession, error) {
	if err := a.Require(ScopeAdmin, ""); err != nil {
		return domain.LoginSession{}, err
	}
	if s.d.Login == nil {
		return domain.LoginSession{}, fmt.Errorf("%w: this daemon cannot run a provider's login", ErrInvalid)
	}
	if !accountIDRe.MatchString(account) {
		return domain.LoginSession{}, fmt.Errorf("%w: %q is not an account id (letters, digits, - and _)", ErrInvalid, account)
	}
	// A login of the account that already runs here is the one to follow.
	now := s.d.Clock.Now().UTC()
	if r := s.logins.get(account, now); r != nil {
		if cur := r.state(); cur.Running() && (provider == "" || provider == cur.Provider) {
			return cur, nil
		}
	}
	known := s.knownAccount(account)
	if known != nil {
		if provider != "" && provider != known.Provider {
			return domain.LoginSession{}, fmt.Errorf("%w: %s is an account of %s, not of %s", ErrInvalid, account, known.Provider, provider)
		}
		provider = known.Provider
	}
	if !domain.Contains(domain.LoginProviders, provider) {
		return domain.LoginSession{}, fmt.Errorf("%w: the login of %q cannot be followed from here (it works for %s): run `barnraise account add` on the machine itself",
			ErrInvalid, provider, strings.Join(domain.LoginProviders, ", "))
	}
	dir := filepath.Join(s.d.DataDir, domain.AccountsDir, account)
	if known != nil {
		dir = known.DirOn(s.d.Machine, s.d.DataDir)
	}
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return domain.LoginSession{}, err
		}
	}
	// The login outlives the request that started it.
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loginTimeout)
	spec := ports.LoginSpec{Provider: provider, Dir: dir}
	proc, err := s.d.Login.Start(lctx, spec)
	if err != nil {
		cancel()
		return domain.LoginSession{}, fmt.Errorf("the login of %s did not start: %w", provider, err)
	}
	run := &loginRun{proc: proc, by: a, s: domain.LoginSession{Machine: s.d.Machine, Account: account, Provider: provider, State: domain.LoginRunning, StartedAt: now}}
	s.logins.mu.Lock()
	if s.logins.runs == nil {
		s.logins.runs = map[string]*loginRun{}
	}
	s.logins.runs[account] = run
	s.logins.mu.Unlock()
	go func() {
		defer cancel()
		s.followLogin(lctx, run, spec)
	}()
	return run.state(), nil
}

// followLogin reads the login until it ends, checks that the folder holds a
// login that works and puts the account in the rotation.
func (s *Service) followLogin(ctx context.Context, run *loginRun, spec ports.LoginSpec) {
	for out := range run.proc.Output() {
		run.mu.Lock()
		run.s.Read(out)
		run.mu.Unlock()
	}
	err := run.proc.Wait()
	state, why := domain.LoginDone, ""
	run.mu.Lock()
	closed := run.closed
	run.mu.Unlock()
	switch {
	case closed:
		state = domain.LoginCancelled
	case ctx.Err() != nil:
		state, why = domain.LoginFailed, "nobody finished the login in "+loginTimeout.String()
	case err != nil:
		state, why = domain.LoginFailed, err.Error()
	default:
		ok, cerr := s.d.Login.LoggedIn(context.WithoutCancel(ctx), spec)
		switch {
		case cerr != nil:
			state, why = domain.LoginFailed, "the login ended, but it could not be checked: "+cerr.Error()
		case !ok:
			state, why = domain.LoginFailed, "the login ended without signing in"
		}
	}
	if state == domain.LoginDone {
		if err := s.adoptLogin(context.WithoutCancel(ctx), run.by, run.s.Account, run.s.Provider); err != nil {
			state, why = domain.LoginFailed, "signed in, but the account did not enter the rotation: "+err.Error()
		}
	}
	run.mu.Lock()
	run.s.State, run.s.Error, run.s.WantsCode, run.s.EndedAt = state, why, false, s.d.Clock.Now().UTC()
	run.mu.Unlock()
}

// adoptLogin records, in every project of this machine, that the account is
// logged in here: a new account enters the rotation with its login under
// each machine's data dir; a known one gains this machine and is in order
// again when its login had expired.
func (s *Service) adoptLogin(ctx context.Context, a Actor, account, provider string) error {
	var errs []error
	added := 0
	for _, name := range s.Projects() {
		p, err := s.Project(name)
		if err != nil {
			continue
		}
		next, changed := loggedInHere(p.Snapshot().Accounts[account], account, provider, s.d.Machine)
		if !changed {
			added++
			continue
		}
		if _, err := s.UpsertAccount(ctx, name, a, next); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		added++
	}
	if added == 0 {
		if len(errs) == 0 {
			return errors.New("this machine has no project")
		}
		return errors.Join(errs...)
	}
	return nil
}

// loggedInHere is the account once it is logged in on the machine, and
// whether that changes what the log holds.
func loggedInHere(cur *domain.Account, id, provider, machine string) (domain.Account, bool) {
	if cur == nil {
		return domain.Account{ID: id, Provider: provider, ConfigDir: domain.AccountsDir + "/" + id, CeilingPct: 100,
			Status: domain.AccountOK, Machines: []string{machine}}, true
	}
	next, changed := *cur, false
	next.Outlook = nil
	// An account that names no machine runs anywhere: it stays that way.
	anywhere := cur.Machine == "" && len(cur.Machines) == 0
	if !anywhere && !cur.UsableOn(machine) {
		next.Machines = append(append([]string(nil), cur.Machines...), machine)
		sort.Strings(next.Machines)
		changed = true
	}
	if cur.Status == domain.AccountExpired {
		next.Status, changed = domain.AccountOK, true
	}
	return next, changed
}

// reconcileLogins repeats what a finished login recorded when another
// machine's record of the same account, written at the same moment, took
// its place.
func (s *Service) reconcileLogins(ctx context.Context) {
	s.logins.mu.Lock()
	var done []*loginRun
	for _, r := range s.logins.runs {
		done = append(done, r)
	}
	s.logins.mu.Unlock()
	for _, r := range done {
		if st := r.state(); st.State == domain.LoginDone {
			if err := s.adoptLogin(ctx, r.by, st.Account, st.Provider); err != nil && s.d.Log != nil {
				s.d.Log.Debug("login record", "account", st.Account, "err", err)
			}
		}
	}
}

// The steps of a login, as the API and the machines name them.
const (
	LoginOpStart  = "start"
	LoginOpState  = "state"
	LoginOpAnswer = "answer"
	LoginOpCancel = "cancel"
)

// LoginAccountID is the account a login is about: the one named, or the
// first free id of the provider for a new account.
func (s *Service) LoginAccountID(account, provider string) (string, error) {
	if account != "" {
		return account, nil
	}
	if !domain.Contains(domain.LoginProviders, provider) {
		return "", fmt.Errorf("%w: name the account, or the provider of a new one (%s)", ErrInvalid, strings.Join(domain.LoginProviders, ", "))
	}
	return s.NextAccountID(provider), nil
}

// Login runs one step of an account's login on this machine. Asked for the
// state of a login that does not run here, it answers an empty session.
func (s *Service) Login(ctx context.Context, a Actor, op, account, provider, code string) (domain.LoginSession, error) {
	switch op {
	case LoginOpStart:
		return s.StartLogin(ctx, a, account, provider)
	case LoginOpState:
		st, err := s.LoginState(a, account)
		if errors.Is(err, ErrNotFound) {
			return domain.LoginSession{Machine: s.d.Machine, Account: account}, nil
		}
		return st, err
	case LoginOpAnswer:
		return s.AnswerLogin(a, account, code)
	case LoginOpCancel:
		return s.CancelLogin(a, account)
	}
	return domain.LoginSession{}, fmt.Errorf("%w: unknown login step %q", ErrInvalid, op)
}

// StopLogins ends the logins that still run: the daemon is stopping, and a
// login nobody can follow must not stay behind.
func (s *Service) StopLogins() {
	s.logins.mu.Lock()
	var runs []*loginRun
	for _, r := range s.logins.runs {
		runs = append(runs, r)
	}
	s.logins.mu.Unlock()
	for _, r := range runs {
		r.mu.Lock()
		running := r.s.Running()
		if running {
			r.closed = true
		}
		r.mu.Unlock()
		if running {
			r.proc.Stop()
		}
	}
}

// LoginState tells how the login of an account goes on this machine.
func (s *Service) LoginState(a Actor, account string) (domain.LoginSession, error) {
	if err := a.Require(ScopeAdmin, ""); err != nil {
		return domain.LoginSession{}, err
	}
	r := s.logins.get(account, s.d.Clock.Now().UTC())
	if r == nil {
		return domain.LoginSession{}, fmt.Errorf("%w: no login of %s runs on %s", ErrNotFound, account, s.d.Machine)
	}
	return r.state(), nil
}

// AnswerLogin hands the login the code the page showed after signing in.
func (s *Service) AnswerLogin(a Actor, account, code string) (domain.LoginSession, error) {
	if err := a.Require(ScopeAdmin, ""); err != nil {
		return domain.LoginSession{}, err
	}
	code = strings.TrimSpace(code)
	if code == "" || strings.ContainsAny(code, "\r\n") {
		return domain.LoginSession{}, fmt.Errorf("%w: paste the code the page showed, in one line", ErrInvalid)
	}
	r := s.logins.get(account, s.d.Clock.Now().UTC())
	if st := (domain.LoginSession{}); r != nil {
		st = r.state()
		if !st.Running() {
			r = nil
		}
	}
	if r == nil {
		return domain.LoginSession{}, fmt.Errorf("%w: no login of %s runs on %s", ErrNotFound, account, s.d.Machine)
	}
	if err := r.proc.Input(code); err != nil {
		return domain.LoginSession{}, fmt.Errorf("the login did not take the code: %w", err)
	}
	r.mu.Lock()
	r.s.WantsCode = false
	r.mu.Unlock()
	return r.state(), nil
}

// CancelLogin stops the login of an account on this machine.
func (s *Service) CancelLogin(a Actor, account string) (domain.LoginSession, error) {
	if err := a.Require(ScopeAdmin, ""); err != nil {
		return domain.LoginSession{}, err
	}
	r := s.logins.get(account, s.d.Clock.Now().UTC())
	if r == nil {
		return domain.LoginSession{}, fmt.Errorf("%w: no login of %s runs on %s", ErrNotFound, account, s.d.Machine)
	}
	r.mu.Lock()
	running := r.s.Running()
	if running {
		r.closed = true
	}
	r.mu.Unlock()
	if running {
		r.proc.Stop()
	}
	return r.state(), nil
}
