package otlp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// Config is the plugin's `options` in barnraise.yaml, with the standard
// OTEL_EXPORTER_OTLP_* variables as fallbacks.
type Config struct {
	Endpoint    string            `json:"endpoint"`     // collector base URL (…:4318) or the full traces URL
	Headers     map[string]string `json:"headers"`      // extra headers; secrets go in OTEL_EXPORTER_OTLP_HEADERS
	ServiceName string            `json:"service_name"` // default barnraise
	Interval    string            `json:"interval"`     // how often spans are sent (default 5s)
	Batch       int               `json:"batch"`        // send as soon as this many wait (default 256)
	Timeout     string            `json:"timeout"`      // per request (default 10s)
}

// DefaultEndpoint is the OTLP/HTTP port of a local collector.
const DefaultEndpoint = "http://localhost:4318"

// Settings are the resolved options.
type Settings struct {
	URL      string
	Headers  map[string]string
	Service  string
	Interval time.Duration
	Batch    int
	Timeout  time.Duration
}

// Resolve applies defaults and the environment to the options.
func (c Config) Resolve(getenv func(string) string) (Settings, error) {
	s := Settings{Headers: map[string]string{}, Service: def(c.ServiceName, "barnraise"), Interval: 5 * time.Second, Batch: 256, Timeout: 10 * time.Second}
	for _, d := range []struct {
		v   string
		dst *time.Duration
		key string
	}{{c.Interval, &s.Interval, "interval"}, {c.Timeout, &s.Timeout, "timeout"}} {
		if d.v == "" {
			continue
		}
		x, err := domain.ParseDuration(d.v)
		if err != nil || x <= 0 {
			return s, fmt.Errorf("option %s is %q: use a duration such as 5s", d.key, d.v)
		}
		*d.dst = x
	}
	if c.Batch > 0 {
		s.Batch = c.Batch
	}
	switch {
	case c.Endpoint != "":
		s.URL = tracesURL(c.Endpoint)
	case getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != "":
		s.URL = getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") // already the full URL, as the specification says
	case getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "":
		s.URL = tracesURL(getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	default:
		s.URL = tracesURL(DefaultEndpoint)
	}
	u, err := url.Parse(s.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return s, fmt.Errorf("the collector endpoint %q is not an http(s) URL", s.URL)
	}
	for _, env := range []string{"OTEL_EXPORTER_OTLP_HEADERS", "OTEL_EXPORTER_OTLP_TRACES_HEADERS"} {
		for k, v := range parseHeaders(getenv(env)) {
			s.Headers[k] = v
		}
	}
	for k, v := range c.Headers {
		s.Headers[k] = v
	}
	return s, nil
}

func tracesURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(base, "/v1/traces") {
		return base
	}
	return base + "/v1/traces"
}

// parseHeaders reads the W3C-baggage-like list of OTEL_EXPORTER_OTLP_HEADERS:
// key=value pairs separated by commas, values URL-encoded.
func parseHeaders(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(pair, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			continue
		}
		if dv, err := url.QueryUnescape(strings.TrimSpace(v)); err == nil {
			v = dv
		}
		out[k] = strings.TrimSpace(v)
	}
	return out
}

// ServeOptions are what the program gets from its process.
type ServeOptions struct {
	Version string              // Barnraise's version
	Getenv  func(string) string // default os.Getenv
	HTTP    *http.Client        // default: the settings' timeout
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve speaks the hook plugin protocol (docs/plugins.md) on in and out
// until in ends or the daemon sends shutdown. Every event becomes spans;
// they are sent every Interval, when Batch of them wait, and on shutdown.
func Serve(ctx context.Context, in io.Reader, out io.Writer, o ServeOptions) error {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	var wmu sync.Mutex
	send := func(m map[string]any) {
		m["jsonrpc"] = "2.0"
		b, _ := json.Marshal(m)
		wmu.Lock()
		defer wmu.Unlock()
		_, _ = out.Write(append(b, '\n'))
	}
	logf := func(level, format string, args ...any) {
		send(map[string]any{"method": "$/log", "params": map[string]string{"level": level, "message": fmt.Sprintf(format, args...)}})
	}

	var (
		x      *Exporter
		tr     *Tracer
		set    Settings
		stop   = make(chan struct{})
		done   = make(chan struct{})
		kick   = make(chan struct{}, 1)
		failed bool
	)
	flush := func(ctx context.Context) {
		if x == nil {
			return
		}
		if n := x.Dropped(); n > 0 {
			logf("warn", "%d spans dropped: the collector has been unreachable too long", n)
		}
		if err := x.Flush(ctx); err != nil {
			if !failed {
				logf("warn", "%v; keeping the spans and retrying", err)
			}
			failed = true
			return
		}
		if failed {
			logf("info", "the collector at %s answers again", set.URL)
		}
		failed = false
	}
	loop := func() {
		defer close(done)
		tick := time.NewTicker(set.Interval)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
			case <-kick:
			}
			fctx, cancel := context.WithTimeout(ctx, set.Timeout)
			flush(fctx)
			cancel()
		}
	}
	started := false
	finish := func(grace time.Duration) {
		if !started {
			return
		}
		started = false
		close(stop)
		<-done
		fctx, cancel := context.WithTimeout(context.Background(), grace)
		flush(fctx)
		cancel()
	}
	defer finish(2 * time.Second)

	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var m rpcMessage
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			send(map[string]any{"id": nil, "error": rpcError{-32700, "the plugin could not read the request: " + err.Error()}})
			continue
		}
		if len(m.ID) == 0 || string(m.ID) == "null" {
			continue // a notification: $/cancel needs nothing, every call is short
		}
		id := m.ID
		switch m.Method {
		case "initialize":
			var p struct {
				Version string `json:"barnraise_version"`
				Project string `json:"project"`
				Options Config `json:"options"`
			}
			if err := json.Unmarshal(m.Params, &p); err != nil {
				send(map[string]any{"id": id, "error": rpcError{-32602, "unreadable initialize params: " + err.Error()}})
				continue
			}
			s, err := p.Options.Resolve(o.Getenv)
			if err != nil {
				send(map[string]any{"id": id, "error": rpcError{-32602, err.Error()}})
				continue
			}
			if started {
				finish(2 * time.Second)
				stop, done = make(chan struct{}), make(chan struct{})
			}
			set = s
			version := def(p.Version, o.Version)
			hc := o.HTTP
			if hc == nil {
				hc = &http.Client{Timeout: s.Timeout}
			}
			x = &Exporter{URL: s.URL, Headers: s.Headers, HTTP: hc, Scope: "barnraise", Version: version,
				Resource: []Attr{{"service.name", s.Service}, {"service.version", version}, {"barnraise.project", p.Project}}}
			exp := x
			tr = NewTracer(func(sp Span) {
				exp.Add(sp)
				if exp.Pending() >= set.Batch {
					select {
					case kick <- struct{}{}:
					default:
					}
				}
			})
			started = true
			go loop()
			send(map[string]any{"id": id, "result": map[string]any{"name": "barnraise-otel", "version": version, "protocol_version": 1, "kinds": []string{domain.PluginHook}}})
			logf("info", "exporting spans to %s every %s", s.URL, s.Interval)
		case "hook.event":
			if tr == nil {
				send(map[string]any{"id": id, "error": rpcError{-32600, "hook.event before initialize"}})
				continue
			}
			var p struct {
				Event domain.Event `json:"event"`
			}
			if err := json.Unmarshal(m.Params, &p); err != nil {
				send(map[string]any{"id": id, "error": rpcError{-32602, "unreadable event: " + err.Error()}})
				continue
			}
			tr.Handle(p.Event)
			send(map[string]any{"id": id, "result": map[string]any{}})
		case "shutdown":
			// The daemon waits 2 seconds for this answer: send what is
			// left within that, then answer.
			finish(1500 * time.Millisecond)
			send(map[string]any{"id": id, "result": map[string]any{}})
			return nil
		default:
			send(map[string]any{"id": id, "error": rpcError{-32601, "method not found: " + m.Method}})
		}
	}
	return sc.Err()
}
