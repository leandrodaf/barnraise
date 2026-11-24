package astgrep

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/execx"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

const errorMatch = `{"text":"x.unwrap()","range":{"start":{"line":4,"column":8},"end":{"line":4,"column":18}},"file":"src/main.rs","lines":"    let y = x.unwrap();","language":"Rust","ruleId":"no-unwrap","severity":"error","message":"Do not unwrap: return the error","note":null}`
const warnMatch = `{"text":"println!","range":{"start":{"line":0,"column":0}},"file":"src/lib.rs","lines":"println!(\"hi\")","language":"Rust","ruleId":"no-print","severity":"warning","message":"","note":null}`

// fakeASTGrep writes an ast-grep stand-in: it records its arguments and
// prints the given JSON stream with the given exit code.
func fakeASTGrep(t *testing.T, stdout, stderr string, exit int) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	errf := filepath.Join(dir, "err")
	args := filepath.Join(dir, "args")
	_ = os.WriteFile(out, []byte(stdout), 0o600)
	_ = os.WriteFile(errf, []byte(stderr), 0o600)
	script := "#!/bin/sh\npwd > '" + args + "'\necho \"$@\" >> '" + args + "'\ncat '" + out + "'\ncat '" + errf + "' >&2\nexit " + map[int]string{0: "0", 1: "1", 2: "2"}[exit] + "\n"
	bin := filepath.Join(dir, "ast-grep")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, args
}

func project(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"rust/src/main.rs", "rust/src/lib.rs", "rust/sgconfig.yml", "web/app.ts"} {
		p := filepath.Join(dir, filepath.FromSlash(f))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("x"), 0o644)
	}
	return dir
}

func spec(dir string, touched ...string) ports.CheckSpec {
	return ports.CheckSpec{Def: domain.CheckDef{Name: "structure", RunsOn: domain.RunsOnASTGrep, Dir: "rust", Run: "--filter no-", Timeout: time.Minute}, Dir: dir, Touched: touched}
}

func TestErrorRulesFailWithWhereAndWhy(t *testing.T) {
	bin, args := fakeASTGrep(t, errorMatch+"\n"+warnMatch+"\n", "", 1)
	dir := project(t)
	r := New(Options{Exec: execx.New(), LookPath: func(string) (string, error) { return bin, nil }})
	res, log, err := r.Check(context.Background(), spec(dir, "rust/src/main.rs", "rust/src/lib.rs", "rust/src/gone.rs", "web/app.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed || res.MainRed {
		t.Fatalf("an error rule must fail: %+v", res)
	}
	for _, want := range []string{"1 structural rule violation at severity error", "- src/main.rs:5:9 no-unwrap [error]: Do not unwrap: return the error", "- src/lib.rs:1:1 no-print [warning]: println!(\"hi\")"} {
		if !strings.Contains(res.Summary, want) {
			t.Errorf("summary misses %q:\n%s", want, res.Summary)
		}
	}
	if !strings.Contains(string(log), "no-unwrap") {
		t.Fatal("the log keeps the raw output")
	}
	got, _ := os.ReadFile(args)
	lines := strings.Split(strings.TrimSpace(string(got)), "\n")
	if wd, _ := filepath.EvalSymlinks(filepath.Join(dir, "rust")); len(lines) != 2 || (lines[0] != filepath.Join(dir, "rust") && lines[0] != wd) ||
		lines[1] != "scan --json=stream --filter no- src/lib.rs src/main.rs" {
		t.Fatalf("ast-grep runs in the check's folder on the touched files that exist there: %q", lines)
	}
}

func TestWarningsAloneDoNotFail(t *testing.T) {
	bin, _ := fakeASTGrep(t, warnMatch+"\n", "", 0)
	r := New(Options{Exec: execx.New(), LookPath: func(string) (string, error) { return bin, nil }})
	res, _, err := r.Check(context.Background(), spec(project(t)))
	if err != nil || !res.Passed || !strings.Contains(res.Summary, "1 other match") {
		t.Fatalf("warnings: %+v %v", res, err)
	}
	clean, _ := fakeASTGrep(t, "", "", 0)
	r = New(Options{Exec: execx.New(), LookPath: func(string) (string, error) { return clean, nil }})
	res, _, _ = r.Check(context.Background(), spec(project(t), "rust/src/main.rs"))
	if !res.Passed || res.Summary != "no match of the structural rules (ast-grep)" {
		t.Fatalf("clean: %+v", res)
	}
	// Nothing of the change is under the check's folder.
	res, _, _ = r.Check(context.Background(), spec(project(t), "web/app.ts"))
	if !res.Passed || !strings.Contains(res.Summary, "no file of the change") {
		t.Fatalf("out of folder: %+v", res)
	}
}

func TestBrokenSetupHoldsForTheOwner(t *testing.T) {
	r := New(Options{Exec: execx.New(), Machine: "desk", LookPath: func(string) (string, error) { return "", exec.ErrNotFound }})
	res, _, err := r.Check(context.Background(), spec(project(t)))
	if err != nil || res.Passed || !res.MainRed || !strings.Contains(res.Summary, "not installed on desk") {
		t.Fatalf("missing ast-grep: %+v %v", res, err)
	}
	bin, _ := fakeASTGrep(t, "", "Error: No ast-grep project configuration is found.\nhelp", 2)
	r = New(Options{Exec: execx.New(), LookPath: func(string) (string, error) { return bin, nil }})
	res, _, _ = r.Check(context.Background(), spec(project(t)))
	if !res.MainRed || !strings.Contains(res.Summary, "exit 2): Error: No ast-grep project configuration is found.") {
		t.Fatalf("no sgconfig: %+v", res)
	}
	garbage, _ := fakeASTGrep(t, "not json\n", "", 1)
	r = New(Options{Exec: execx.New(), LookPath: func(string) (string, error) { return garbage, nil }})
	res, _, _ = r.Check(context.Background(), spec(project(t)))
	if !res.MainRed {
		t.Fatalf("unreadable output: %+v", res)
	}
}

func TestManyMatchesAreCut(t *testing.T) {
	ms := make([]Match, maxListed+5)
	for i := range ms {
		ms[i] = Match{File: "a.go", RuleID: "r", Severity: "error", Message: "m"}
		ms[i].Range.Start.Line = i
	}
	ok, sum := verdict(ms)
	if ok || !strings.Contains(sum, "… and 5 more in the log") || !strings.HasPrefix(sum, "35 structural rule violations") {
		t.Fatalf("%v %s", ok, sum)
	}
	if rank("hint") != 0 || rank("info") != 1 {
		t.Fatal("rank")
	}
}

// The real ast-grep, when installed: a rule file and a match.
func TestRealASTGrep(t *testing.T) {
	if testing.Short() {
		t.Skip("real tool")
	}
	if _, err := exec.LookPath(Binary); err != nil {
		t.Skip("ast-grep is not installed")
	}
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "rules"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "sgconfig.yml"), []byte("ruleDirs: [rules]\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "rules", "no-eval.yml"), []byte("id: no-eval\nlanguage: JavaScript\nseverity: error\nmessage: no eval\nrule: {pattern: eval($A)}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "a.js"), []byte("eval(x)\n"), 0o644)
	r := New(Options{Exec: execx.New()})
	res, _, err := r.Check(context.Background(), ports.CheckSpec{Def: domain.CheckDef{Name: "sg", RunsOn: domain.RunsOnASTGrep}, Dir: dir, Touched: []string{"a.js"}})
	if err != nil || res.Passed || !strings.Contains(res.Summary, "a.js:1:1 no-eval [error]: no eval") {
		t.Fatalf("real ast-grep: %+v %v", res, err)
	}
}
