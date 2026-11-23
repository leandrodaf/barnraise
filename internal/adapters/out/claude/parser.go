package claude

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/provcommon"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Parser turns `claude -p --output-format stream-json --verbose` lines into
// normalized events. Every valid JSON line yields at least one event with the
// original line in Raw; lines without a normalized meaning (hooks, thinking,
// partial chunks…) come out as PEMessage with empty Text. Non-JSON lines are
// dropped. Use NewParser; one Parser per session.
type Parser struct {
	// Base is copied into every TokenUsage (agent, account, provider, role…).
	Base domain.TokenUsage
	// Now is used to resolve relative reset times; defaults to time.Now.
	Now func() time.Time

	sessionID string
	model     string
	toolNames map[string]string
	lastText  string
	done      bool
	res       ports.SessionResult
}

// NewParser returns a parser whose usage records carry base.
func NewParser(base domain.TokenUsage) *Parser {
	if base.Provider == "" {
		base.Provider = "claude"
	}
	return &Parser{Base: base, Now: time.Now, toolNames: map[string]string{}}
}

// ParseLine is a convenience for replaying recordings: it parses one line with p.
func (p *Parser) ParseLine(line string) []ports.ProviderEvent { return p.Parse([]byte(line)) }

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type apiMessage struct {
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
}

type usageJSON struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

// RateLimitInfo is the payload of a rate_limit_event line.
type RateLimitInfo struct {
	Status        string  `json:"status"` // allowed, allowed_warning, rejected
	ResetsAt      int64   `json:"resetsAt"`
	RateLimitType string  `json:"rateLimitType"` // five_hour, seven_day, …
	Utilization   float64 `json:"utilization"`   // 0..1
	Windows       map[string]struct {
		Utilization float64 `json:"utilization"`
		ResetsAt    int64   `json:"resetsAt"`
		Status      string  `json:"status"`
	} `json:"unifiedWindows"`
}

type line struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	Model     string          `json:"model"`
	Message   json.RawMessage `json:"message"`
	Error     string          `json:"error"`

	// result
	Result            string                     `json:"result"`
	IsError           bool                       `json:"is_error"`
	TotalCostUSD      float64                    `json:"total_cost_usd"`
	DurationMS        int64                      `json:"duration_ms"`
	Usage             *usageJSON                 `json:"usage"`
	ModelUsage        map[string]json.RawMessage `json:"modelUsage"`
	PermissionDenials []struct {
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	} `json:"permission_denials"`
	Errors []string `json:"errors"`

	// rate_limit_event
	RateLimitInfo *RateLimitInfo `json:"rate_limit_info"`

	// control_request (permission prompts routed to the host)
	Request *struct {
		Subtype  string          `json:"subtype"`
		ToolName string          `json:"tool_name"`
		Input    json.RawMessage `json:"input"`
	} `json:"request"`
}

// accountUsage turns the windows of a rate_limit_event into the account's
// usage. Claude sends one after each request, so a headless session keeps
// the account's limits current without the status line.
func (ri *RateLimitInfo) accountUsage() *domain.Usage {
	if ri == nil {
		return nil
	}
	u := &domain.Usage{Source: "session"}
	known := false
	at := func(sec int64) time.Time {
		if sec <= 0 {
			return time.Time{}
		}
		return time.Unix(sec, 0).UTC()
	}
	for name, w := range ri.Windows {
		pct := w.Utilization * 100
		switch {
		case name == "five_hour":
			u.FiveHourPct, u.FiveHourResets, known = pct, at(w.ResetsAt), true
		case name == "seven_day":
			u.WeeklyPct, u.WeeklyResets, known = pct, at(w.ResetsAt), true
		case strings.HasPrefix(name, "seven_day"):
			// The weekly window of one model family (the top model's is the tightest).
			if pct > u.WeeklyTopPct {
				u.WeeklyTopPct = pct
			}
			known = true
		}
	}
	if !known {
		// Older shape: one window per event.
		pct := ri.Utilization * 100
		switch ri.RateLimitType {
		case "five_hour":
			u.FiveHourPct, u.FiveHourResets = pct, at(ri.ResetsAt)
		case "seven_day":
			u.WeeklyPct, u.WeeklyResets = pct, at(ri.ResetsAt)
		default:
			return nil
		}
		if ri.Utilization == 0 {
			return nil // carries no number: not worth overwriting what is known
		}
	}
	return u
}

// RateLimit decodes the rate-limit status carried by an event, if any.
// Claude sends a rate_limit_event after each request with the utilization
// of the 5-hour and weekly windows.
func RateLimit(ev ports.ProviderEvent) (RateLimitInfo, bool) {
	var l line
	if len(ev.Raw) == 0 || json.Unmarshal(ev.Raw, &l) != nil || l.Type != "rate_limit_event" || l.RateLimitInfo == nil {
		return RateLimitInfo{}, false
	}
	return *l.RateLimitInfo, true
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
	var out []ports.ProviderEvent

	switch l.Type {
	case "system":
		if l.Subtype == "init" {
			if l.Model != "" {
				p.model = l.Model
			}
			e := ev(ports.PEInit)
			e.Text = l.Model
			out = append(out, e)
		}
	case "assistant":
		var m apiMessage
		_ = json.Unmarshal(l.Message, &m)
		if m.Model != "" && m.Model != "<synthetic>" && p.model == "" {
			p.model = m.Model
		}
		blocks := decodeBlocks(m.Content)
		if l.Error != "" {
			text := joinText(blocks)
			if text == "" {
				text = l.Error
			}
			if l.Error == "rate_limit" || provcommon.IsLimitText(text) {
				out = append(out, p.limit(raw, text, time.Time{}))
			} else {
				e := ev(ports.PEError)
				e.Text, e.IsError = text, true
				out = append(out, e)
			}
			break
		}
		for _, bl := range blocks {
			switch bl.Type {
			case "text":
				if bl.Text == "" {
					continue
				}
				p.lastText = bl.Text
				e := ev(ports.PEMessage)
				e.Text = bl.Text
				out = append(out, e)
			case "tool_use", "server_tool_use", "mcp_tool_use":
				p.toolNames[bl.ID] = bl.Name
				e := ev(ports.PEToolUse)
				e.Tool, e.Input = bl.Name, bl.Input
				out = append(out, e)
			}
		}
	case "user":
		var m apiMessage
		_ = json.Unmarshal(l.Message, &m)
		for _, bl := range decodeBlocks(m.Content) {
			if bl.Type != "tool_result" {
				continue
			}
			e := ev(ports.PEToolResult)
			e.Tool = p.toolNames[bl.ToolUseID]
			e.Text = resultText(bl.Content)
			e.IsError = bl.IsError
			out = append(out, e)
		}
	case "rate_limit_event":
		if ri := l.RateLimitInfo; ri != nil && ri.Status == "rejected" {
			var at time.Time
			if ri.ResetsAt > 0 {
				at = time.Unix(ri.ResetsAt, 0)
			}
			out = append(out, p.limit(raw, "rate limit "+ri.RateLimitType+" rejected", at))
		} else {
			// Utilization report: usage information without token counts.
			e := ev(ports.PEUsage)
			e.Limits = l.RateLimitInfo.accountUsage()
			out = append(out, e)
		}
	case "control_request":
		if l.Request != nil && l.Request.Subtype == "can_use_tool" {
			e := ev(ports.PEPermission)
			e.Tool, e.Input = l.Request.ToolName, l.Request.Input
			out = append(out, e)
		}
	case "result":
		out = append(out, p.result(l, raw)...)
	}
	if len(out) == 0 {
		out = append(out, ev(ports.PEMessage))
	}
	return out
}

func (p *Parser) limit(raw json.RawMessage, text string, at time.Time) ports.ProviderEvent {
	if at.IsZero() {
		at = provcommon.ParseResetsAt(text, p.now())
	}
	p.res.Limited = true
	if !at.IsZero() {
		p.res.ResetsAt = at
	}
	return ports.ProviderEvent{Kind: ports.PELimit, SessionID: p.sessionID, Text: text, IsError: true, ResetsAt: at, Raw: raw}
}

func (p *Parser) result(l line, raw json.RawMessage) []ports.ProviderEvent {
	var out []ports.ProviderEvent
	u := p.Base
	if p.model != "" {
		u.Model = p.model
	}
	if u.Model == "" {
		for name := range l.ModelUsage {
			u.Model = name
			break
		}
	}
	if l.Usage != nil {
		u.Input = l.Usage.InputTokens
		u.Output = l.Usage.OutputTokens
		u.CacheRead = l.Usage.CacheReadInputTokens
		u.CacheWrite = l.Usage.CacheCreationInputTokens
	}
	u.CostUSD = l.TotalCostUSD
	u.Duration = time.Duration(l.DurationMS) * time.Millisecond
	usage := u
	out = append(out, ports.ProviderEvent{Kind: ports.PEUsage, SessionID: p.sessionID, Usage: &usage, Raw: raw})

	for _, d := range l.PermissionDenials {
		out = append(out, ports.ProviderEvent{Kind: ports.PEPermission, SessionID: p.sessionID, Tool: d.ToolName, Input: d.ToolInput, IsError: true, Raw: raw})
	}

	text := l.Result
	if text == "" && len(l.Errors) > 0 {
		text = strings.Join(l.Errors, "; ")
	}
	if l.IsError && !p.res.Limited && provcommon.IsLimitText(text) {
		out = append(out, p.limit(raw, text, time.Time{}))
	}
	p.done = true
	p.res.SessionID = p.sessionID
	p.res.Text = text
	p.res.Usage = u
	doneUsage := u
	out = append(out, ports.ProviderEvent{Kind: ports.PEDone, SessionID: p.sessionID, Text: text, Usage: &doneUsage, IsError: l.IsError, Raw: raw})
	return out
}

// Finish implements provcommon.Parser: a run that never printed a result
// still ends with a done event, and limit messages on stderr are noticed.
func (p *Parser) Finish(exitCode int, stderr string) []ports.ProviderEvent {
	var out []ports.ProviderEvent
	if l, ok := provcommon.LimitLine(stderr); ok && exitCode != 0 && !p.res.Limited {
		out = append(out, p.limit(nil, l, time.Time{}))
	}
	if !p.done {
		p.done = true
		p.res.SessionID = p.sessionID
		if p.res.Text == "" {
			p.res.Text = p.lastText
		}
		u := p.Base
		if p.model != "" {
			u.Model = p.model
		}
		p.res.Usage = u
		out = append(out, ports.ProviderEvent{Kind: ports.PEDone, SessionID: p.sessionID, Text: p.res.Text, IsError: true})
	}
	return out
}

// Result implements provcommon.Parser.
func (p *Parser) Result() ports.SessionResult {
	r := p.res
	if r.SessionID == "" {
		r.SessionID = p.sessionID
	}
	return r
}

func (p *Parser) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func decodeBlocks(raw json.RawMessage) []contentBlock {
	if len(raw) == 0 {
		return nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		return blocks
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		return []contentBlock{{Type: "text", Text: s}}
	}
	return nil
}

func joinText(blocks []contentBlock) string {
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// resultText flattens tool_result content (a string or a list of blocks).
func resultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return joinText(decodeBlocks(raw))
}

var _ provcommon.Parser = (*Parser)(nil)
