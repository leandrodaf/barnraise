package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A fake gh on the PATH records what it was asked; `gh api` fails when it
// cannot reach the repository.
func fakeGH(t *testing.T, statusFails bool) (logFile string) {
	t.Helper()
	dir := t.TempDir()
	logFile = filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$@\" >> " + logFile + "\n" +
		"if [ \"$1\" = \"api\" ]; then exit " + map[bool]string{true: "1", false: "0"}[statusFails] + "; fi\n" +
		"if [ \"$1 $2\" = \"auth login\" ]; then cat > " + logFile + ".token; fi\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	ghRepairOn.Store(true)
	t.Cleanup(func() { ghRepairOn.Store(false) })
	return logFile
}

func TestRepairGHLoginWhenBroken(t *testing.T) {
	calls := fakeGH(t, true)
	d := &daemon{log: slog.New(slog.DiscardHandler)}
	d.repairGHLogin("github_pat_new", "acme/app")
	got, _ := os.ReadFile(calls)
	if !strings.Contains(string(got), "auth login -h github.com --git-protocol https --with-token") {
		t.Fatalf("calls %s", got)
	}
	if tok, _ := os.ReadFile(calls + ".token"); strings.TrimSpace(string(tok)) != "github_pat_new" {
		t.Fatalf("token %q", tok)
	}
}

// A gh that reaches the repository is left alone.
func TestRepairGHLoginLeavesAWorkingLogin(t *testing.T) {
	calls := fakeGH(t, false)
	d := &daemon{log: slog.New(slog.DiscardHandler)}
	d.repairGHLogin("github_pat_new", "acme/app")
	got, _ := os.ReadFile(calls)
	if strings.Contains(string(got), "auth login") {
		t.Fatalf("a working gh login was replaced: %s", got)
	}
}
