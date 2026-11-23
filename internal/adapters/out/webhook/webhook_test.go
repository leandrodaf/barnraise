package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func testEvent() domain.Event {
	return domain.NewEvent("ev-1", domain.EvItemCreated, "nobira", "I-42", "laptop", "owner", t0, domain.Payload{Note: "hi"})
}

func TestToCloudEvent(t *testing.T) {
	ce := ToCloudEvent(testEvent(), "")
	if ce.SpecVersion != "1.0" || ce.ID != "ev-1" || ce.Source != "barnraise://laptop/nobira" ||
		ce.Type != "dev.barnraise.item.created" || ce.Subject != "I-42" || !ce.Time.Equal(t0) ||
		ce.DataContentType != "application/json" {
		t.Fatalf("%+v", ce)
	}
	b, _ := json.Marshal(ce)
	var m map[string]any
	json.Unmarshal(b, &m)
	if m["specversion"] != "1.0" || m["data"].(map[string]any)["note"] != "hi" || m["barnraiseproject"] != "nobira" {
		t.Fatalf("%s", b)
	}
	if ToCloudEvent(testEvent(), "x://y").Source != "x://y" {
		t.Fatal("explicit source ignored")
	}
}

func TestMatches(t *testing.T) {
	cases := []struct {
		f    []string
		typ  string
		want bool
	}{
		{nil, "item.created", true},
		{[]string{"item.*"}, "dev.barnraise.item.step.started", true},
		{[]string{"item.*"}, "check.completed", false},
		{[]string{"check.completed"}, "check.completed", true},
		{[]string{"check.completed"}, "check.requested", false},
		{[]string{"*"}, "x", true},
		{[]string{"item.*"}, "itemx.created", false},
	}
	for _, c := range cases {
		if got := Matches(c.f, c.typ); got != c.want {
			t.Errorf("Matches(%v,%q)=%v", c.f, c.typ, got)
		}
	}
}

func TestSignVerify(t *testing.T) {
	body := []byte(`{"a":1}`)
	ts := "1767323045"
	now := time.Unix(1767323045, 0)
	sig := Sign("s3cret", ts, body)
	if err := VerifyAt(now, "s3cret", body, sig, ts, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAt(now, "wrong", body, sig, ts, 5*time.Minute); !errors.Is(err, ErrBadSignature) {
		t.Fatal(err)
	}
	if err := VerifyAt(now, "s3cret", []byte(`{"a":2}`), sig, ts, 5*time.Minute); !errors.Is(err, ErrBadSignature) {
		t.Fatal(err)
	}
	if err := VerifyAt(now.Add(10*time.Minute), "s3cret", body, sig, ts, 5*time.Minute); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	// Changing the timestamp to dodge the window breaks the signature.
	if err := VerifyAt(now.Add(time.Hour), "s3cret", body, sig, "1767326645", 5*time.Minute); !errors.Is(err, ErrBadSignature) {
		t.Fatal(err)
	}
	if err := VerifyAt(now, "s3cret", body, "", ts, 0); !errors.Is(err, ErrNoSignature) {
		t.Fatal(err)
	}
}

type recorder struct {
	mu    sync.Mutex
	reqs  []*http.Request
	body  [][]byte
	codes []int // responses in order; last repeats
	n     int
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.body = append(r.body, b)
	code := r.codes[min(r.n, len(r.codes)-1)]
	r.n++
	r.mu.Unlock()
	if code == 429 {
		w.Header().Set("Retry-After", "7")
	}
	w.WriteHeader(code)
}

func fastSender(o Options, sleeps *[]time.Duration, mu *sync.Mutex) *Sender {
	o.Sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		*sleeps = append(*sleeps, d)
		mu.Unlock()
		return ctx.Err()
	}
	o.Now = func() time.Time { return t0 }
	return NewSender(o)
}

func TestSenderSignsAndRetries(t *testing.T) {
	rec := &recorder{codes: []int{500, 429, 502, 200}}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	var sleeps []time.Duration
	var mu sync.Mutex
	var results []Result
	s := fastSender(Options{OnResult: func(r Result) { mu.Lock(); results = append(results, r); mu.Unlock() }}, &sleeps, &mu)
	ce := ToCloudEvent(testEvent(), "")
	if err := s.Send(domain.WebhookDef{URL: srv.URL, Events: []string{"item.*"}}, "k", ce); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Err != nil || results[0].Attempts != 4 || results[0].Status != 200 {
		t.Fatalf("%+v", results)
	}
	want := []time.Duration{20 * time.Second, 7 * time.Second, 80 * time.Second}
	if len(sleeps) != 3 || sleeps[0] != want[0] || sleeps[1] != want[1] || sleeps[2] != want[2] {
		t.Fatalf("sleeps %v", sleeps)
	}
	req := rec.reqs[3]
	if req.Header.Get("Content-Type") != "application/cloudevents+json" || req.Header.Get(HeaderDelivery) != "ev-1" {
		t.Fatalf("%v", req.Header)
	}
	if err := VerifyAt(t0, "k", rec.body[3], req.Header.Get(HeaderSignature), req.Header.Get(HeaderTimestamp), time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestSenderGivesUpAndPermanent(t *testing.T) {
	rec := &recorder{codes: []int{503}}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	var sleeps []time.Duration
	var mu sync.Mutex
	s := fastSender(Options{MaxAttempts: 3}, &sleeps, &mu)
	err := s.Deliver(context.Background(), domain.WebhookDef{URL: srv.URL}, "k", ToCloudEvent(testEvent(), ""))
	if err == nil || rec.n != 3 {
		t.Fatalf("err=%v n=%d", err, rec.n)
	}
	rec2 := &recorder{codes: []int{400}}
	srv2 := httptest.NewServer(rec2)
	defer srv2.Close()
	if err := s.Deliver(context.Background(), domain.WebhookDef{URL: srv2.URL}, "k", ToCloudEvent(testEvent(), "")); err == nil || rec2.n != 1 {
		t.Fatalf("4xx must not retry: err=%v n=%d", err, rec2.n)
	}
	s.Close()
}

func TestSenderFilterConcurrencyAndClose(t *testing.T) {
	var inflight, peak, total atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inflight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		inflight.Add(-1)
		total.Add(1)
	}))
	defer srv.Close()
	s := NewSender(Options{Concurrency: 2})
	def := domain.WebhookDef{URL: srv.URL, Events: []string{"check.completed"}}
	for i := range 10 {
		ev := testEvent()
		if i%2 == 0 {
			ev.Type = domain.EvCheckCompleted
		}
		if err := s.Send(def, "", ToCloudEvent(ev, "")); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	if total.Load() != 5 {
		t.Fatalf("delivered %d, want 5 (filter)", total.Load())
	}
	if peak.Load() > 2 {
		t.Fatalf("peak concurrency %d", peak.Load())
	}
	if err := s.Send(def, "", ToCloudEvent(testEvent(), "")); err != nil {
		t.Fatal("filtered event after close should be a no-op")
	}
	ev := testEvent()
	ev.Type = domain.EvCheckCompleted
	if err := s.Send(def, "", ToCloudEvent(ev, "")); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v", err)
	}
}

func TestShutdownCutsRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	s := NewSender(Options{BaseDelay: time.Hour, MaxDelay: time.Hour})
	s.Send(domain.WebhookDef{URL: srv.URL}, "", ToCloudEvent(testEvent(), ""))
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := s.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("shutdown did not cut the retry wait")
	}
}
