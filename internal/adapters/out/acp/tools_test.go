package acp

import (
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func TestSandboxGivesBackTheInstalledTools(t *testing.T) {
	tools := t.TempDir()
	t.Setenv(domain.EnvToolsDir, tools)
	wt := t.TempDir()
	_, args := sandboxArgs(domain.Policy{}, wt, "", wt, "true", nil)
	line := strings.Join(args, " ")
	if !strings.Contains(line, "--ro-bind "+tools+" "+tools) {
		t.Fatalf("tools not given back: %s", line)
	}
	t.Setenv(domain.EnvToolsDir, "relative")
	if _, args := sandboxArgs(domain.Policy{}, wt, "", wt, "true", nil); strings.Contains(strings.Join(args, " "), "relative") {
		t.Fatal("a relative folder is ignored")
	}
}
