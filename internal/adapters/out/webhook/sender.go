package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// Sender errors.
var (
	ErrClosed    = errors.New("webhook sender is closed")
	ErrQueueFull = errors.New("webhook queue is full")
)

// Result reports the outcome of one delivery.
type Result struct {
	ID       string
	URL      string
	Type     string
	Attempts int
	Status   int // last HTTP status, 0 on network errors
	Err      error
}

// Options configures a Sender. Zero values take the defaults.
type Options struct {
	Client      *http.Client
	MaxAttempts int           // default 6
	BaseDelay   time.Duration // default 20s; doubles per attempt (20s..320s, ~10 min total)
	MaxDelay    time.Duration // default 5m; also caps Retry-After
	Concurrency int           // default 4
	QueueSize   int           // default 1024
	// Sleep waits between attempts; tests inject a fast one. It must return
	// early with ctx.Err() when ctx ends.
	Sleep    func(ctx context.Context, d time.Duration) error
	Now      func() time.Time
	OnResult func(Result)
}

type job struct {
	def    domain.WebhookDef
	secret string
	ce     CloudEvent
}

// Sender delivers CloudEvents to webhooks from a bounded queue with a
// bounded number of workers.
type Sender struct {
	opts   Options
	queue  chan job
	wg     sync.WaitGroup
	mu     sync.RWMutex
	closed bool
	ctx    context.Context
	cancel context.CancelFunc
}

// NewSender starts the workers.
func NewSender(o Options) *Sender {
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 15 * time.Second}
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 6
	}
	if o.BaseDelay <= 0 {
		o.BaseDelay = 20 * time.Second
	}
	if o.MaxDelay <= 0 {
		o.MaxDelay = 5 * time.Minute
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 4
	}
	if o.QueueSize <= 0 {
		o.QueueSize = 1024
	}
	if o.Sleep == nil {
		o.Sleep = sleepCtx
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Sender{opts: o, queue: make(chan job, o.QueueSize)}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	for i := 0; i < o.Concurrency; i++ {
		s.wg.Add(1)
		go s.worker()
	}
	return s
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Send queues ce for def. Events filtered out by def.Events are dropped
// silently (nil). It never blocks: a full queue returns ErrQueueFull.
func (s *Sender) Send(def domain.WebhookDef, secret string, ce CloudEvent) error {
	if !Matches(def.Events, ce.Type) {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ErrClosed
	}
	select {
	case s.queue <- job{def: def, secret: secret, ce: ce}:
		return nil
	default:
		return ErrQueueFull
	}
}

func (s *Sender) worker() {
	defer s.wg.Done()
	for j := range s.queue {
		r := s.deliver(s.ctx, j.def, j.secret, j.ce)
		if s.opts.OnResult != nil {
			s.opts.OnResult(r)
		}
	}
}

// Close stops accepting events and waits until the queue is drained,
// retries included.
func (s *Sender) Close() error { return s.Shutdown(context.Background()) }

// Shutdown stops accepting events and drains the queue; when ctx ends,
// pending retry waits are cut short and remaining deliveries fail fast.
func (s *Sender) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.queue)
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		s.cancel()
		return nil
	case <-ctx.Done():
		s.cancel()
		<-done
		return ctx.Err()
	}
}

// Deliver posts ce synchronously with retries, ignoring the queue.
func (s *Sender) Deliver(ctx context.Context, def domain.WebhookDef, secret string, ce CloudEvent) error {
	return s.deliver(ctx, def, secret, ce).Err
}

func (s *Sender) deliver(ctx context.Context, def domain.WebhookDef, secret string, ce CloudEvent) Result {
	res := Result{ID: ce.ID, URL: def.URL, Type: ce.Type}
	body, err := json.Marshal(ce)
	if err != nil {
		res.Err = err
		return res
	}
	for attempt := 1; ; attempt++ {
		res.Attempts = attempt
		status, retryAfter, err := s.post(ctx, def.URL, secret, ce.ID, body)
		res.Status, res.Err = status, err
		if err == nil {
			return res
		}
		var perm *permanentError
		if errors.As(err, &perm) || attempt >= s.opts.MaxAttempts || ctx.Err() != nil {
			return res
		}
		d := s.backoff(attempt)
		if retryAfter > 0 {
			d = min(retryAfter, s.opts.MaxDelay)
		}
		if serr := s.opts.Sleep(ctx, d); serr != nil {
			return res
		}
	}
}

func (s *Sender) backoff(attempt int) time.Duration {
	d := s.opts.BaseDelay << (attempt - 1)
	if d <= 0 || d > s.opts.MaxDelay {
		d = s.opts.MaxDelay
	}
	return d
}

type permanentError struct{ status int }

func (e *permanentError) Error() string {
	return fmt.Sprintf("webhook answered %d; not retrying", e.status)
}

func (s *Sender) post(ctx context.Context, url, secret, id string, body []byte) (int, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, 0, &permanentError{}
	}
	req.Header.Set("Content-Type", "application/cloudevents+json")
	req.Header.Set("User-Agent", "barnraise-webhook")
	req.Header.Set(HeaderDelivery, id)
	SignRequest(req, secret, body, s.opts.Now())
	resp, err := s.opts.Client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return resp.StatusCode, 0, nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout:
		return resp.StatusCode, retryAfter(resp.Header.Get("Retry-After")), fmt.Errorf("webhook answered %d", resp.StatusCode)
	default:
		return resp.StatusCode, 0, &permanentError{status: resp.StatusCode}
	}
}

func retryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
