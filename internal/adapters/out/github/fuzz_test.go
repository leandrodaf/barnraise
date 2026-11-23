package github

import (
	"os"
	"path/filepath"
	"testing"
)

func FuzzParseEvent(f *testing.F) {
	for typ, file := range map[string]string{
		"push": "hook_push.json", "check_run": "hook_check_run.json", "issue_comment": "hook_issue_comment.json",
		"issues": "hook_issues.json", "workflow_run": "hook_workflow_run.json",
	} {
		b, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(typ, b)
	}
	for _, typ := range []string{"ping", "pull_request", "pull_request_review", "unknown", ""} {
		f.Add(typ, []byte(`{"repository":{"full_name":"a/b"}}`))
		f.Add(typ, []byte(`null`))
	}
	f.Fuzz(func(t *testing.T, typ string, body []byte) {
		_, _ = ParseEvent(typ, body)
	})
}
