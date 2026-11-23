package gemini

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/provcommon"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Parser turns `gemini --output-format stream-json` lines into normalized
// events. The stream is one JSON object per line with a "type": init,
// message, tool_use, tool_result, error and result (the shape documented in
// the CLI's headless mode guide). Every valid JSON line yields at least one
// event with the original line in Raw; types without a normalized meaning,
// user messages and warnings come out as PEMessage with empty Text, so a new
// event type of a later CLI only passes through. One Parser per session.
//
// Assistant text arrives in chunks ("delta": true); each chunk is a
// PEMessage with its text, and the session's text is the assistant text
// after the last tool call.
//
// Token counts are normalized to the Claude convention: Input excludes the
// cached part, so TokenUsage.Total stays comparable across providers.
type Parser struct {
	Base domain.TokenUsage
	Now  func() time.Time

	sessionID string
	tools     map[string]string // tool_id -> tool_name
	text      strings.Builder
	afterTool bool
	done      bool
	res       ports.SessionResult
	usage     domain.TokenUsage
}

// NewParser returns a parser whose usage records carry base.
func NewParser(base domain.TokenUsage) *Parser {
	if base.Provider == "" {
		base.Provider = "gemini"
	}
	return &Parser{Base: base, Now: time.Now, tools: map[string]string{}, usage: base}
}

type errorJSON struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (e *errorJSON) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		e.Message = s
		return nil
	}
	type plain errorJSON
	return json.Unmarshal(b, (*plain)(e))
}

// content is a message's text: a string, or a list of parts with "text".
type content string

func (c *content) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*c = content(s)
		return nil
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(b, &parts) == nil {
		var sb strings.Builder
		for _, p := range parts {
			sb.WriteString(p.Text)
		}
		*c = content(sb.String())
	}
	return nil
}

// output is a tool result's output: a string, or any JSON kept as text.
type output string

func (o *output) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*o = output(s)
		return nil
	}
	if string(b) != "null" {
		*o = output(b)
	}
	return nil
}

type stats struct {
	TotalTokens  int64 `json:"total_tokens"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	Cached       int64 `json:"cached"`
}

type line struct {
	Type       string          `json:"type"`
	SessionID  string          `json:"session_id"`
	Role       string          `json:"role"`
	Content    content         `json:"content"`
	ToolName   string          `json:"tool_name"`
	ToolID     string          `json:"tool_id"`
	Parameters json.RawMessage `json:"parameters"`
	Status     string          `json:"status"`
	Output     output          `json:"output"`
	Severity   string          `json:"severity"`
	Message    string          `json:"message"`
	Error      *errorJSON      `json:"error"`
	Stats      *stats          `json:"stats"`
}

// isLimit adds the Gemini API's quota wording to the shared limit markers.
func isLimit(text string) bool {
	if provcommon.IsLimitText(text) {
		return true
	}
	t := strings.ToLower(text)
	for _, m := range []string{"resource_exhausted", "exhausted your", "quota"} {
		if strings.Contains(t, m) {
			return true
		}
	}
	return false
}

// Parse implements provcommon.Parser.
func (p *Parser) Parse(b []byte) []ports.ProviderEvent {
	var l line
	if err := json.Unmarshal(b, &l); err != nil {
		return nil
	}
	raw := json.RawMessage(append([]byte(nil), b...))
	if l.SessionID != "" {
		p.sessionID = l.SessionID
	}
	ev := func(k ports.ProviderEventKind) ports.ProviderEvent {
		return ports.ProviderEvent{Kind: k, SessionID: p.sessionID, Raw: raw}
	}
	errText := func() string {
		msg := l.Message
		if l.Error != nil && l.Error.Message != "" {
			msg = l.Error.Message
		}
		return msg
	}
	var out []ports.ProviderEvent

	switch l.Type {
	case "init":
		out = append(out, ev(ports.PEInit))
	case "message":
		if l.Role == "assistant" && l.Content != "" {
			if p.afterTool {
				p.text.Reset()
				p.afterTool = false
			}
			p.text.WriteString(string(l.Content))
			e := ev(ports.PEMessage)
			e.Text = string(l.Content)
			out = append(out, e)
		}
	case "tool_use":
		p.afterTool = true
		if l.ToolID != "" {
			p.tools[l.ToolID] = l.ToolName
		}
		in := l.Parameters
		if len(in) == 0 || string(in) == "null" {
			in = json.RawMessage("{}")
		}
		e := ev(ports.PEToolUse)
		e.Tool, e.Input = l.ToolName, in
		out = append(out, e)
	case "tool_result":
		p.afterTool = true
		e := ev(ports.PEToolResult)
		e.Tool = p.tools[l.ToolID]
		if e.Tool == "" {
			e.Tool = l.ToolName
		}
		e.Text = string(l.Output)
		if l.Status == "error" {
			e.IsError = true
			if msg := errText(); msg != "" {
				e.Text = msg
			}
		}
		out = append(out, e)
	case "error":
		msg := errText()
		switch {
		case isLimit(msg):
			if !p.res.Limited {
				out = append(out, p.limit(raw, msg))
			}
		case l.Severity == "warning":
			// A warning is not an error of the session; it passes through raw.
		default:
			e := ev(ports.PEError)
			e.Text, e.IsError = msg, true
			out = append(out, e)
		}
	case "result":
		if l.Stats != nil {
			in := l.Stats.InputTokens - l.Stats.Cached
			if in < 0 {
				in = l.Stats.InputTokens
			}
			// Several results (resume) add up.
			p.usage.Input += in
			p.usage.CacheRead += min(l.Stats.Cached, l.Stats.InputTokens)
			p.usage.Output += l.Stats.OutputTokens
		}
		u := p.usage
		e := ev(ports.PEUsage)
		e.Usage = &u
		out = append(out, e)
		failed := l.Status != "" && l.Status != "success"
		text := p.text.String()
		if failed {
			msg := errText()
			if isLimit(msg) {
				if !p.res.Limited {
					out = append(out, p.limit(raw, msg))
				}
			} else if msg != "" {
				e := ev(ports.PEError)
				e.Text, e.IsError = msg, true
				out = append(out, e)
			}
			if msg != "" {
				text = msg
			}
		}
		out = append(out, p.finish(raw, text, failed))
	}
	if len(out) == 0 {
		out = append(out, ev(ports.PEMessage))
	}
	return out
}

func (p *Parser) limit(raw json.RawMessage, text string) ports.ProviderEvent {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	at := provcommon.ParseResetsAt(text, now())
	p.res.Limited = true
	if !at.IsZero() {
		p.res.ResetsAt = at
	}
	return ports.ProviderEvent{Kind: ports.PELimit, SessionID: p.sessionID, Text: text, IsError: true, ResetsAt: at, Raw: raw}
}

func (p *Parser) finish(raw json.RawMessage, text string, isErr bool) ports.ProviderEvent {
	p.done = true
	p.res.SessionID = p.sessionID
	p.res.Text = text
	p.res.Usage = p.usage
	u := p.usage
	return ports.ProviderEvent{Kind: ports.PEDone, SessionID: p.sessionID, Text: text, Usage: &u, IsError: isErr, Raw: raw}
}

// Finish implements provcommon.Parser.
func (p *Parser) Finish(exitCode int, stderr string) []ports.ProviderEvent {
	var out []ports.ProviderEvent
	if exitCode != 0 && !p.res.Limited {
		if l, ok := limitLine(stderr); ok {
			out = append(out, p.limit(nil, l))
		}
	}
	if !p.done {
		e := p.finish(nil, p.text.String(), true)
		e.Usage = nil
		out = append(out, e)
	}
	return out
}

// limitLine returns the last line of text that reads as a limit message.
func limitLine(text string) (string, bool) {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" && isLimit(l) {
			return l, true
		}
	}
	return "", false
}

// Result implements provcommon.Parser.
func (p *Parser) Result() ports.SessionResult {
	r := p.res
	if r.SessionID == "" {
		r.SessionID = p.sessionID
	}
	return r
}

var _ provcommon.Parser = (*Parser)(nil)
