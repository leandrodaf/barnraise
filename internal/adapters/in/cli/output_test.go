package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	e := &env{out: &out, errOut: &errOut, in: strings.NewReader("")}
	root := e.rootCmd()
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&errOut)
	err := root.Execute()
	return out.String(), err
}

func TestFilterThenOutputShow(t *testing.T) {
	wt := t.TempDir()
	t.Setenv("BARNRAISE_WORKTREE", wt)
	t.Setenv(domain.EnvRun, "run-1")
	t.Setenv(domain.EnvOutfilterDelegate, domain.DelegateOff)
	out, err := runCLI(t, "filter", "--", "sh", "-c", "seq 1 400; exit 3")
	if ec, ok := err.(exitCode); !ok || ec != 3 {
		t.Fatalf("exit %v", err)
	}
	m := regexp.MustCompile(`barnraise output show (o[0-9a-f]{8})`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no handle in:\n%s", out)
	}
	id := m[1]
	if !strings.Contains(out, "400") || strings.Contains(out, "\n200\n") {
		t.Fatalf("%s", out)
	}
	full, err := runCLI(t, "output", "show", id)
	if err != nil || strings.Count(full, "\n") != 400 {
		t.Fatalf("%d lines, %v", strings.Count(full, "\n"), err)
	}
	part, _ := runCLI(t, "output", "show", id, "--lines", "199:200")
	if part != "199\n200\n" {
		t.Fatalf("%q", part)
	}
	hits, _ := runCLI(t, "output", "show", id, "--grep", "^25[0-2]$")
	if hits != "250: 250\n251: 251\n252: 252\n" {
		t.Fatalf("%q", hits)
	}
	list, _ := runCLI(t, "output", "list")
	if !strings.Contains(list, id) || !strings.Contains(list, "seq 1 400") {
		t.Fatalf("%s", list)
	}
	stats, _ := runCLI(t, "--json", "output", "stats")
	var st filterStats
	if err := json.Unmarshal([]byte(stats), &st); err != nil || st.Runs != 1 || st.Saved <= 0 || st.ByTool[0].Tool != "generic" {
		t.Fatalf("%s %v", stats, err)
	}
	human, _ := runCLI(t, "output", "stats")
	if !strings.Contains(human, "1 outputs shortened") {
		t.Fatal(human)
	}
	if _, err := runCLI(t, "output", "show", "nope"); err == nil {
		t.Fatal("bad id")
	}
	if _, err := runCLI(t, "output", "show", id, "--lines", "9:2"); err == nil {
		t.Fatal("bad range")
	}
	// The folder is the session's.
	if got := outputsDir(); got != filepath.Join(wt, ".barnraise-session", "outputs") {
		t.Fatal(got)
	}
	// --raw prints everything.
	raw, _ := runCLI(t, "filter", "--raw", "--", "sh", "-c", "seq 1 100")
	if strings.Count(raw, "\n") != 100 {
		t.Fatalf("%q", raw)
	}
	if _, err := runCLI(t, "filter", "--bogus", "--", "true"); err == nil {
		t.Fatal("unknown flag")
	}
	empty, _ := runCLI(t, "--dir", t.TempDir(), "output", "list")
	if !strings.Contains(empty, "No kept outputs") {
		t.Fatal(empty)
	}
}

func TestParseRange(t *testing.T) {
	cases := map[string][2]int{"": {0, 0}, "5": {5, 5}, "3:": {3, 0}, ":9": {0, 9}, "2:4": {2, 4}}
	for in, want := range cases {
		a, b, err := parseRange(in)
		if err != nil || a != want[0] || b != want[1] {
			t.Errorf("%q: %d %d %v", in, a, b, err)
		}
	}
	for _, bad := range []string{"x", "0:2", "5:1"} {
		if _, _, err := parseRange(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
