package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

func line(i int) LiveLine { return LiveLine{Kind: "text", Text: fmt.Sprint("line ", i)} }

// Nobody watching: a publish only keeps the line in the ring.
func TestLiveFeedWithoutWatchersOnlyKeepsTheRing(t *testing.T) {
	f := newLiveFeed()
	f.publish(line(1))
	if f.watchers() != 0 || len(f.subs) != 0 {
		t.Fatal("no watcher was asked for")
	}
	back, ch, stop := f.subscribe()
	defer stop()
	if len(back) != 1 || back[0].Text != "line 1" {
		t.Fatalf("backlog %+v", back)
	}
	select {
	case l := <-ch:
		t.Fatalf("a line kept before the watch came live: %+v", l)
	default:
	}
}

// A watcher gets the backlog, then the new lines, until it stops.
func TestLiveFeedBacklogThenLiveThenStop(t *testing.T) {
	f := newLiveFeed()
	f.publish(line(1))
	f.publish(line(2))
	back, ch, stop := f.subscribe()
	if len(back) != 2 || back[0].Text != "line 1" || back[1].Text != "line 2" {
		t.Fatalf("backlog %+v", back)
	}
	f.publish(line(3))
	if l := <-ch; l.Text != "line 3" {
		t.Fatalf("live %+v", l)
	}
	if f.watchers() != 1 {
		t.Fatal("one watcher")
	}
	stop()
	stop() // twice is harmless
	if _, ok := <-ch; ok {
		t.Fatal("a stopped watch is closed")
	}
	f.publish(line(4)) // nobody left: no panic, nothing sent
	if f.watchers() != 0 {
		t.Fatal("the watcher is gone")
	}
}

// The ring keeps the last liveRing lines, oldest first; a slow watcher
// never blocks the session.
func TestLiveFeedIsBounded(t *testing.T) {
	f := newLiveFeed()
	_, ch, stop := f.subscribe()
	defer stop()
	n := liveRing + 50
	for i := range n {
		f.publish(line(i))
	}
	if len(ch) != liveSubBuffer {
		t.Fatalf("the slow watcher holds %d lines, want the %d of its buffer", len(ch), liveSubBuffer)
	}
	back, _, stop2 := f.subscribe()
	defer stop2()
	if len(back) != liveRing || back[0].Text != "line 50" || back[liveRing-1].Text != fmt.Sprint("line ", n-1) {
		t.Fatalf("ring of %d from %q to %q", len(back), back[0].Text, back[len(back)-1].Text)
	}
}

// The end reaches every watcher; a watch opened later gets the backlog and a closed channel.
func TestLiveFeedClose(t *testing.T) {
	f := newLiveFeed()
	f.publish(line(1))
	_, ch, _ := f.subscribe()
	f.close(time.Now(), "done")
	if l := <-ch; l.Kind != "end" {
		t.Fatalf("end %+v", l)
	}
	if _, ok := <-ch; ok {
		t.Fatal("closed")
	}
	f.publish(line(2))
	back, ch2, stop := f.subscribe()
	stop()
	if _, ok := <-ch2; ok || len(back) != 1 {
		t.Fatalf("after the end: %d lines", len(back))
	}
	var nilFeed *liveFeed
	nilFeed.publish(line(1))
	nilFeed.close(time.Now(), "")
}

func TestLiveLineOf(t *testing.T) {
	now := time.Now()
	long := strings.Repeat("x", 1000)
	cases := []struct {
		ev   ports.ProviderEvent
		kind string
		text string
		show bool
	}{
		{ports.ProviderEvent{Kind: ports.PEMessage, Text: "  hi  "}, "text", "hi", true},
		{ports.ProviderEvent{Kind: ports.PEMessage, Text: " "}, "", "", false},
		{ports.ProviderEvent{Kind: ports.PEToolUse, Tool: "Bash", Input: json.RawMessage(`{"command":"go  test ./...","timeout":5}`)}, "tool", "command: go test ./...", true},
		{ports.ProviderEvent{Kind: ports.PEToolUse, Tool: "X", Input: json.RawMessage(`{"n":1}`)}, "tool", `{"n":1}`, true},
		{ports.ProviderEvent{Kind: ports.PEToolUse, Tool: "X", Input: json.RawMessage(`[1, 2]`)}, "tool", `[1, 2]`, true},
		{ports.ProviderEvent{Kind: ports.PEToolResult, Text: long}, "result", strings.Repeat("x", liveTextMax) + "…", true},
		{ports.ProviderEvent{Kind: ports.PEError, Text: "boom"}, "error", "boom", true},
		{ports.ProviderEvent{Kind: ports.PELimit, Text: "limit"}, "limit", "limit", true},
		{ports.ProviderEvent{Kind: ports.PEPermission, Tool: "Edit"}, "error", "blocked: Edit", true},
		{ports.ProviderEvent{Kind: ports.PEUsage}, "", "", false},
	}
	for i, c := range cases {
		l, show := liveLineOf(c.ev, now)
		if show != c.show || (show && (l.Kind != c.kind || l.Text != c.text)) {
			t.Errorf("%d: %+v %v", i, l, show)
		}
	}
	if inputSummary(nil) != "" {
		t.Error("no input")
	}
}

// A running session is watched through the service: who it is, the lines
// it wrote before and the ones it writes next; its end closes the watch.
// Only the owner and admin tokens may watch; a session token never.
func TestWatchSessionOfARunningItem(t *testing.T) {
	e := newEnv(t)
	e.account("fake-1")
	it := e.ready("simple", "watch me", "x")
	wrote := make(chan struct{})
	more := make(chan struct{})
	release := make(chan struct{})
	e.prov.Handle(func(ctx context.Context, _ ports.SessionSpec, emit func(ports.ProviderEvent)) (ports.SessionResult, error) {
		emit(ports.ProviderEvent{Kind: ports.PEMessage, Text: "reading the code"})
		emit(ports.ProviderEvent{Kind: ports.PEToolUse, Tool: "Read", Input: json.RawMessage(`{"file_path":"main.go"}`)})
		emit(ports.ProviderEvent{Kind: ports.PEUsage, Usage: &domain.TokenUsage{Input: 1}})
		close(wrote)
		select {
		case <-more:
		case <-ctx.Done():
		}
		emit(ports.ProviderEvent{Kind: ports.PEToolResult, Text: "package main"})
		select {
		case <-release:
		case <-ctx.Done():
		}
		return ports.SessionResult{}, nil
	})
	e.fill()
	<-wrote
	if _, err := e.svc.WatchSession(e.ctx, agent("implementer", it.ID, "implement"), "demo", it.ID, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a session token must not watch: %v", err)
	}
	reader := Actor{Kind: ActorToken, Name: "ro", Scopes: []string{ScopeItemsRead, ScopeEvents}}
	if _, err := e.svc.WatchSession(e.ctx, reader, "demo", it.ID, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a token without admin must not watch: %v", err)
	}
	if _, err := e.svc.WatchLocalSession(agent("implementer", it.ID, "implement"), "demo", it.ID, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("local watch: %v", err)
	}
	var w *LiveWatch
	eventually(t, "the two lines", func() bool {
		var err error
		w, err = e.svc.WatchSession(e.ctx, owner(), "demo", strings.ToLower(it.ID), "")
		if err != nil {
			t.Fatal(err)
		}
		if len(w.Backlog) == 2 {
			return true
		}
		w.Stop()
		return false
	})
	if w.Session.ItemID != it.ID || w.Session.Machine != "m1" || w.Session.Role != "implementer" || w.Session.Step != "implement" || !strings.HasPrefix(w.Session.Model, "fake/") {
		t.Fatalf("session %+v", w.Session)
	}
	if w.Backlog[0].Text != "reading the code" || w.Backlog[1].Kind != "tool" || w.Backlog[1].Text != "file_path: main.go" {
		t.Fatalf("backlog %+v", w.Backlog)
	}
	close(more)
	if l := <-w.Lines; l.Kind != "result" || l.Text != "package main" {
		t.Fatalf("live %+v", l)
	}
	if _, err := e.svc.WatchLocalSession(owner(), "demo", "I-999", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no session: %v", err)
	}
	close(release)
	got := <-w.Lines
	if got.Kind != "end" {
		t.Fatalf("end %+v", got)
	}
	if _, ok := <-w.Lines; ok {
		t.Fatal("closed at the end")
	}
	e.idle()
	if _, err := e.svc.WatchSession(e.ctx, owner(), "demo", it.ID, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after the end: %v", err)
	}
	// Nothing of it reached the event log.
	for _, ev := range e.store.Events() {
		if strings.Contains(string(ev.Data), "reading the code") {
			t.Fatalf("a live line was stored: %s", ev.Type)
		}
	}
}

// A session on another machine is watched through the peer; without one,
// the error names the machine.
func TestWatchSessionOnAnotherMachine(t *testing.T) {
	e := newEnv(t)
	it := e.ready("simple", "far", "x")
	if _, err := e.svc.WatchSession(e.ctx, owner(), "demo", it.ID, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nothing runs: %v", err)
	}
	if _, err := e.svc.WatchSession(e.ctx, owner(), "nope", it.ID, ""); err == nil {
		t.Fatal("unknown project")
	}
	e.svc.emitEphemeral(e.p, domain.EvAgentSession, it.ID, "impl-1", domain.Payload{Session: &domain.SessionInfo{Agent: "impl-1", ItemID: it.ID, State: "working", Machine: "m2", At: e.clock.Now()}})
	_, err := e.svc.WatchSession(e.ctx, owner(), "demo", it.ID, "")
	var ee *ElsewhereError
	if !errors.As(err, &ee) || ee.Machine != "m2" || !strings.Contains(ee.Error(), "m2") {
		t.Fatalf("elsewhere: %v", err)
	}
	var asked []string
	lines := make(chan LiveLine)
	e.svc.SetLivePeer(func(_ context.Context, machine, project, item, agent string) (*LiveWatch, error) {
		asked = append(asked, machine+"/"+project+"/"+item+"/"+agent)
		if agent == "down" {
			return nil, errors.New("unreachable")
		}
		return &LiveWatch{Session: LiveSession{Machine: machine}, Lines: lines, Stop: func() {}}, nil
	})
	w, err := e.svc.WatchSession(e.ctx, owner(), "demo", it.ID, "")
	if err != nil || w.Session.Machine != "m2" {
		t.Fatalf("through the peer: %+v %v", w, err)
	}
	if _, err := e.svc.WatchSession(e.ctx, owner(), "demo", it.ID, "impl-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another agent: %v", err)
	}
	e.svc.emitEphemeral(e.p, domain.EvAgentSession, it.ID, "down", domain.Payload{Session: &domain.SessionInfo{Agent: "down", ItemID: it.ID, State: "working", Machine: "m3", At: e.clock.Now()}})
	if _, err := e.svc.WatchSession(e.ctx, owner(), "demo", it.ID, "down"); !errors.As(err, &ee) || ee.Machine != "m3" || !strings.Contains(ee.Why, "unreachable") {
		t.Fatalf("peer failure: %v", err)
	}
	if len(asked) != 2 || asked[0] != "m2/demo/"+it.ID+"/" {
		t.Fatalf("asked %v", asked)
	}
}

// A message to the item reaches the running session: its hooks are told at
// once (the mail flag), the inbox holds it, and the conversation lists it
// with the agent's answer. A message replicated from another machine flags too.
func TestMessageReachesARunningSession(t *testing.T) {
	e := newEnv(t)
	e.account("fake-1")
	it := e.ready("simple", "talk", "x")
	release := e.holdSessions()
	e.fill()
	var dir string
	eventually(t, "the session", func() bool {
		r := e.svc.Runtime()
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, ls := range r.running {
			dir = ls.dir
		}
		return dir != ""
	})
	flag := filepath.Join(dir, ".barnraise-session", domain.MailFlag)
	_ = os.Remove(flag)
	if _, err := e.svc.SendMessage(e.ctx, "demo", owner(), domain.Message{To: "item:" + it.ID, ItemID: it.ID, Text: "use the new API"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(flag); err != nil {
		t.Fatalf("the session's hooks were not told: %v", err)
	}
	ag := e.svc.Runtime().Sessions()[0]["agent"]
	in, err := e.svc.Inbox(e.ctx, "demo", ag, "implementer", it.ID, true)
	if err != nil || len(in) != 1 || in[0].Text != "use the new API" {
		t.Fatalf("inbox %+v %v", in, err)
	}
	if _, err := e.svc.SendMessage(e.ctx, "demo", agent("implementer", it.ID, "implement"), domain.Message{To: "owner", Text: "will do"}); err != nil {
		t.Fatal(err)
	}
	thread, err := e.svc.ItemMessages(owner(), "demo", it.ID, 10)
	if err != nil || len(thread) != 2 || thread[0].Text != "use the new API" || thread[1].Text != "will do" {
		t.Fatalf("thread %+v %v", thread, err)
	}
	if last, _ := e.svc.ItemMessages(owner(), "demo", it.ID, 1); len(last) != 1 || last[0].Text != "will do" {
		t.Fatalf("last %+v", last)
	}
	if _, err := e.svc.ItemMessages(agent("implementer", it.ID, "implement"), "demo", it.ID, 10); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a session token reads no conversation: %v", err)
	}

	_ = os.Remove(flag)
	m := domain.Message{ID: "remote-1", From: "owner", To: ag, ItemID: it.ID, Text: "from far", At: e.clock.Now()}
	ev := domain.NewEvent("m2-ev-1", domain.EvMessageSent, "demo", it.ID, "m2", "owner", e.clock.Now(), domain.Payload{Message: &m})
	if err := e.svc.Ingest(e.ctx, ev); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(flag); err != nil {
		t.Fatalf("a replicated message must flag the session: %v", err)
	}
	// A message for someone else flags nothing.
	_ = os.Remove(flag)
	if _, err := e.svc.SendMessage(e.ctx, "demo", owner(), domain.Message{To: "role:reviewer", ItemID: it.ID, Text: "not you"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(flag); err == nil {
		t.Fatal("a message for another role flagged the session")
	}
	release()
	e.idle()
}
