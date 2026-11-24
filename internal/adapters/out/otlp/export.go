// Package otlp is Barnraise's OpenTelemetry export, shipped as a hook
// plugin (`barnraise plugin otel`, ADR-0035): it turns the event log's
// item and step lifecycle, checks and the token, cost and time ledger
// (agent.usage) into spans and sends them to a collector with OTLP over
// HTTP in its JSON encoding. It is written against the OTLP specification
// with the standard library only, without the OpenTelemetry SDK.
package otlp

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Span is one finished span.
type Span struct {
	TraceID [16]byte
	SpanID  [8]byte
	Parent  [8]byte // zero for a root span
	Name    string
	Start   time.Time
	End     time.Time
	Attrs   []Attr
	Error   bool
	Message string // status message of an error
}

// Attr is a span attribute: a string, an int64, a float64 or a bool.
type Attr struct {
	Key   string
	Value any
}

// Exporter batches spans and posts them to an OTLP/HTTP endpoint.
type Exporter struct {
	URL      string            // full traces URL (…/v1/traces)
	Headers  map[string]string // sent with every request (an API key)
	Resource []Attr            // service.name and the like
	Scope    string            // instrumentation scope name and version
	Version  string
	HTTP     *http.Client
	// MaxQueue bounds the spans kept while the collector is unreachable;
	// the oldest are dropped beyond it.
	MaxQueue int

	mu      sync.Mutex
	queue   []Span
	dropped int
}

// Add queues a span.
func (x *Exporter) Add(s Span) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.queue = append(x.queue, s)
	if max := x.maxQueue(); len(x.queue) > max {
		x.dropped += len(x.queue) - max
		x.queue = append([]Span(nil), x.queue[len(x.queue)-max:]...)
	}
}

func (x *Exporter) maxQueue() int {
	if x.MaxQueue > 0 {
		return x.MaxQueue
	}
	return 4096
}

// Pending is the number of spans waiting to be sent.
func (x *Exporter) Pending() int {
	x.mu.Lock()
	defer x.mu.Unlock()
	return len(x.queue)
}

// Dropped returns and resets the number of spans dropped since the last call.
func (x *Exporter) Dropped() int {
	x.mu.Lock()
	defer x.mu.Unlock()
	n := x.dropped
	x.dropped = 0
	return n
}

// Flush sends what is queued. On failure the spans stay queued for the
// next flush.
func (x *Exporter) Flush(ctx context.Context) error {
	x.mu.Lock()
	batch := x.queue
	x.queue = nil
	x.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	if err := x.post(ctx, batch); err != nil {
		x.mu.Lock()
		x.queue = append(batch, x.queue...)
		if max := x.maxQueue(); len(x.queue) > max {
			x.dropped += len(x.queue) - max
			x.queue = append([]Span(nil), x.queue[len(x.queue)-max:]...)
		}
		x.mu.Unlock()
		return err
	}
	return nil
}

func (x *Exporter) post(ctx context.Context, spans []Span) error {
	body, err := json.Marshal(Encode(x.Resource, x.Scope, x.Version, spans))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, x.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "barnraise-otel/"+x.Version)
	for k, v := range x.Headers {
		req.Header.Set(k, v)
	}
	c := x.HTTP
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("send %d spans to %s: %w", len(spans), x.URL, err)
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("send %d spans to %s: the collector answered %d: %s", len(spans), x.URL, resp.StatusCode, bytes.TrimSpace(msg))
	}
	return nil
}

// The OTLP JSON encoding (opentelemetry-proto, ExportTraceServiceRequest
// in the protobuf JSON mapping): ids in hex, 64-bit integers as strings.

type anyValue struct {
	StringValue *string  `json:"stringValue,omitempty"`
	IntValue    *string  `json:"intValue,omitempty"`
	DoubleValue *float64 `json:"doubleValue,omitempty"`
	BoolValue   *bool    `json:"boolValue,omitempty"`
}

type keyValue struct {
	Key   string   `json:"key"`
	Value anyValue `json:"value"`
}

type jsonSpan struct {
	TraceID           string     `json:"traceId"`
	SpanID            string     `json:"spanId"`
	ParentSpanID      string     `json:"parentSpanId,omitempty"`
	Name              string     `json:"name"`
	Kind              int        `json:"kind"`
	StartTimeUnixNano string     `json:"startTimeUnixNano"`
	EndTimeUnixNano   string     `json:"endTimeUnixNano"`
	Attributes        []keyValue `json:"attributes,omitempty"`
	Status            struct {
		Code    int    `json:"code,omitempty"`
		Message string `json:"message,omitempty"`
	} `json:"status"`
}

// Request is the body of an OTLP/HTTP traces export.
type Request struct {
	ResourceSpans []ResourceSpans `json:"resourceSpans"`
}

// ResourceSpans are the spans of one resource (this Barnraise project).
type ResourceSpans struct {
	Resource struct {
		Attributes []keyValue `json:"attributes"`
	} `json:"resource"`
	ScopeSpans []ScopeSpans `json:"scopeSpans"`
}

// ScopeSpans are the spans of one instrumentation scope.
type ScopeSpans struct {
	Scope struct {
		Name    string `json:"name"`
		Version string `json:"version,omitempty"`
	} `json:"scope"`
	Spans []jsonSpan `json:"spans"`
}

const (
	spanKindInternal = 1
	statusOK         = 1
	statusError      = 2
)

// Encode builds the export request of spans.
func Encode(resource []Attr, scope, version string, spans []Span) Request {
	var ss ScopeSpans
	ss.Scope.Name, ss.Scope.Version = scope, version
	for _, s := range spans {
		js := jsonSpan{
			TraceID: hex.EncodeToString(s.TraceID[:]), SpanID: hex.EncodeToString(s.SpanID[:]), Name: s.Name, Kind: spanKindInternal,
			StartTimeUnixNano: strconv.FormatInt(s.Start.UnixNano(), 10), EndTimeUnixNano: strconv.FormatInt(s.End.UnixNano(), 10),
			Attributes: attrs(s.Attrs),
		}
		if s.Parent != ([8]byte{}) {
			js.ParentSpanID = hex.EncodeToString(s.Parent[:])
		}
		js.Status.Code = statusOK
		if s.Error {
			js.Status.Code, js.Status.Message = statusError, s.Message
		}
		ss.Spans = append(ss.Spans, js)
	}
	var rs ResourceSpans
	rs.Resource.Attributes = attrs(resource)
	rs.ScopeSpans = []ScopeSpans{ss}
	return Request{ResourceSpans: []ResourceSpans{rs}}
}

func attrs(as []Attr) []keyValue {
	out := make([]keyValue, 0, len(as))
	for _, a := range as {
		var v anyValue
		switch x := a.Value.(type) {
		case string:
			if x == "" {
				continue
			}
			v.StringValue = &x
		case int:
			s := strconv.Itoa(x)
			v.IntValue = &s
		case int64:
			s := strconv.FormatInt(x, 10)
			v.IntValue = &s
		case float64:
			v.DoubleValue = &x
		case bool:
			v.BoolValue = &x
		default:
			s := fmt.Sprint(x)
			v.StringValue = &s
		}
		out = append(out, keyValue{Key: a.Key, Value: v})
	}
	return out
}
