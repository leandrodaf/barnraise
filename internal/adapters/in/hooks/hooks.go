// Package hooks implements the provider hooks Barnraise installs in every
// agent session. The pre-tool decision is the hottest path: it reads the
// compiled policy from a local file and decides without any network; the
// daemon hears about blocks asynchronously.
package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/leandrodaf/barnraise/internal/adapters/out/imagex"
	"github.com/leandrodaf/barnraise/internal/adapters/out/outfilter"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// Input is the hook payload (Claude Code shape; Codex sends a compatible subset).
type Input struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	Cwd            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	Prompt         string          `json:"prompt"`
	Source         string          `json:"source"` // SessionStart: startup, resume, clear, compact
	StopHookActive bool            `json:"stop_hook_active"`
}

// ReadInput parses the hook payload from stdin.
func ReadInput(r io.Reader) (Input, error) {
	var in Input
	data, err := io.ReadAll(io.LimitReader(r, 4<<20))
	if err != nil {
		return in, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return in, nil
	}
	err = json.Unmarshal(data, &in)
	return in, err
}

// LoadPolicy reads the compiled policy the daemon wrote for this session.
func LoadPolicy(path string) (*domain.Policy, error) {
	if path == "" {
		return nil, fmt.Errorf("BARNRAISE_POLICY is not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p domain.Policy
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("policy %s: %w", path, err)
	}
	return &p, nil
}

// ToolCall normalizes a tool call of any provider into the policy's shape.
func ToolCall(in Input) domain.ToolCall {
	var ti map[string]any
	_ = json.Unmarshal(in.ToolInput, &ti)
	str := func(k string) string {
		if v, ok := ti[k].(string); ok {
			return v
		}
		return ""
	}
	tc := domain.ToolCall{Tool: in.ToolName}
	switch in.ToolName {
	case "Bash", "shell", "exec_command", "local_shell":
		tc.Command = str("command")
		if tc.Command == "" {
			if arr, ok := ti["command"].([]any); ok {
				var parts []string
				for _, a := range arr {
					parts = append(parts, fmt.Sprint(a))
				}
				tc.Command = strings.Join(parts, " ")
			}
		}
	case "Write", "Edit", "MultiEdit", "NotebookEdit", "apply_patch", "str_replace_based_edit_tool":
		tc.Write = true
		for _, k := range []string{"file_path", "notebook_path", "path"} {
			if v := str(k); v != "" {
				tc.Paths = append(tc.Paths, v)
			}
		}
		if in.ToolName == "apply_patch" {
			tc.Paths = append(tc.Paths, patchFiles(str("input")+str("patch"))...)
		}
	case "Read", "NotebookRead", "Glob", "Grep", "LS":
		for _, k := range []string{"file_path", "notebook_path", "path"} {
			if v := str(k); v != "" {
				tc.Paths = append(tc.Paths, v)
			}
		}
	case "WebFetch":
		tc.URL = str("url")
	default:
		// MCP tools and others: check any path-looking argument as a read.
		for _, k := range []string{"file_path", "path"} {
			if v := str(k); v != "" {
				tc.Paths = append(tc.Paths, v)
			}
		}
	}
	return tc
}

var patchFileRe = regexp.MustCompile(`(?m)^\*\*\* (?:Add|Update|Delete) File: (.+)$`)

func patchFiles(p string) []string {
	var out []string
	for _, m := range patchFileRe.FindAllStringSubmatch(p, -1) {
		out = append(out, strings.TrimSpace(m[1]))
	}
	return out
}

// ExistsOnMain reports whether a worktree-relative path exists on main
// (locked files may be created but not changed).
func ExistsOnMain(worktree, mainBranch string) func(string) bool {
	return func(rel string) bool {
		if worktree == "" {
			return false
		}
		for _, ref := range []string{"refs/remotes/origin/" + mainBranch, "refs/heads/" + mainBranch} {
			cmd := exec.Command("git", "-C", worktree, "cat-file", "-e", ref+":"+filepath.ToSlash(rel))
			if cmd.Run() == nil {
				return true
			}
		}
		return false
	}
}

// PreToolResult is the hook's decision and what to print.
type PreToolResult struct {
	Decision domain.Decision
	Output   map[string]any // printed as JSON on stdout; nil prints nothing
	Call     domain.ToolCall
}

// lookRTK finds rtk for commands without a native filter (a variable for the tests).
var lookRTK = outfilter.LookRTK

// wrapped reports whether a command goes through `barnraise filter --`: a
// simple command (no shell syntax) that is a tool with a native filter, or
// one rtk may take when outfilter.delegate allows it and rtk is installed.
func wrapped(command string) bool {
	if strings.Contains(command, "barnraise filter") || strings.ContainsAny(command, "|;&><`$") {
		return false
	}
	if _, lead := outfilter.DetectLeading(command); lead {
		return true
	}
	return outfilter.Delegable(outfilter.DelegateFromEnv(), outfilter.ToolGeneric, command) && lookRTK() != ""
}

// PreTool decides a tool call. bin is the barnraise binary used for the filter wrapper.
func PreTool(in Input, pol *domain.Policy, exists func(string) bool, bin string) PreToolResult {
	tc := ToolCall(in)
	pc := *pol
	if in.Cwd != "" {
		pc.Cwd = in.Cwd // relative paths are the shell's, from where the agent is
	}
	d := pc.Decide(tc, exists)
	if !d.Allow {
		return PreToolResult{Decision: d, Call: tc, Output: map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName": "PreToolUse", "permissionDecision": "deny",
			"permissionDecisionReason": "Barnraise policy: " + d.Reason + ". This is blocked by design; continue without it or deliver with a concern.",
		}}}
	}
	if in.ToolName == "Read" && len(tc.Paths) == 1 && imagex.IsImage(tc.Paths[0]) && pol.Worktree != "" && !strings.Contains(tc.Paths[0], "/shots/full/") {
		src := tc.Paths[0]
		if !filepath.IsAbs(src) {
			src = filepath.Join(pol.Worktree, src)
		}
		dst, reduced, err := imagex.ReduceFile(src, filepath.Join(pol.Worktree, ".barnraise-session", "shots"), imagex.DefaultWidth)
		if err == nil && reduced {
			var ti map[string]any
			_ = json.Unmarshal(in.ToolInput, &ti)
			if ti == nil {
				ti = map[string]any{}
			}
			ti["file_path"] = dst
			return PreToolResult{Decision: d, Call: tc, Output: map[string]any{"hookSpecificOutput": map[string]any{
				"hookEventName": "PreToolUse", "permissionDecision": "allow",
				"permissionDecisionReason": fmt.Sprintf("image reduced to %d px wide to save tokens; for a full-resolution region use `barnraise image crop %s x,y,w,h`", imagex.DefaultWidth, tc.Paths[0]),
				"updatedInput":             ti,
			}}}
		}
	}
	if tc.Command != "" && bin != "" && wrapped(tc.Command) {
		var ti map[string]any
		_ = json.Unmarshal(in.ToolInput, &ti)
		if ti == nil {
			ti = map[string]any{}
		}
		ti["command"] = bin + " filter -- " + tc.Command
		return PreToolResult{Decision: d, Call: tc, Output: map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName": "PreToolUse", "permissionDecision": "allow",
			"permissionDecisionReason": "output filtered by Barnraise; the full text stays readable with the `barnraise output show <id>` printed at the end",
			"updatedInput":             ti,
		}}}
	}
	return PreToolResult{Decision: d, Call: tc}
}

// AdditionalContext builds the output that injects text into the session.
func AdditionalContext(event, text string) map[string]any {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event, "additionalContext": text}}
}

// BlockStop keeps the agent going when it tries to stop without delivering.
func BlockStop(reason string) map[string]any {
	return map[string]any{"decision": "block", "reason": reason}
}

// Write prints a hook output.
func Write(w io.Writer, out map[string]any) error {
	if out == nil {
		return nil
	}
	return json.NewEncoder(w).Encode(out)
}

// StatusInput is the status line payload; fields vary by version, so
// parsing is tolerant.
type StatusInput struct {
	Model struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"model"`
	ContextWindow struct {
		UsedPercentage float64 `json:"used_percentage"`
	} `json:"context_window"`
	RateLimits map[string]json.RawMessage `json:"rate_limits"`
	Cost       struct {
		TotalCostUSD float64 `json:"total_cost_usd"`
	} `json:"cost"`
}

// Limits extracts 5-hour and weekly usage from a status line payload.
func (s StatusInput) Limits() (fiveHour, weekly, top float64, fiveReset, weekReset string, ok bool) {
	get := func(keys ...string) (float64, string, bool) {
		for _, k := range keys {
			raw, found := s.RateLimits[k]
			if !found {
				continue
			}
			var v struct {
				UsedPercentage *float64 `json:"used_percentage"`
				Utilization    *float64 `json:"utilization"`
				ResetsAt       any      `json:"resets_at"`
			}
			if json.Unmarshal(raw, &v) != nil {
				continue
			}
			pct := 0.0
			switch {
			case v.UsedPercentage != nil:
				pct = *v.UsedPercentage
			case v.Utilization != nil:
				pct = *v.Utilization
				if pct <= 1 {
					pct *= 100
				}
			default:
				continue
			}
			return pct, fmt.Sprint(v.ResetsAt), true
		}
		return 0, "", false
	}
	f, fr, ok1 := get("five_hour", "5h", "session")
	w, wr, ok2 := get("seven_day", "weekly", "week")
	t, _, _ := get("seven_day_opus", "seven_day_top", "weekly_top")
	return f, w, t, fr, wr, ok1 || ok2
}
