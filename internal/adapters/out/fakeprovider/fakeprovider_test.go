package fakeprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/provcommon"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

func drain(s ports.Session) []ports.ProviderEvent {
	var evs []ports.ProviderEvent
	for e := range s.Events() {
		evs = append(evs, e)
	}
	return evs
}

func TestDetectAndIdentity(t *testing.T) {
	p := New("claude", "anthropic")
	caps, _ := p.Detect(context.Background())
	if !caps.Stream || !caps.Hooks || !caps.UsageLimits || !caps.Images || p.Name() != "claude" || p.Family() != "anthropic" {
		t.Fatalf("caps = %+v", caps)
	}
	p.SetCapabilities(ports.Capabilities{Version: "x"})
	if caps, _ := p.Detect(context.Background()); caps.Stream {
		t.Fatal("override ignored")
	}
}

func TestDefaultBehavior(t *testing.T) {
	p := New("f", "x")
	s, err := p.Open(context.Background(), ports.SessionSpec{Prompt: "hello", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	evs := drain(s)
	res, err := s.Wait()
	if err != nil || res.Text != "ok" || res.SessionID != "fake-f-1" {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if len(evs) != 3 || evs[2].Kind != ports.PEDone {
		t.Fatalf("events = %+v", evs)
	}
	if specs := p.Specs(); len(specs) != 1 || specs[0].Prompt != "hello" {
		t.Fatalf("specs = %+v", specs)
	}
}

func TestReplay(t *testing.T) {
	p := New("claude", "anthropic").Replay("../claude/testdata/session_tool.jsonl")
	s, err := p.Open(context.Background(), ports.SessionSpec{Account: domain.Account{ID: "claude-9"}})
	if err != nil {
		t.Fatal(err)
	}
	evs := drain(s)
	res, err := s.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "hi" || res.Usage.Account != "claude-9" || res.Usage.Output == 0 {
		t.Fatalf("res = %+v", res)
	}
	var uses, dones int
	for _, e := range evs {
		switch e.Kind {
		case ports.PEToolUse:
			uses++
		case ports.PEDone:
			dones++
		}
		if len(e.Raw) == 0 {
			t.Fatalf("event without raw: %+v", e)
		}
	}
	if uses != 1 || dones != 1 {
		t.Fatalf("uses = %d dones = %d", uses, dones)
	}
}

func TestReplayMissingFile(t *testing.T) {
	p := New("claude", "anthropic").Replay("/nonexistent.jsonl")
	s, _ := p.Open(context.Background(), ports.SessionSpec{})
	drain(s)
	if _, err := s.Wait(); err == nil {
		t.Fatal("want error")
	}
}

func TestHandlerEditsWorkdir(t *testing.T) {
	dir := t.TempDir()
	p := New("codex", "openai").Handle(func(ctx context.Context, spec ports.SessionSpec, emit func(ports.ProviderEvent)) (ports.SessionResult, error) {
		emit(ports.ProviderEvent{Kind: ports.PEToolUse, Tool: "Write"})
		if err := os.WriteFile(filepath.Join(spec.Workdir, "out.txt"), []byte(spec.Prompt), 0o600); err != nil {
			return ports.SessionResult{}, err
		}
		emit(ports.ProviderEvent{Kind: ports.PEDone, Text: "written"})
		return ports.SessionResult{Text: "written", SessionID: "s-1", Usage: domain.TokenUsage{Output: 7}}, nil
	})
	s, err := p.Open(context.Background(), ports.SessionSpec{Workdir: dir, Prompt: "content"})
	if err != nil {
		t.Fatal(err)
	}
	evs := drain(s)
	res, err := s.Wait()
	if err != nil || res.SessionID != "s-1" || res.Usage.Output != 7 {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if len(evs) != 2 {
		t.Fatalf("handler emitted done; no extra done expected: %+v", evs)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "out.txt")); string(b) != "content" {
		t.Fatalf("file = %q", b)
	}
}

func TestStopAndTimeout(t *testing.T) {
	block := func(ctx context.Context, _ ports.SessionSpec, emit func(ports.ProviderEvent)) (ports.SessionResult, error) {
		emit(ports.ProviderEvent{Kind: ports.PEInit})
		<-ctx.Done()
		return ports.SessionResult{}, ctx.Err()
	}
	p := New("f", "x").Handle(block)
	s, _ := p.Open(context.Background(), ports.SessionSpec{})
	<-s.Events()
	_ = s.Stop()
	if _, err := s.Wait(); !errors.Is(err, provcommon.ErrStopped) {
		t.Fatalf("err = %v", err)
	}
	s, _ = p.Open(context.Background(), ports.SessionSpec{Timeout: 50 * time.Millisecond})
	if _, err := s.Wait(); !errors.Is(err, provcommon.ErrTimeout) {
		t.Fatalf("err = %v", err)
	}
}

func TestPanicAndFailOpen(t *testing.T) {
	p := New("f", "x").Handle(func(context.Context, ports.SessionSpec, func(ports.ProviderEvent)) (ports.SessionResult, error) {
		panic("boom")
	})
	s, _ := p.Open(context.Background(), ports.SessionSpec{})
	drain(s)
	if _, err := s.Wait(); err == nil {
		t.Fatal("want error")
	}
	p.FailOpen(errors.New("no login"))
	if _, err := p.Open(context.Background(), ports.SessionSpec{Prompt: "p2"}); err == nil {
		t.Fatal("want error")
	}
	if len(p.Specs()) != 2 {
		t.Fatal("failed opens are recorded too")
	}
}

func TestConcurrentOpens(t *testing.T) {
	p := New("f", "x").Handle(func(ctx context.Context, spec ports.SessionSpec, emit func(ports.ProviderEvent)) (ports.SessionResult, error) {
		emit(ports.ProviderEvent{Kind: ports.PEMessage, Text: spec.Prompt})
		return ports.SessionResult{Text: spec.Prompt}, nil
	})
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			s, err := p.Open(context.Background(), ports.SessionSpec{Prompt: "x"})
			if err != nil {
				t.Error(err)
				return
			}
			// Not reading Events must not block Wait.
			if _, err := s.Wait(); err != nil {
				t.Error(err)
			}
			_ = p.Specs()
		})
	}
	wg.Wait()
	if len(p.Specs()) != 20 {
		t.Fatalf("specs = %d", len(p.Specs()))
	}
}
