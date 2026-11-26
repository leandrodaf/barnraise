package claudechat

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// projectsDir is where Claude Code keeps transcripts: the account's login
// folder, else CLAUDE_CONFIG_DIR, else ~/.claude.
func projectsDir(configDir string) string {
	if configDir != "" {
		return filepath.Join(configDir, "projects")
	}
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "projects")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

var notWordRe = regexp.MustCompile(`[^A-Za-z0-9]`)

// TranscriptPath is the file Claude Code writes the session's conversation
// to: its folder named after the working directory, every character but
// letters and digits turned into a dash.
func TranscriptPath(configDir, dir, sessionID string) string {
	return filepath.Join(projectsDir(configDir), notWordRe.ReplaceAllString(dir, "-"), sessionID+".jsonl")
}

// Transcript reads the conversation of sessionID run in dir, from entry
// after on.
func (s *Session) Transcript(configDir, sessionID, dir string, after int) ([]ports.ChatEntry, error) {
	f, err := os.Open(TranscriptPath(configDir, dir, sessionID))
	if errors.Is(err, fs.ErrNotExist) {
		return []ports.ChatEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	all := ParseTranscript(f)
	if after < 0 {
		after = 0
	}
	if after >= len(all) {
		return []ports.ChatEntry{}, nil
	}
	return all[after:], nil
}

type line struct {
	Type        string          `json:"type"`
	IsMeta      bool            `json:"isMeta"`
	IsSidechain bool            `json:"isSidechain"`
	Timestamp   time.Time       `json:"timestamp"`
	Message     json.RawMessage `json:"message"`
}

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// maxResult bounds what a tool's result shows in the conversation.
const maxResult = 1200

// ParseTranscript turns a Claude Code transcript into the conversation the
// owner reads: their messages, the assistant's answers and the tools it ran
// with what they gave back. Its own bookkeeping, hidden context and
// subagents' turns are left out.
func ParseTranscript(r interface{ Read([]byte) (int, error) }) []ports.ChatEntry {
	var out []ports.ChatEntry
	tools := map[string]int{} // tool_use id -> index in out
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	add := func(e ports.ChatEntry) {
		e.Seq = len(out)
		out = append(out, e)
	}
	for sc.Scan() {
		var l line
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.IsMeta || l.IsSidechain || (l.Type != "user" && l.Type != "assistant") {
			continue
		}
		var m message
		if json.Unmarshal(l.Message, &m) != nil {
			continue
		}
		var text string
		if json.Unmarshal(m.Content, &text) == nil {
			if t := ownerText(text); t != "" && l.Type == "user" {
				add(ports.ChatEntry{Role: "user", Text: t, At: l.Timestamp})
			}
			continue
		}
		var blocks []block
		if json.Unmarshal(m.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			switch {
			case b.Type == "text" && l.Type == "assistant":
				if t := strings.TrimSpace(b.Text); t != "" {
					add(ports.ChatEntry{Role: "assistant", Text: t, At: l.Timestamp})
				}
			case b.Type == "text" && l.Type == "user":
				if t := ownerText(b.Text); t != "" {
					add(ports.ChatEntry{Role: "user", Text: t, At: l.Timestamp})
				}
			case b.Type == "tool_use":
				tools[b.ID] = len(out)
				add(ports.ChatEntry{Role: "tool", Tool: b.Name, Input: toolInput(b.Input), At: l.Timestamp})
			case b.Type == "tool_result":
				if i, ok := tools[b.ToolUseID]; ok {
					out[i].Result = cut(resultText(b.Content), maxResult)
					out[i].Failed = b.IsError
				}
			}
		}
	}
	return out
}

// ownerText is a message the owner typed, or "" for what Claude Code itself
// put in the user's turn (command output, reminders, interruptions).
func ownerText(t string) string {
	t = strings.TrimSpace(t)
	if t == "" || strings.HasPrefix(t, "<") || strings.HasPrefix(t, "[Request interrupted") || strings.HasPrefix(t, "Caveat:") {
		return ""
	}
	return t
}

// toolInput is the one line that says what a tool was asked: a command, a
// path, a pattern, or the input itself.
func toolInput(raw json.RawMessage) string {
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		return cut(string(raw), 200)
	}
	for _, k := range []string{"command", "file_path", "path", "pattern", "query", "url", "prompt", "description"} {
		if v, ok := in[k].(string); ok && v != "" {
			return cut(v, 300)
		}
	}
	b, _ := json.Marshal(in)
	return cut(string(b), 300)
}

// resultText reads a tool result: a string, or text blocks.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var blocks []block
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				parts = append(parts, strings.TrimSpace(b.Text))
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
