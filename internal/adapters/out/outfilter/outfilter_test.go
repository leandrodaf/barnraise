package outfilter

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

func lineCount(s string) int { return strings.Count(s, "\n") + 1 }

func noise(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "> Task :lib:compile%d UP-TO-DATE\n", i)
	}
	return b.String()
}

func TestGradleFailure(t *testing.T) {
	out := noise(500) + `e: /src/app/Main.kt: (12, 5): Unresolved reference: foo
> Task :app:compileDebugKotlin FAILED

FAILURE: Build failed with an exception.

* What went wrong:
Execution failed for task ':app:compileDebugKotlin'.
> Compilation error. See log for more details

* Try:
> Run with --stacktrace option to get the stack trace.

BUILD FAILED in 12s
`
	s := FilterWithLog("gradle", []byte(out), 1, "/logs/a.log")
	for _, want := range []string{"Unresolved reference: foo", "compileDebugKotlin FAILED", "What went wrong", "Compilation error", "BUILD FAILED in 12s", "gradle: failed (exit 1)"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "UP-TO-DATE") || strings.Contains(s, "--stacktrace") {
		t.Errorf("noise kept:\n%s", s)
	}
	if !strings.HasSuffix(s, "full log: /logs/a.log") {
		t.Errorf("no log path:\n%s", s)
	}
}

func TestGradleTestFailureAndSuccess(t *testing.T) {
	out := noise(50) + `com.example.FooTest > addsNumbers FAILED
    java.lang.AssertionError: expected:<2> but was:<3>
        at com.example.FooTest.addsNumbers(FooTest.kt:10)

12 tests completed, 1 failed
BUILD FAILED in 3s
`
	s := Filter("gradle", []byte(out), 1)
	if !strings.Contains(s, "addsNumbers FAILED") || !strings.Contains(s, "expected:<2>") || !strings.Contains(s, "12 tests completed, 1 failed") {
		t.Fatal(s)
	}
	ok := Filter("gradle", []byte(noise(300)+"BUILD SUCCESSFUL in 40s\n"), 0)
	if ok != "gradle: passed\nBUILD SUCCESSFUL in 40s" {
		t.Fatalf("%q", ok)
	}
}

func TestGoTest(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, "ok  \texample.com/p%d\t0.01s\n", i)
	}
	b.WriteString(`--- FAIL: TestAdd (0.00s)
    add_test.go:10: got 3, want 2
FAIL
FAIL	example.com/calc	0.02s
? 	example.com/cmd	[no test files]
`)
	s := Filter("go", []byte(b.String()), 1)
	for _, want := range []string{"--- FAIL: TestAdd", "got 3, want 2", "FAIL\texample.com/calc", "30 packages ok"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "example.com/p3\t") {
		t.Errorf("ok lines should collapse:\n%s", s)
	}
}

func TestGoPanicAndBuildError(t *testing.T) {
	out := "# example.com/x\n./x.go:3:2: undefined: y\nFAIL\texample.com/x [build failed]\n"
	if s := Filter("go", []byte(out), 1); !strings.Contains(s, "undefined: y") {
		t.Fatal(s)
	}
	out = "panic: runtime error: index out of range [3] with length 2\n\ngoroutine 7 [running]:\nexample.com/x.f()\n"
	if s := Filter("go", []byte(out), 2); !strings.Contains(s, "index out of range") {
		t.Fatal(s)
	}
}

func TestJest(t *testing.T) {
	out := strings.Repeat("  console.log noise\n", 200) + ` FAIL  src/sum.test.js
  ● sum › adds

    expect(received).toBe(expected)

    Expected: 3
    Received: 4

Test Suites: 1 failed, 4 passed, 5 total
Tests:       1 failed, 20 passed, 21 total
`
	s := Filter(Detect("npx jest --ci"), []byte(out), 1)
	for _, want := range []string{"FAIL  src/sum.test.js", "● sum › adds", "Expected: 3", "Tests:       1 failed"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "console.log") {
		t.Errorf("noise:\n%s", s)
	}
}

func TestVitestAndPytest(t *testing.T) {
	v := " × src/a.test.ts > math > adds 2ms\nAssertionError: expected 4 to be 3\n Test Files  1 failed (1)\n      Tests  1 failed | 3 passed (4)\n"
	if s := Filter("vitest", []byte(v), 1); !strings.Contains(s, "AssertionError") || !strings.Contains(s, "× src/a.test.ts") {
		t.Fatal(s)
	}
	p := `____________________ test_add ____________________
    def test_add():
>       assert add(1, 1) == 3
E       assert 2 == 3
tests/test_calc.py:4: AssertionError
=========================== short test summary info ===========================
FAILED tests/test_calc.py::test_add - assert 2 == 3
========================= 1 failed, 9 passed in 0.12s =========================
`
	s := Filter(Detect("python -m pytest -q"), []byte(p), 1)
	for _, want := range []string{"E       assert 2 == 3", "FAILED tests/test_calc.py::test_add", "1 failed, 9 passed in 0.12s"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}

func TestMaestro(t *testing.T) {
	out := strings.Repeat("Tap on \"Next\"... COMPLETED\n", 100) + `Assert that "Welcome" is visible... FAILED
Element not found: Text matching regex: Welcome
[Failed] onboarding (12s)
1/3 flows failed
`
	s := Filter(Detect("maestro test flows/"), []byte(out), 1)
	for _, want := range []string{"Welcome\" is visible... FAILED", "Element not found", "[Failed] onboarding", "1/3 flows failed"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "Tap on") {
		t.Errorf("noise:\n%s", s)
	}
}

func TestGenericTailAndCap(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&b, "\x1b[32mline %d\x1b[0m\n", i)
	}
	s := FilterWithLog("make", []byte(b.String()), 2, "/l")
	if !strings.Contains(s, "line 999") || strings.Contains(s, "line 900\n") || strings.Contains(s, "\x1b") {
		t.Fatal(s)
	}
	if n := lineCount(s); n > 60 {
		t.Fatalf("%d lines", n)
	}
	// Many failures are capped too.
	var g strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&g, "e: /f%d.kt: boom\n", i)
	}
	g.WriteString("BUILD FAILED in 1s\n")
	s = FilterWithLog("gradle", []byte(g.String()), 1, "/l")
	if n := lineCount(s); n > 60 || !strings.Contains(s, "more lines omitted") || !strings.Contains(s, "BUILD FAILED in 1s") {
		t.Fatalf("%d lines:\n%s", n, s)
	}
}

func TestDetect(t *testing.T) {
	cases := map[string]string{
		"./gradlew :app:testDebugUnitTest": ToolGradle,
		"cd android && ./gradlew assemble": ToolGradle,
		"go test ./...":                    ToolGo,
		"npm test":                         ToolJS,
		"pnpm run test:unit":               ToolJS,
		"yarn vitest run":                  ToolVitest,
		"npx jest":                         ToolJest,
		"pytest -x":                        ToolPytest,
		"python3 -m pytest":                ToolPytest,
		"maestro test .maestro/login.yaml": ToolMaestro,
		"ls -la":                           ToolGeneric,
	}
	for cmd, want := range cases {
		if got := Detect(cmd); got != want {
			t.Errorf("Detect(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestRun(t *testing.T) {
	dir := t.TempDir()
	sum, code, logPath, err := Run(context.Background(), []string{"sh", "-c", "echo one; echo two >&2; exit 4"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if code != 4 {
		t.Fatalf("code %d", code)
	}
	full, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(full) != "one\ntwo\n" {
		t.Fatalf("log %q", full)
	}
	if !strings.Contains(sum, "two") || !strings.HasSuffix(sum, "full log: "+logPath) || !strings.HasSuffix(logPath, "-generic.log") {
		t.Fatalf("%s / %s", sum, logPath)
	}
	if _, _, _, err := Run(context.Background(), []string{"/no/such/bin"}, dir); err == nil {
		t.Fatal("expected start error")
	}
}
