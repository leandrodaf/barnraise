package hooks

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func FuzzHookInput(f *testing.F) {
	for _, s := range []string{
		"", "   ", "{}", "null", "[]", "x",
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git push origin main"}}`,
		`{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"/w/I-1/.github/workflows/ci.yml","content":"x"}}`,
		`{"hook_event_name":"PreToolUse","tool_name":"apply_patch","tool_input":{"input":"*** Begin Patch\n*** Update File: .barnraise/x.md\n*** Add File: a\n*** Delete File: b\n"}}`,
		`{"hook_event_name":"PreToolUse","tool_name":"MultiEdit","tool_input":{"file_path":7,"edits":[{"old_string":1}]}}`,
		`{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/home/agent/.codex/auth.json"}}`,
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"go test ./..."}}`,
		`{"tool_name":"Glob","tool_input":{"pattern":"**","path":null}}`,
		`{"tool_name":"WebFetch","tool_input":{"url":"http://x"}}`,
		`{"tool_name":"shell","tool_input":{"command":["bash","-lc","cat ~/.ssh/id_rsa"]}}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		in, err := ReadInput(bytes.NewReader(data))
		if err != nil {
			return
		}
		_ = ToolCall(in)
		pol := domain.DefaultPolicy()
		pol.Home = "/home/agent"
		pol.Worktree = "/w/I-1"
		res := PreTool(in, &pol, func(string) bool { return true }, "barnraise")
		if res.Output != nil {
			if _, err := json.Marshal(res.Output); err != nil {
				t.Fatalf("hook output is not JSON: %v", err)
			}
		}
		if !res.Decision.Allow && res.Output == nil {
			t.Fatalf("deny without output: %+v", res.Decision)
		}
	})
}
