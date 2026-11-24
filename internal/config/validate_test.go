package config

import (
	"strings"
	"testing"
)

func TestValidateLeavesPluginProgramsToTheMachine(t *testing.T) {
	root := t.TempDir()
	write(t, root, FileName, "project: demo\ntemplate: blank\nplugins:\n  jira: { command: no-such-program-barnraise }\n")
	checked, err := Check(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(checked, "\n"), "plugin jira runs no-such-program-barnraise, which is not on the PATH") {
		t.Fatalf("check: %v", checked)
	}
	validated, err := Validate(root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(validated, "\n"), "plugin jira") {
		t.Fatalf("validate: %v", validated)
	}
}
