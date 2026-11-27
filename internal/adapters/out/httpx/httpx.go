// Package httpx is the small HTTP client shared by the tracker and code-host
// adapters: JSON in and out, retries with backoff on 5xx and 429 (honoring
// Retry-After and rate-limit reset headers), and Link-header pagination.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Defaults used when the corresponding Client field is zero.
const (
	DefaultMaxRetries = 3
	DefaultMaxWait    = 60 * time.Second
	maxBodyBytes      = 32 << 20
)

// defaultHTTP is used when Client.HTTP is nil. http.DefaultClient has no
// timeout: one request whose connection dies silently would hang its
// caller (the merge queue asking for a pull request's checks) forever.
var defaultHTTP = &http.Client{Timeout: 2 * time.Minute}

// Client sends JSON requests to one API.
type Client struct {
	HTTP    *http.Client
	BaseURL string // prefixed to paths that do not start with http:// or https://
	// Auth sets the credentials on each request.
	Auth func(*http.Request)
	// Header holds extra headers sent on every request.
	Header http.Header
	// MaxRetries is the number of retries after the first attempt (0 means
	// DefaultMaxRetries, negative disables retries).
	MaxRetries int
	// Backoff gives the wait before retry n (starting at 1) when the server
	// does not say how long to wait. Default: exponential from 500ms with jitter.
	Backoff func(attempt int) time.Duration
	// Sleep waits; tests replace it to avoid real sleeps.
	Sleep func(ctx context.Context, d time.Duration) error
	// MaxWait caps a wait announced by the server; a longer announced wait
	// fails immediately instead of blocking the daemon.
	MaxWait time.Duration
	// Now is the clock used to interpret reset timestamps.
	Now func() time.Time
}

// Response is a finished request.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// StatusError is a non-2xx answer.
type StatusError struct {
	Method string
	URL    string
	Status int
	Body   []byte
}

func (e *StatusError) Error() string {
	b := strings.TrimSpace(string(e.Body))
	if len(b) > 300 {
		b = b[:300] + "…"
	}
	return fmt.Sprintf("%s %s: %d %s: %s", e.Method, e.URL, e.Status, http.StatusText(e.Status), b)
}

// IsStatus reports whether err is a StatusError with one of the codes.
func IsStatus(err error, codes ...int) bool {
	var se *StatusError
	if !errors.As(err, &se) {
		return false
	}
	return slices.Contains(codes, se.Status)
}

// StatusOf returns the HTTP status of err, or 0.
func StatusOf(err error) int {
	if se, ok := errors.AsType[*StatusError](err); ok {
		return se.Status
	}
	return 0
}

// URL resolves a path against BaseURL.
func (c *Client) URL(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	return strings.TrimRight(c.BaseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

// Do sends a request. in may be nil, a []byte (sent as is) or any value
// (sent as JSON). out, when not nil, receives the decoded JSON of a 2xx
// answer. A non-2xx answer after retries returns a *StatusError together
// with the response.
func (c *Client) Do(ctx context.Context, method, path string, in, out any) (*Response, error) {
	var payload []byte
	if in != nil {
		if b, ok := in.([]byte); ok {
			payload = b
		} else {
			b, err := json.Marshal(in)
			if err != nil {
				return nil, fmt.Errorf("encode request: %w", err)
			}
			payload = b
		}
	}
	url := c.URL(path)
	retries := c.MaxRetries
	if retries == 0 {
		retries = DefaultMaxRetries
	}
	if retries < 0 {
		retries = 0
	}
	for attempt := 0; ; attempt++ {
		resp, err := c.once(ctx, method, url, payload, in != nil)
		if err != nil {
			if ctx.Err() != nil || attempt >= retries || !idempotent(method) {
				return nil, err
			}
			if werr := c.sleep(ctx, c.backoff(attempt+1)); werr != nil {
				return nil, werr
			}
			continue
		}
		if resp.Status >= 200 && resp.Status < 300 {
			if out != nil && len(bytes.TrimSpace(resp.Body)) > 0 {
				if err := json.Unmarshal(resp.Body, out); err != nil {
					return resp, fmt.Errorf("%s %s: decode response: %w", method, url, err)
				}
			}
			return resp, nil
		}
		serr := &StatusError{Method: method, URL: url, Status: resp.Status, Body: resp.Body}
		wait, retry := c.retryWait(resp, attempt+1)
		if !retry || attempt >= retries {
			return resp, serr
		}
		if werr := c.sleep(ctx, wait); werr != nil {
			return resp, werr
		}
	}
}

func (c *Client) once(ctx context.Context, method, url string, payload []byte, hasBody bool) (*Response, error) {
	var body io.Reader
	if hasBody {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	for k, vs := range c.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	if hasBody && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "barnraise")
	}
	if c.Auth != nil {
		c.Auth(req)
	}
	hc := c.HTTP
	if hc == nil {
		hc = defaultHTTP
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, url, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("%s %s: read response: %w", method, url, err)
	}
	return &Response{Status: res.StatusCode, Header: res.Header, Body: b}, nil
}

func idempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions:
		return true
	}
	return false
}

// retryWait decides whether a non-2xx answer is worth retrying and how long
// to wait first.
func (c *Client) retryWait(resp *Response, attempt int) (time.Duration, bool) {
	limited := resp.Status == http.StatusTooManyRequests
	// GitHub signals primary and secondary rate limits with 403.
	if resp.Status == http.StatusForbidden && (resp.Header.Get("Retry-After") != "" || resp.Header.Get("X-RateLimit-Remaining") == "0") {
		limited = true
	}
	if !limited && resp.Status < 500 {
		return 0, false
	}
	if resp.Status == http.StatusNotImplemented {
		return 0, false
	}
	maxWait := c.MaxWait
	if maxWait <= 0 {
		maxWait = DefaultMaxWait
	}
	if d, ok := RetryAfter(resp.Header, c.now()); ok {
		if d > maxWait {
			return 0, false
		}
		return d, true
	}
	if limited && resp.Header.Get("X-RateLimit-Remaining") == "0" {
		if d, ok := resetWait(resp.Header, c.now()); ok {
			if d > maxWait {
				return 0, false
			}
			return d, true
		}
	}
	return c.backoff(attempt), true
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) backoff(attempt int) time.Duration {
	if c.Backoff != nil {
		return c.Backoff(attempt)
	}
	return DefaultBackoff(attempt)
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	return Sleep(ctx, d)
}

// DefaultBackoff is exponential from 500ms, capped at 30s, with ±20% jitter.
func DefaultBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := min(500*time.Millisecond<<min(attempt-1, 6), 30*time.Second)
	j := time.Duration(rand.Int64N(int64(d)/5+1)) - d/10
	return d + j
}

// Sleep waits for d or until ctx ends.
func Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// RetryAfter parses a Retry-After header (seconds or HTTP date).
func RetryAfter(h http.Header, now time.Time) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 {
			n = 0
		}
		return time.Duration(n) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		d := max(t.Sub(now), 0)
		return d, true
	}
	return 0, false
}

// resetWait reads X-RateLimit-Reset (epoch seconds) or RateLimit-Reset.
func resetWait(h http.Header, now time.Time) (time.Duration, bool) {
	for _, k := range []string{"X-RateLimit-Reset", "RateLimit-Reset"} {
		v := strings.TrimSpace(h.Get(k))
		if v == "" {
			continue
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			continue
		}
		// Small numbers are delta seconds (IETF draft); large ones are epochs.
		var d time.Duration
		if n < 1_000_000_000 {
			d = time.Duration(n) * time.Second
		} else {
			d = time.Unix(n, 0).Sub(now) + time.Second
		}
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

// NextLink returns the rel="next" URL of a Link header, or "".
func NextLink(h http.Header) string {
	for _, link := range h.Values("Link") {
		for part := range strings.SplitSeq(link, ",") {
			segs := strings.Split(part, ";")
			if len(segs) < 2 {
				continue
			}
			u := strings.TrimSpace(segs[0])
			if !strings.HasPrefix(u, "<") || !strings.HasSuffix(u, ">") {
				continue
			}
			for _, p := range segs[1:] {
				p = strings.TrimSpace(p)
				if p == `rel="next"` || p == "rel=next" {
					return u[1 : len(u)-1]
				}
			}
		}
	}
	return ""
}

// GraphQLError is one entry of a GraphQL "errors" array.
type GraphQLError struct {
	Message    string         `json:"message"`
	Type       string         `json:"type,omitempty"`
	Path       []any          `json:"path,omitempty"`
	Extensions map[string]any `json:"extensions,omitempty"`
}

// GraphQLErrors is returned when the answer carries errors.
type GraphQLErrors []GraphQLError

func (e GraphQLErrors) Error() string {
	msgs := make([]string, 0, len(e))
	for _, g := range e {
		msgs = append(msgs, g.Message)
	}
	return "graphql: " + strings.Join(msgs, "; ")
}

// GraphQL posts a query to path and decodes "data" into out. GraphQL errors
// are returned as GraphQLErrors.
func (c *Client) GraphQL(ctx context.Context, path, query string, vars map[string]any, out any) error {
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors GraphQLErrors   `json:"errors"`
	}
	req := map[string]any{"query": query}
	if len(vars) > 0 {
		req["variables"] = vars
	}
	if _, err := c.Do(ctx, http.MethodPost, path, req, &env); err != nil {
		return err
	}
	if len(env.Errors) > 0 {
		return env.Errors
	}
	if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("graphql: decode data: %w", err)
		}
	}
	return nil
}
