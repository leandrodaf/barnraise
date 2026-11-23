package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func recorder() (*[]time.Duration, func(context.Context, time.Duration) error) {
	var slept []time.Duration
	return &slept, func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
}

func TestRetryOn5xxAnd429(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"a":1}` {
			t.Errorf("attempt %d body = %s", n.Load(), b)
		}
		switch n.Add(1) {
		case 1:
			w.WriteHeader(http.StatusBadGateway)
		case 2:
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()
	slept, sleep := recorder()
	c := &Client{BaseURL: srv.URL, Sleep: sleep, Backoff: func(int) time.Duration { return time.Millisecond }}
	var out struct{ OK bool }
	if _, err := c.Do(context.Background(), http.MethodPost, "/x", map[string]int{"a": 1}, &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || n.Load() != 3 {
		t.Fatalf("ok=%v attempts=%d", out.OK, n.Load())
	}
	if len(*slept) != 2 || (*slept)[0] != time.Millisecond || (*slept)[1] != 2*time.Second {
		t.Errorf("slept = %v", *slept)
	}
}

func TestGiveUpAfterMaxRetries(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("down"))
	}))
	defer srv.Close()
	_, sleep := recorder()
	c := &Client{BaseURL: srv.URL, Sleep: sleep, MaxRetries: 2}
	_, err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil)
	if !IsStatus(err, 503) || StatusOf(err) != 503 || n.Load() != 3 {
		t.Fatalf("err=%v attempts=%d", err, n.Load())
	}
	var se *StatusError
	if !errors.As(err, &se) || string(se.Body) != "down" {
		t.Errorf("status error = %+v", se)
	}
}

func TestNoRetryOnClientErrors(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL}
	if _, err := c.Do(context.Background(), http.MethodPost, "/x", nil, nil); !IsStatus(err, 422) || n.Load() != 1 {
		t.Fatalf("err=%v attempts=%d", err, n.Load())
	}
}

func TestRateLimitResetAndMaxWait(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var n atomic.Int32
	reset := now.Add(10 * time.Second).Unix()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	slept, sleep := recorder()
	c := &Client{BaseURL: srv.URL, Sleep: sleep, Now: func() time.Time { return now }}
	if _, err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(*slept) != 1 || (*slept)[0] != 11*time.Second {
		t.Errorf("slept = %v", *slept)
	}

	// An announced wait above MaxWait fails right away.
	n.Store(0)
	*slept = nil
	c.MaxWait = 5 * time.Second
	if _, err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil); !IsStatus(err, 403) || len(*slept) != 0 {
		t.Errorf("err=%v slept=%v", err, *slept)
	}

	// A plain 403 is not retried.
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer plain.Close()
	*slept = nil
	c = &Client{BaseURL: plain.URL, Sleep: sleep}
	if _, err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil); !IsStatus(err, 403) || len(*slept) != 0 {
		t.Errorf("plain 403: err=%v slept=%v", err, *slept)
	}
}

func TestTransportErrorsRetriedOnlyWhenIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	slept, sleep := recorder()
	c := &Client{BaseURL: url, Sleep: sleep, Backoff: func(int) time.Duration { return time.Microsecond }}
	if _, err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil); err == nil {
		t.Fatal("no error")
	}
	if len(*slept) != DefaultMaxRetries {
		t.Errorf("GET slept %d times", len(*slept))
	}
	*slept = nil
	if _, err := c.Do(context.Background(), http.MethodPost, "/x", nil, nil); err == nil || len(*slept) != 0 {
		t.Errorf("POST err=%v slept=%v", err, *slept)
	}
}

func TestRetryAfterDate(t *testing.T) {
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	h := http.Header{"Retry-After": {now.Add(7 * time.Second).Format(http.TimeFormat)}}
	if d, ok := RetryAfter(h, now); !ok || d != 7*time.Second {
		t.Errorf("d=%v ok=%v", d, ok)
	}
	if _, ok := RetryAfter(http.Header{"Retry-After": {"soon"}}, now); ok {
		t.Error("garbage parsed")
	}
}

func TestNextLink(t *testing.T) {
	h := http.Header{"Link": {`<https://api.x/r?page=1>; rel="prev", <https://api.x/r?page=3>; rel="next", <https://api.x/r?page=9>; rel="last"`}}
	if got := NextLink(h); got != "https://api.x/r?page=3" {
		t.Errorf("next = %q", got)
	}
	if NextLink(http.Header{"Link": {`<https://api.x/r?page=9>; rel="last"`}}) != "" || NextLink(http.Header{}) != "" {
		t.Error("phantom next")
	}
}

func TestGraphQL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if string(b) == `{"query":"{ bad }"}` {
			w.Write([]byte(`{"data":null,"errors":[{"message":"Field 'bad' doesn't exist"}]}`))
			return
		}
		w.Write([]byte(`{"data":{"viewer":{"login":"bot"}}}`))
	}))
	defer srv.Close()
	c := &Client{}
	var out struct {
		Viewer struct{ Login string }
	}
	if err := c.GraphQL(context.Background(), srv.URL, "{ viewer { login } }", map[string]any{"a": 1}, &out); err != nil || out.Viewer.Login != "bot" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	err := c.GraphQL(context.Background(), srv.URL, "{ bad }", nil, nil)
	var ge GraphQLErrors
	if !errors.As(err, &ge) || ge[0].Message != "Field 'bad' doesn't exist" {
		t.Errorf("err = %v", err)
	}
}

func TestDefaultBackoffBounds(t *testing.T) {
	for a := 1; a < 12; a++ {
		d := DefaultBackoff(a)
		if d < 400*time.Millisecond || d > 36*time.Second {
			t.Errorf("attempt %d: %v", a, d)
		}
	}
}

func TestSleepHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}
