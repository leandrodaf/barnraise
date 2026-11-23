package codex

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/provcommon"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Parser turns `codex exec --json` lines into normalized events. Every valid
// JSON line yields at least one event with the original line in Raw; lines
// without a normalized meaning (turn.started, reasoning, todo lists, item
// updates…) come out as PEMessage with empty Text. One Parser per session.
//
// Token counts are normalized to the Claude convention: Input excludes the
// cached part (codex reports input_tokens including cached_input_tokens), so
// TokenUsage.Total stays comparable across providers.
type Parser struct {
	Base domain.TokenUsage
	Now  func() time.Time

	threadID string
	started  map[string]bool
	lastText string
	done     bool
	res      ports.SessionResult
	usage    domain.TokenUsage
}

// NewParser returns a parser whose usage records carry base.
func NewParser(base domain.TokenUsage) *Parser {
	if base.Provider == "" {
		base.Provider = "codex"
	}
	return &Parser{Base: base, Now: time.Now, started: map[string]bool{}, usage: base}
}

type item struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	Text             string          `json:"text"`
	Command          string          `json:"command"`
	AggregatedOutput string          `json:"aggregated_output"`
	ExitCode         *int            `json:"exit_code"`
	Status           string          `json:"status"`
	Changes          json.RawMessage `json:"changes"`
	Server           string          `json:"server"`
	Tool             string          `json:"tool"`
	Arguments        json.RawMessage `json:"arguments"`
	Result           *struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
	Error   *errorJSON `json:"error"`
	Query   string     `json:"query"`
	Message string     `json:"message"`
}

type errorJSON struct {
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

type line struct {
	Type     string     `json:"type"`
	ThreadID string     `json:"thread_id"`
	Item     *item      `json:"item"`
	Message  string     `json:"message"`
	Error    *errorJSON `json:"error"`
	Usage    *struct {
		InputTokens       int64 `json:"input_tokens"`
		CachedInputTokens int64 `json:"cached_input_tokens"`
		CacheWriteTokens  int64 `json:"cache_write_input_tokens"`
		OutputTokens      int64 `json:"output_tokens"`
		ReasoningTokens   int64 `json:"reasoning_output_tokens"`
	} `json:"usage"`
}

// Parse implements provcommon.Parser.
func (p *Parser) Parse(b []byte) []ports.ProviderEvent {
	var l line
	if err := json.Unmarshal(b, &l); err != nil {
		return nil
	}
	raw := json.RawMessage(append([]byte(nil), b...))
	if l.ThreadID != "" {
		p.threadID = l.ThreadID
	}
	ev := func(k ports.ProviderEventKind) ports.ProviderEvent {
		return ports.ProviderEvent{Kind: k, SessionID: p.threadID, Raw: raw}
	}
	var out []ports.ProviderEvent

	switch l.Type {
	case "thread.started":
		out = append(out, ev(ports.PEInit))
	case "item.started", "item.updated", "item.completed":
		if l.Item != nil {
			out = append(out, p.item(l.Type, l.Item, ev)...)
		}
	case "turn.completed":
		if l.Usage != nil {
			// Several turns (resume) add up.
			p.usage.Input += l.Usage.InputTokens - l.Usage.CachedInputTokens
			p.usage.CacheRead += l.Usage.CachedInputTokens
			p.usage.CacheWrite += l.Usage.CacheWriteTokens
			p.usage.Output += l.Usage.OutputTokens
		}
		u := p.usage
		e := ev(ports.PEUsage)
		e.Usage = &u
		out = append(out, e)
		out = append(out, p.finish(raw, p.lastText, false))
	case "turn.failed":
		msg := ""
		if l.Error != nil {
			msg = l.Error.Message
		}
		if provcommon.IsLimitText(msg) {
			if !p.res.Limited {
				out = append(out, p.limit(raw, msg))
			}
		} else {
			e := ev(ports.PEError)
			e.Text, e.IsError = msg, true
			out = append(out, e)
		}
		out = append(out, p.finish(raw, msg, true))
	case "error":
		msg := l.Message
		if msg == "" && l.Error != nil {
			msg = l.Error.Message
		}
		if provcommon.IsLimitText(msg) {
			if !p.res.Limited {
				out = append(out, p.limit(raw, msg))
			}
		} else {
			e := ev(ports.PEError)
			e.Text, e.IsError = msg, true
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		out = append(out, ev(ports.PEMessage))
	}
	return out
}

func (p *Parser) item(kind string, it *item, ev func(ports.ProviderEventKind) ports.ProviderEvent) []ports.ProviderEvent {
	var out []ports.ProviderEvent
	completed := kind == "item.completed"
	failed := it.Status == "failed" || it.Status == "declined"

	use := func(tool string, input any) {
		if p.started[it.ID] {
			return
		}
		p.started[it.ID] = true
		e := ev(ports.PEToolUse)
		e.Tool = tool
		e.Input, _ = json.Marshal(input)
		out = append(out, e)
	}
	result := func(tool, text string, isErr bool) {
		e := ev(ports.PEToolResult)
		e.Tool, e.Text, e.IsError = tool, text, isErr
		out = append(out, e)
	}

	switch it.Type {
	case "agent_message":
		if completed && it.Text != "" {
			p.lastText = it.Text
			e := ev(ports.PEMessage)
			e.Text = it.Text
			out = append(out, e)
		}
	case "command_execution":
		use("command_execution", map[string]string{"command": it.Command})
		if completed {
			isErr := failed || (it.ExitCode != nil && *it.ExitCode != 0)
			result("command_execution", it.AggregatedOutput, isErr)
		}
	case "file_change":
		changes := it.Changes
		if len(changes) == 0 {
			changes = json.RawMessage("[]")
		}
		use("file_change", map[string]json.RawMessage{"changes": changes})
		if completed {
			result("file_change", it.Status, failed)
		}
	case "mcp_tool_call":
		tool := it.Server + "." + it.Tool
		args := it.Arguments
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		if !p.started[it.ID] {
			p.started[it.ID] = true
			e := ev(ports.PEToolUse)
			e.Tool, e.Input = tool, args
			out = append(out, e)
		}
		if completed {
			var text []string
			isErr := failed
			if it.Result != nil {
				for _, c := range it.Result.Content {
					if c.Text != "" {
						text = append(text, c.Text)
					}
				}
			}
			if it.Error != nil && it.Error.Message != "" {
				isErr = true
				text = append(text, it.Error.Message)
			}
			result(tool, strings.Join(text, "\n"), isErr)
		}
	case "web_search":
		use("web_search", map[string]string{"query": it.Query})
		if completed {
			result("web_search", "", failed)
		}
	case "error":
		e := ev(ports.PEError)
		e.Text, e.IsError = it.Message, true
		out = append(out, e)
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
	return ports.ProviderEvent{Kind: ports.PELimit, SessionID: p.threadID, Text: text, IsError: true, ResetsAt: at, Raw: raw}
}

func (p *Parser) finish(raw json.RawMessage, text string, isErr bool) ports.ProviderEvent {
	p.done = true
	p.res.SessionID = p.threadID
	p.res.Text = text
	p.res.Usage = p.usage
	u := p.usage
	return ports.ProviderEvent{Kind: ports.PEDone, SessionID: p.threadID, Text: text, Usage: &u, IsError: isErr, Raw: raw}
}

// Finish implements provcommon.Parser.
func (p *Parser) Finish(exitCode int, stderr string) []ports.ProviderEvent {
	var out []ports.ProviderEvent
	if l, ok := provcommon.LimitLine(stderr); ok && exitCode != 0 && !p.res.Limited {
		out = append(out, p.limit(nil, l))
	}
	if !p.done {
		e := p.finish(nil, p.lastText, true)
		e.Usage = nil
		out = append(out, e)
	}
	return out
}

// Result implements provcommon.Parser.
func (p *Parser) Result() ports.SessionResult {
	r := p.res
	if r.SessionID == "" {
		r.SessionID = p.threadID
	}
	return r
}

var _ provcommon.Parser = (*Parser)(nil)
