package codexchat

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// maxResult bounds what a tool's result shows in the conversation.
const maxResult = 1200

// Transcript reads the thread's items from entry after on. With no
// connection open it asks the daemon on a short one; a daemon that is not
// running gives an empty conversation.
func (s *Session) Transcript(configDir, threadID, _ string, after int) ([]ports.ChatEntry, error) {
	if threadID == "" {
		return []ports.ChatEntry{}, nil
	}
	s.mu.Lock()
	c := s.conn
	if c != nil && s.thread != threadID {
		c = nil
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if c == nil {
		tmp, err := dial(SocketPath(configDir), nil)
		if err != nil {
			return []ports.ChatEntry{}, nil
		}
		defer tmp.close()
		if _, err := tmp.call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "barnraise", "version": "1"}}); err != nil {
			return []ports.ChatEntry{}, nil
		}
		_ = tmp.notifyServer("initialized")
		c = tmp
	}
	var items []json.RawMessage
	var cursor any
	for range 50 {
		params := map[string]any{"threadId": threadID, "limit": 200, "sortDirection": "asc"}
		if cursor != nil {
			params["cursor"] = cursor
		}
		res, err := c.call(ctx, "thread/items/list", params)
		if err != nil {
			return []ports.ChatEntry{}, nil
		}
		var page struct {
			Data       []json.RawMessage `json:"data"`
			NextCursor *string           `json:"nextCursor"`
		}
		if err := json.Unmarshal(res, &page); err != nil {
			return nil, err
		}
		items = append(items, page.Data...)
		if page.NextCursor == nil || *page.NextCursor == "" || len(page.Data) == 0 {
			break
		}
		cursor = *page.NextCursor
	}
	all := ParseItems(items)
	if after < 0 {
		after = 0
	}
	if after >= len(all) {
		return []ports.ChatEntry{}, nil
	}
	return all[after:], nil
}

type item struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Command          string          `json:"command"`
	AggregatedOutput *string         `json:"aggregatedOutput"`
	ExitCode         *int            `json:"exitCode"`
	Status           string          `json:"status"`
	Server           string          `json:"server"`
	Tool             string          `json:"tool"`
	Arguments        json.RawMessage `json:"arguments"`
	Result           json.RawMessage `json:"result"`
	Error            json.RawMessage `json:"error"`
	Query            string          `json:"query"`
	Path             string          `json:"path"`
	Changes          json.RawMessage `json:"changes"`
}

// ParseItems turns a thread's items into the conversation the owner reads:
// their messages, the answers and the tools run with what they gave back.
// Reasoning and bookkeeping are left out.
func ParseItems(raw []json.RawMessage) []ports.ChatEntry {
	out := []ports.ChatEntry{}
	var at time.Time
	add := func(e ports.ChatEntry) {
		e.Seq, e.At = len(out), at
		out = append(out, e)
	}
	for _, r := range raw {
		// thread/items/list wraps each item with its turn and times.
		var wrap struct {
			Item      json.RawMessage `json:"item"`
			StartedAt int64           `json:"startedAtMs"`
		}
		if json.Unmarshal(r, &wrap) == nil && len(wrap.Item) > 0 {
			r = wrap.Item
		}
		at = time.Time{}
		if wrap.StartedAt > 0 {
			at = time.UnixMilli(wrap.StartedAt).UTC()
		}
		var it item
		if json.Unmarshal(r, &it) != nil {
			continue
		}
		switch it.Type {
		case "userMessage":
			var parts []string
			for _, c := range it.Content {
				if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
					parts = append(parts, strings.TrimSpace(c.Text))
				}
			}
			if t := strings.Join(parts, "\n"); t != "" {
				add(ports.ChatEntry{Role: "user", Text: t})
			}
		case "agentMessage":
			if t := strings.TrimSpace(it.Text); t != "" {
				add(ports.ChatEntry{Role: "assistant", Text: t})
			}
		case "commandExecution":
			e := ports.ChatEntry{Role: "tool", Tool: "Bash", Input: cut(it.Command, 300)}
			if it.AggregatedOutput != nil {
				e.Result = cut(strings.TrimSpace(*it.AggregatedOutput), maxResult)
			}
			e.Failed = it.Status == "failed" || it.Status == "declined" || (it.ExitCode != nil && *it.ExitCode != 0)
			if e.Result == "" && it.Status != "inProgress" && !e.Failed {
				e.Result = "(no output)"
			}
			add(e)
		case "mcpToolCall":
			e := ports.ChatEntry{Role: "tool", Tool: "mcp__" + it.Server + "__" + it.Tool, Input: cut(compact(it.Arguments), 300)}
			if len(it.Error) > 0 && string(it.Error) != "null" {
				e.Failed, e.Result = true, cut(compact(it.Error), maxResult)
			} else if len(it.Result) > 0 && string(it.Result) != "null" {
				e.Result = cut(compact(it.Result), maxResult)
			}
			add(e)
		case "webSearch":
			add(ports.ChatEntry{Role: "tool", Tool: "WebSearch", Input: cut(it.Query, 300), Result: "done"})
		case "imageView":
			add(ports.ChatEntry{Role: "tool", Tool: "Read", Input: it.Path, Result: "image"})
		case "fileChange":
			add(ports.ChatEntry{Role: "tool", Tool: "Edit", Input: cut(compact(it.Changes), 300), Failed: it.Status == "failed" || it.Status == "declined", Result: it.Status})
		}
	}
	return out
}

func compact(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
