package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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

// fakeChat stands in for the Claude Code session.
type fakeChat struct {
	mu       sync.Mutex
	running  bool
	specs    []ports.ChatSpec
	sent     []string
	stops    int
	stopped  int
	startErr error
	entries  []ports.ChatEntry
	asked    []string // session ids Transcript was asked for
	rename   string   // a provider that names its own sessions (Codex)
	pairs    int
}

func (f *fakeChat) Start(spec ports.ChatSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return "", f.startErr
	}
	if f.running {
		return spec.SessionID, nil
	}
	f.running = true
	f.specs = append(f.specs, spec)
	if f.rename != "" && !spec.Resume {
		return f.rename + spec.SessionID[:4], nil
	}
	return spec.SessionID, nil
}
func (f *fakeChat) Send(text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, text)
	return nil
}
func (f *fakeChat) Interrupt() error { f.mu.Lock(); defer f.mu.Unlock(); f.stops++; return nil }
func (f *fakeChat) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running = false
	f.stopped++
	return nil
}
func (f *fakeChat) Status() ports.ChatStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return ports.ChatStatus{Running: f.running, RemoteURL: map[bool]string{true: "https://claude.ai/code/session_X"}[f.running]}
}
func (f *fakeChat) Transcript(_, id, dir string, after int) ([]ports.ChatEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, id)
	if after >= len(f.entries) {
		return []ports.ChatEntry{}, nil
	}
	return f.entries[after:], nil
}

func TestConversationIsOneSessionThatResumes(t *testing.T) {
	chat := &fakeChat{entries: []ports.ChatEntry{{Seq: 0, Role: "user", Text: "hi"}, {Seq: 1, Role: "assistant", Text: "hello"}}}
	e := newEnv(t, withChat(chat))
	owner := Owner("")

	v, err := e.svc.Conversation(e.ctx, owner, 0)
	if err != nil || !v.Available || v.Status.Running || len(v.Entries) != 2 {
		t.Fatalf("view %+v %v", v, err)
	}
	id := v.SessionID
	if len(id) != 36 || id[14] != '4' {
		t.Fatalf("a v4 uuid: %q", id)
	}
	if err := e.svc.ConversationSend(e.ctx, owner, "  "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty: %v", err)
	}
	if err := e.svc.ConversationSend(e.ctx, owner, "what is stuck?"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ConversationSend(e.ctx, owner, "and why?"); err != nil {
		t.Fatal(err)
	}
	if len(chat.specs) != 1 || strings.Join(chat.sent, "|") != "what is stuck?|and why?" {
		t.Fatalf("one session, two messages: %d %q", len(chat.specs), chat.sent)
	}
	spec := chat.specs[0]
	if spec.SessionID != id || spec.Resume || !strings.HasPrefix(spec.Name, "Barnraise · ") || !strings.Contains(spec.SystemPrompt, "barnraise status") ||
		!strings.HasSuffix(spec.Dir, filepath.Join("chat", "workspace")) {
		t.Fatalf("spec %+v", spec)
	}
	if !strings.Contains(strings.Join(spec.Env, " "), "BARNRAISE_SOCKET=") {
		t.Fatal("the session reaches this daemon")
	}
	var settings struct {
		Permissions struct {
			Allow, Deny []string
			Dirs        []string `json:"additionalDirectories"`
		} `json:"permissions"`
	}
	b, _ := os.ReadFile(spec.SettingsFile)
	if err := json.Unmarshal(b, &settings); err != nil || !slices.Contains(settings.Permissions.Allow, "Bash(barnraise:*)") || !slices.Contains(settings.Permissions.Deny, "Write") ||
		!slices.Equal(settings.Permissions.Dirs, []string{e.root}) {
		t.Fatalf("settings %s %v", b, err)
	}

	// The daemon restarts: the same conversation resumes.
	_ = e.svc.ConversationStop(e.ctx, owner)
	if err := e.svc.ConversationStart(e.ctx, owner); err != nil {
		t.Fatal(err)
	}
	if len(chat.specs) != 2 || !chat.specs[1].Resume || chat.specs[1].SessionID != id {
		t.Fatalf("resume: %+v", chat.specs)
	}
	if err := e.svc.ConversationInterrupt(e.ctx, owner); err != nil || chat.stops != 1 {
		t.Fatal("interrupt")
	}
	if v, _ := e.svc.Conversation(e.ctx, owner, 1); len(v.Entries) != 1 || v.Status.RemoteURL == "" {
		t.Fatalf("after 1: %+v", v)
	}

	// A new conversation: a new id, started fresh.
	if err := e.svc.ConversationNew(e.ctx, owner); err != nil {
		t.Fatal(err)
	}
	last := chat.specs[len(chat.specs)-1]
	if last.SessionID == id || last.Resume {
		t.Fatalf("new: %+v", last)
	}
}

func TestConversationIsTheOwners(t *testing.T) {
	e := newEnv(t, withChat(&fakeChat{}))
	agent := Actor{Kind: ActorAgent, Project: "demo", Scopes: AllScopes}
	for name, call := range map[string]func() error{
		"view":      func() error { _, err := e.svc.Conversation(e.ctx, agent, 0); return err },
		"send":      func() error { return e.svc.ConversationSend(e.ctx, agent, "x") },
		"start":     func() error { return e.svc.ConversationStart(e.ctx, agent) },
		"stop":      func() error { return e.svc.ConversationStop(e.ctx, agent) },
		"interrupt": func() error { return e.svc.ConversationInterrupt(e.ctx, agent) },
		"new":       func() error { return e.svc.ConversationNew(e.ctx, agent) },
		"keep":      func() error { return e.svc.ConversationKeep(e.ctx, agent, true) },
	} {
		if err := call(); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s by an agent: %v", name, err)
		}
	}
}

func TestNoConversationOnThisMachine(t *testing.T) {
	e := newEnv(t)
	owner := Owner("")
	v, err := e.svc.Conversation(e.ctx, owner, 0)
	if err != nil || v.Available || v.Why == "" {
		t.Fatalf("%+v %v", v, err)
	}
	for _, err := range []error{e.svc.ConversationSend(e.ctx, owner, "x"), e.svc.ConversationStop(e.ctx, owner),
		e.svc.ConversationInterrupt(e.ctx, owner), e.svc.ConversationNew(e.ctx, owner), e.svc.ConversationStart(e.ctx, owner)} {
		if !errors.Is(err, ErrNoConversation) {
			t.Fatalf("got %v", err)
		}
	}
	e.svc.StartConversationIfKept(e.ctx) // returns at once
}

func TestKeptConversationRunsFromTheStart(t *testing.T) {
	chat := &fakeChat{}
	e := newEnv(t, withChat(chat))
	owner := Owner("")
	if err := e.svc.ConversationKeep(e.ctx, owner, true); err != nil || !chat.Status().Running {
		t.Fatalf("keep starts it: %v", err)
	}
	_ = chat.Stop() // it ends on its own
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { e.svc.StartConversationIfKept(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for !chat.Status().Running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !chat.Status().Running {
		t.Fatal("a kept session starts again")
	}
	cancel()
	<-done
	if chat.Status().Running {
		t.Fatal("the daemon stopping stops the session")
	}
	if err := e.svc.ConversationKeep(e.ctx, owner, false); err != nil {
		t.Fatal(err)
	}
	if v, _ := e.svc.Conversation(e.ctx, owner, 0); v.Keep {
		t.Fatal("released")
	}
}

func TestConversationStartFails(t *testing.T) {
	e := newEnv(t, withChat(&fakeChat{startErr: errors.New("claude is not installed")}))
	if err := e.svc.ConversationSend(e.ctx, Owner(""), "x"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("got %v", err)
	}
}

func writeLogin(t *testing.T, dir, email string) {
	t.Helper()
	_ = os.MkdirAll(dir, 0o700)
	b, _ := json.Marshal(map[string]any{"oauthAccount": map[string]string{"emailAddress": email, "displayName": "Ana", "billingType": "stripe_subscription"}})
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The owner picks the account the conversation runs as: the phone shows it
// in the Claude app signed in to that account. Each keeps its conversation.
func TestConversationRunsAsTheChosenAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeLogin(t, home, "me@home.dev")
	chat := &fakeChat{}
	e := newEnv(t, withChat(chat))
	owner := Owner("")
	e.svc.d.Providers["claude"], e.svc.d.Providers["codex"] = e.prov, e.prov
	work := filepath.Join(t.TempDir(), "claude-2")
	writeLogin(t, work, "work@corp.dev")
	for _, acc := range []domain.Account{
		{ID: "claude-2", Provider: "claude", ConfigDir: work, CeilingPct: 100, Usage: domain.Usage{WeeklyPct: 40}},
		{ID: "claude-3", Provider: "claude", ConfigDir: t.TempDir(), CeilingPct: 100},
		{ID: "codex-1", Provider: "codex", ConfigDir: t.TempDir(), CeilingPct: 100},
		{ID: "claude-9", Provider: "claude", ConfigDir: t.TempDir(), CeilingPct: 100, Machine: "elsewhere"},
	} {
		if _, err := e.svc.UpsertAccount(e.ctx, "demo", owner, acc); err != nil {
			t.Fatal(err)
		}
	}
	v, _ := e.svc.Conversation(e.ctx, owner, 0)
	byID := map[string]ConversationAccount{}
	for _, a := range v.Accounts {
		byID[a.ID] = a
	}
	if len(v.Accounts) != 4 || !byID[""].Usable || byID[""].Email != "me@home.dev" || !byID["claude-2"].Usable || byID["claude-2"].Email != "work@corp.dev" ||
		byID["claude-2"].Week != 40 || byID["claude-3"].Usable || byID["codex-1"].Usable || byID["codex-1"].Why == "" {
		t.Fatalf("accounts %+v", v.Accounts)
	}
	if _, other := byID["claude-9"]; other {
		t.Fatal("an account of another machine is not offered")
	}
	if err := e.svc.ConversationSend(e.ctx, owner, "hi"); err != nil {
		t.Fatal(err)
	}
	homeID := chat.specs[0].SessionID
	if chat.specs[0].ConfigDir != "" {
		t.Fatalf("the own login: %q", chat.specs[0].ConfigDir)
	}
	if err := e.svc.ConversationUse(e.ctx, owner, "codex-1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("codex: %v", err)
	}
	if err := e.svc.ConversationUse(e.ctx, owner, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	if err := e.svc.ConversationUse(e.ctx, owner, "claude-2"); err != nil {
		t.Fatal(err)
	}
	last := chat.specs[len(chat.specs)-1]
	if last.ConfigDir != work || last.SessionID == homeID || last.Resume {
		t.Fatalf("as claude-2: %+v", last)
	}
	if v, _ := e.svc.Conversation(e.ctx, owner, 0); v.Account != "claude-2" || v.SessionID != last.SessionID {
		t.Fatalf("view %+v", v)
	}
	if err := e.svc.ConversationUse(e.ctx, owner, "claude-2"); err != nil || len(chat.specs) != 2 {
		t.Fatal("the same account again changes nothing")
	}
	// Back to the own login: its conversation resumes.
	if err := e.svc.ConversationUse(e.ctx, owner, ""); err != nil {
		t.Fatal(err)
	}
	back := chat.specs[len(chat.specs)-1]
	if back.SessionID != homeID || !back.Resume || back.ConfigDir != "" {
		t.Fatalf("back: %+v", back)
	}
	if err := e.svc.ConversationUse(e.ctx, Actor{Kind: ActorAgent, Scopes: AllScopes}, "claude-2"); !errors.Is(err, ErrForbidden) {
		t.Fatal("an agent cannot switch")
	}
}

// fakeCodex names its own threads and pairs the ChatGPT app with a code.
type fakeCodex struct{ fakeChat }

func (f *fakeCodex) Pair(configDir string) (ports.ChatPairing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pairs++
	return ports.ChatPairing{Code: "123", Manual: "AB12-CD34", Expires: time.Now().Add(10 * time.Minute)}, nil
}

func writeCodexLogin(t *testing.T, dir, email string) {
	t.Helper()
	_ = os.MkdirAll(dir, 0o700)
	claims, _ := json.Marshal(map[string]any{"email": email, "https://api.openai.com/auth": map[string]string{"chatgpt_plan_type": "plus"}})
	token := "h." + base64.RawURLEncoding.EncodeToString(claims) + ".s"
	b, _ := json.Marshal(map[string]any{"tokens": map[string]string{"id_token": token}})
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The conversation runs on ChatGPT (Codex) as well: its own login or a Codex
// account, the phone paired with a code; switching between Claude and
// ChatGPT stops one and resumes the other's conversation.
func TestConversationOnChatGPT(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	writeLogin(t, home, "me@home.dev")
	writeCodexLogin(t, filepath.Join(home, ".codex"), "me@openai.dev")
	claude, codex := &fakeChat{}, &fakeCodex{fakeChat{rename: "th-"}}
	e := newEnv(t, withChat(claude), withChatCodex(codex))
	e.svc.d.Providers["codex"] = e.prov
	work := t.TempDir()
	writeCodexLogin(t, work, "work@openai.dev")
	owner := Owner("")
	if _, err := e.svc.UpsertAccount(e.ctx, "demo", owner, domain.Account{ID: "codex-2", Provider: "codex", ConfigDir: work, CeilingPct: 100}); err != nil {
		t.Fatal(err)
	}
	v, _ := e.svc.Conversation(e.ctx, owner, 0)
	byID := map[string]ConversationAccount{}
	for _, a := range v.Accounts {
		byID[a.ID] = a
	}
	if !byID[OwnCodexLogin].Usable || byID[OwnCodexLogin].Email != "me@openai.dev" || byID[OwnCodexLogin].Plan != "plus" ||
		!byID["codex-2"].Usable || byID["codex-2"].Email != "work@openai.dev" {
		t.Fatalf("accounts %+v", v.Accounts)
	}
	if _, err := e.svc.ConversationPair(e.ctx, owner); !errors.Is(err, ErrInvalid) {
		t.Fatalf("claude needs no pairing: %v", err)
	}
	if err := e.svc.ConversationSend(e.ctx, owner, "hi claude"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ConversationUse(e.ctx, owner, OwnCodexLogin); err != nil {
		t.Fatal(err)
	}
	if claude.Status().Running || !codex.Status().Running {
		t.Fatal("ChatGPT runs, Claude stopped")
	}
	if err := e.svc.ConversationSend(e.ctx, owner, "hi chatgpt"); err != nil || len(codex.sent) != 1 {
		t.Fatalf("send to codex: %v %v", err, codex.sent)
	}
	v, _ = e.svc.Conversation(e.ctx, owner, 0)
	if v.Account != OwnCodexLogin || !strings.HasPrefix(v.SessionID, "th-") {
		t.Fatalf("the thread id Codex gave is kept: %+v", v)
	}
	p, err := e.svc.ConversationPair(e.ctx, owner)
	if err != nil || p.Manual != "AB12-CD34" || codex.pairs != 1 {
		t.Fatalf("pair %+v %v", p, err)
	}
	if err := e.svc.ConversationInterrupt(e.ctx, owner); err != nil || codex.stops != 1 {
		t.Fatal("interrupt reaches codex")
	}
	if err := e.svc.ConversationUse(e.ctx, owner, "codex-2"); err != nil || codex.specs[len(codex.specs)-1].ConfigDir != work {
		t.Fatalf("codex-2: %v", err)
	}
	if err := e.svc.ConversationUse(e.ctx, owner, ""); err != nil {
		t.Fatal(err)
	}
	if !claude.Status().Running || codex.Status().Running || !claude.specs[len(claude.specs)-1].Resume {
		t.Fatal("back on Claude, its conversation resumes")
	}
	if _, err := e.svc.ConversationPair(e.ctx, Actor{Kind: ActorAgent, Scopes: AllScopes}); !errors.Is(err, ErrForbidden) {
		t.Fatal("an agent cannot pair")
	}
}

// Only Codex on this machine: the conversation runs there.
func TestConversationOnlyOnCodex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	codex := &fakeCodex{}
	e := newEnv(t, withChatCodex(codex))
	if err := e.svc.ConversationSend(e.ctx, Owner(""), "hi"); err != nil || len(codex.sent) != 1 {
		t.Fatalf("%v %v", err, codex.sent)
	}
	if got := codexLoginEmail(t, "not json"); got != "" {
		t.Fatal(got)
	}
}

func codexLoginEmail(t *testing.T, content string) string {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "auth.json"), []byte(content), 0o600)
	email, _ := codexLogin(dir)
	return email
}
