package outfilter

import (
	"fmt"
	"strings"
	"testing"
)

func mustContain(t *testing.T, s string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in:\n%s", w, s)
		}
	}
}

func mustNotContain(t *testing.T, s string, bad ...string) {
	t.Helper()
	for _, w := range bad {
		if strings.Contains(s, w) {
			t.Errorf("kept %q in:\n%s", w, s)
		}
	}
}

func repeatf(n int, format string) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, format, i)
	}
	return b.String()
}

func TestShortOutputOfTransparentToolsIsUnchanged(t *testing.T) {
	out := "On branch main\nYour branch is up to date with 'origin/main'.\n\nnothing to commit, working tree clean\n"
	s, short := Summarize(Detect("git status"), []byte(out), 0, "full output: x")
	if short || s != strings.TrimRight(out, "\n") {
		t.Fatalf("%v %q", short, s)
	}
	s, short = Summarize(ToolGrep, []byte("\x1b[35ma.go\x1b[0m:3:func main() {\n"), 0, "f")
	if short || s != "a.go:3:func main() {" {
		t.Fatalf("%q", s)
	}
}

func TestGitStatus(t *testing.T) {
	out := `On branch feature/login
Your branch is ahead of 'origin/feature/login' by 2 commits.
  (use "git push" to publish your local commits)

Changes to be committed:
  (use "git restore --staged <file>..." to unstage)
	modified:   internal/app/login.go
	new file:   internal/app/login_test.go

Changes not staged for commit:
  (use "git add <file>..." to update what will be committed)
  (use "git restore <file>..." to discard changes in working directory)
` + repeatf(30, "\tmodified:   web/src/pages/page%d.tsx\n") + `
Untracked files:
  (use "git add <file>..." to include in what will be committed)
` + repeatf(80, "\tnode_modules/.cache/f%d\n") + "\tnotes.md\n\tscripts/new.sh\n"
	s := Filter(Detect("git status"), []byte(out), 0)
	mustContain(t, s, "On branch feature/login", "ahead of 'origin/feature/login' by 2 commits", "staged (2):", "modified:   internal/app/login.go",
		"not staged (30):", "page0.tsx", "… 15 more", "untracked (82):", "node_modules/ (80 files)", "notes.md")
	mustNotContain(t, s, "(use \"git", "page29.tsx")
	if n := lineCount(s); n > 45 {
		t.Fatalf("%d lines", n)
	}
	short := repeatf(60, " M src/f%d.go\n") + "?? new.txt\n"
	s = Filter(ToolGitStatus, []byte(short), 0)
	mustContain(t, s, "M (60):", "src/f0.go", "?? (1):", "new.txt")
}

func TestGitDiff(t *testing.T) {
	var b strings.Builder
	b.WriteString("commit 1f3a9c2d7e\nAuthor: Ana <ana@example.com>\nDate:   Mon Nov 23 10:00:00 2026\n\n    fix(api): keep cents as integers\n\n")
	b.WriteString("diff --git a/api/money.go b/api/money.go\nindex 1111111..2222222 100644\n--- a/api/money.go\n+++ b/api/money.go\n@@ -10,7 +10,7 @@ func Total() {\n")
	b.WriteString(repeatf(80, "-\told := %d\n"))
	b.WriteString(repeatf(80, "+\tcents := %d\n"))
	b.WriteString("diff --git a/package-lock.json b/package-lock.json\nindex 3..4 100644\n--- a/package-lock.json\n+++ b/package-lock.json\n@@ -1,3 +1,3 @@\n")
	b.WriteString(repeatf(500, "+    \"integrity\": \"sha512-%d\"\n"))
	b.WriteString("diff --git a/logo.png b/logo.png\nnew file mode 100644\nBinary files /dev/null and b/logo.png differ\n")
	b.WriteString("diff --git a/api/money_test.go b/api/money_test.go\nnew file mode 100644\n--- /dev/null\n+++ b/api/money_test.go\n@@ -0,0 +1,3 @@\n+package api\n+\n+func TestCents(t *testing.T) {}\n")
	s := Filter(Detect("git show HEAD"), []byte(b.String()), 0)
	mustContain(t, s, "fix(api): keep cents as integers", "4 files changed, +583 -80", "api/money.go +80 -80", "package-lock.json +500 -0, not shown",
		"logo.png binary", "--- api/money.go", "@@ -10,7 +10,7 @@", "… 121 more lines of api/money.go", "--- api/money_test.go", "+func TestCents")
	mustNotContain(t, s, "sha512-", "index 1111111")
}

func TestGitLog(t *testing.T) {
	var b strings.Builder
	for i := range 60 {
		fmt.Fprintf(&b, "commit %040x\nAuthor: Dev %d <dev@example.com>\nDate:   Mon Nov %d 10:00:00 2026 +0000\n\n    feat(x): change number %d\n\n    Longer body that nobody needs here.\n\n", i+1, i, i%28+1, i)
	}
	s := Filter(Detect("git log -n 60"), []byte(b.String()), 0)
	mustContain(t, s, "0000000000 feat(x): change number 0 (Dev 0, Mon Nov 1 10:00:00 2026 +0000)", "… 20 more commits")
	mustNotContain(t, s, "Longer body", "change number 45")
	// --oneline output has no commit headers: the head is kept.
	s = Filter(ToolGitLog, []byte(repeatf(100, "abc%04d some subject\n")), 0)
	mustContain(t, s, "abc0000 some subject", "abc0039", "more lines omitted")
	mustNotContain(t, s, "abc0099")
}

func TestGitSync(t *testing.T) {
	out := "Cloning into 'shop'...\n" + repeatf(50, "remote: Counting objects: %d%% (1/100)\n") +
		"remote: Total 3000 (delta 10), reused 0 (delta 0)\nReceiving objects: 100% (3000/3000), 2.1 MiB | 5 MiB/s, done.\nResolving deltas: 100% (10/10), done.\nwarning: redirecting to https://example.com/shop.git/\n"
	s := Filter(Detect("git clone https://example.com/shop"), []byte(out), 0)
	mustContain(t, s, "Cloning into 'shop'", "warning: redirecting", "progress lines dropped")
	mustNotContain(t, s, "Counting objects")
	rejected := repeatf(45, "remote: Enumerating objects: %d\n") + " ! [rejected]        main -> main (fetch first)\nerror: failed to push some refs to 'origin'\n"
	s = Filter(Detect("git push origin main"), []byte(rejected), 1)
	mustContain(t, s, "[rejected]", "failed to push", "exit 1")
}

func TestCargo(t *testing.T) {
	out := repeatf(120, "   Compiling dep%d v0.1.0\n") + `error[E0425]: cannot find value ` + "`totl`" + ` in this scope
  --> src/lib.rs:12:5
   |
12 |     totl
   |     ^^^^ help: a local variable with a similar name exists: ` + "`total`" + `

error: could not compile ` + "`shop`" + ` (lib) due to 1 previous error
`
	s := Filter(Detect("cargo build --release"), []byte(out), 101)
	mustContain(t, s, "error[E0425]: cannot find value", "--> src/lib.rs:12:5", "similar name exists", "could not compile", "120 build progress lines")
	mustNotContain(t, s, "Compiling dep")

	test := repeatf(200, "test tests::case_%d ... ok\n") + `test tests::adds ... FAILED

failures:

---- tests::adds stdout ----
thread 'tests::adds' panicked at src/lib.rs:30:9:
assertion ` + "`left == right`" + ` failed
  left: 3
 right: 4

failures:
    tests::adds

test result: FAILED. 200 passed; 1 failed; 0 ignored; 0 measured; 0 filtered out; finished in 0.02s
`
	s = Filter(Detect("cargo test"), []byte(test), 101)
	mustContain(t, s, "test tests::adds ... FAILED", "---- tests::adds stdout ----", "panicked at src/lib.rs:30:9", "left: 3", "test result: FAILED. 200 passed; 1 failed", "200 tests ok")
	mustNotContain(t, s, "case_7 ")
	ok := Filter(ToolCargo, []byte(repeatf(80, "   Compiling dep%d v0.1.0\n")+"    Finished `release` profile [optimized] target(s) in 41.20s\n"), 0)
	mustContain(t, ok, "cargo: passed", "Finished `release`", "80 build progress lines")
}

func TestMaven(t *testing.T) {
	out := repeatf(300, "Downloaded from central: https://repo.maven.apache.org/maven2/org/x/lib%d.jar (12 kB at 300 kB/s)\n") +
		`[INFO] -------------------------------------------------------
[INFO]  T E S T S
[INFO] -------------------------------------------------------
[INFO] Running com.shop.CartTest
[ERROR] Tests run: 12, Failures: 1, Errors: 0, Skipped: 0, Time elapsed: 0.2 s <<< FAILURE! -- in com.shop.CartTest
[ERROR] com.shop.CartTest.addsItems -- Time elapsed: 0.01 s <<< FAILURE!
    org.opentest4j.AssertionFailedError: expected: <3> but was: <2>
        at com.shop.CartTest.addsItems(CartTest.java:21)
[WARNING] Some deprecation
[WARNING] Some deprecation
[INFO] BUILD FAILURE
[INFO] Total time:  14.2 s
[ERROR] Failed to execute goal org.apache.maven.plugins:maven-surefire-plugin:3.2.5:test (default-test) on project shop: There are test failures.
[ERROR]
[ERROR] -> [Help 1]
[ERROR] Re-run Maven using the -X switch to enable full debug logging.
`
	s := Filter(Detect("./mvnw -B test"), []byte(out), 1)
	mustContain(t, s, "CartTest.addsItems", "expected: <3> but was: <2>", "Failed to execute goal", "BUILD FAILURE", "Total time", "300 download lines", "2 warning lines")
	mustNotContain(t, s, "Re-run Maven", "[Help 1]", "lib7.jar")
}

func TestMake(t *testing.T) {
	out := repeatf(100, "cc -c src/f%d.c -o build/f.o\n") + `src/main.c:14:9: error: use of undeclared identifier 'cnt'
   14 |   return cnt;
      |          ^
make[1]: *** [Makefile:20: build/main.o] Error 1
make: *** [Makefile:8: all] Error 2
`
	s := Filter(Detect("make -j8"), []byte(out), 2)
	mustContain(t, s, "undeclared identifier 'cnt'", "return cnt;", "make[1]: ***", "make: *** [Makefile:8: all] Error 2")
	mustNotContain(t, s, "cc -c src/f5.c")
}

func TestTSC(t *testing.T) {
	var b strings.Builder
	for range 20 {
		b.WriteString("src/api/client.ts(14,7): error TS2322: Type 'string' is not assignable to type 'number'.\n")
	}
	b.WriteString("src/app.tsx:3:10 - error TS2305: Module '\"./api\"' has no exported member 'fetchAll'.\n\n3 import { fetchAll } from \"./api\";\n           ~~~~~~~~\n\nFound 21 errors in 2 files.\n")
	s := Filter(Detect("npx tsc --noEmit"), []byte(b.String()), 2)
	mustContain(t, s, "error TS2322", "(×20)", "error TS2305", "Found 21 errors in 2 files.")
	if strings.Count(s, "TS2322") != 1 {
		t.Fatalf("not deduplicated:\n%s", s)
	}
}

func TestGolangciLint(t *testing.T) {
	out := `level=info msg="[config_reader] Config search paths: [./ /src]"
level=info msg="[lintersdb] Active 12 linters"
internal/app/a.go:12:2: ineffectual assignment to err (ineffassign)
	err = nil
	^
internal/app/b.go:40:15: Error return value of ` + "`f.Close`" + ` is not checked (errcheck)
	defer f.Close()
	             ^
2 issues:
* errcheck: 1
* ineffassign: 1
`
	s := Filter(Detect("golangci-lint run ./..."), []byte(out), 1)
	mustContain(t, s, "a.go:12:2: ineffectual assignment", "b.go:40:15: Error return value", "2 issues:", "* errcheck: 1", "2 log lines")
	mustNotContain(t, s, "Config search paths")
}

func TestESLint(t *testing.T) {
	var b strings.Builder
	b.WriteString("\n/repo/src/App.tsx\n  12:5  error  'user' is not defined  no-undef\n")
	b.WriteString(repeatf(40, "  %d:1  warning  Unexpected console statement  no-console\n"))
	b.WriteString("\n/repo/src/clean.ts\n" + repeatf(10, "  %d:3  warning  'x' is defined but never used  no-unused-vars\n"))
	b.WriteString("\n✖ 51 problems (1 error, 50 warnings)\n")
	s := Filter(Detect("npx eslint src"), []byte(b.String()), 1)
	mustContain(t, s, "/repo/src/App.tsx", "'user' is not defined", "50 warnings: no-console ×40, no-unused-vars ×10", "✖ 51 problems")
	mustNotContain(t, s, "/repo/src/clean.ts", "Unexpected console")
}

func TestFind(t *testing.T) {
	out := repeatf(200, "./src/components/C%d.tsx\n") + repeatf(50, "./test/unit/t%d.ts\n") + "find: './secret': Permission denied\n"
	s := Filter(Detect("find . -name '*.ts*'"), []byte(out), 1)
	mustContain(t, s, "250 paths", "./src/components/C0.tsx", "… 225 more", "by folder:", "src/components 200", "test/unit 50", "1 permission denied lines dropped")
	mustNotContain(t, s, "C199.tsx")
}

func TestGrep(t *testing.T) {
	var b strings.Builder
	for f := range 40 {
		for l := range 5 {
			fmt.Fprintf(&b, "internal/pkg%d/file.go:%d:\treturn fmt.Errorf(\"wrap: %%w\", err)\n", f, l*10+1)
		}
	}
	b.WriteString("Binary file assets/logo.png matches\n")
	s := Filter(Detect("rg -n Errorf"), []byte(b.String()), 0)
	mustContain(t, s, "200 matches in 40 files", "internal/pkg0/file.go (5)", "  1: return fmt.Errorf", "… 10 more files", "1 binary files matched")
	mustNotContain(t, s, "pkg35/")
	// Matches of a single file come without file names: the head is kept.
	s = Filter(ToolGrep, []byte(repeatf(100, "a line %d with the word\n")), 0)
	mustContain(t, s, "a line 0 with", "more lines omitted")
}

func TestListing(t *testing.T) {
	s := Filter(Detect("ls -la node_modules"), []byte("total 4000\n"+repeatf(900, "drwxr-xr-x  3 u u 4096 Nov 24 10:00 pkg%d\n")), 0)
	mustContain(t, s, "list: 901 lines shortened", "total 4000", "pkg38", "… 861 more lines omitted")
	mustNotContain(t, s, "pkg500")
	s = Filter(Detect("kubectl get pods -A"), []byte("NAMESPACE NAME READY\n"+repeatf(100, "default web-%d 1/1\n")), 0)
	mustContain(t, s, "NAMESPACE NAME READY", "web-0", "more lines omitted")
	if Detect("docker ps -a") != ToolList || Detect("docker compose ps") != ToolList || Detect("tree -L 3") != ToolList {
		t.Fatal("listings")
	}
}

func TestDockerBuild(t *testing.T) {
	out := repeatf(150, "#%d [internal] load metadata\n") + `#12 [build 4/6] RUN npm ci
#12 3.210 npm ERR! code ERESOLVE
#12 3.211 npm ERR! ERESOLVE unable to resolve dependency tree
#12 ERROR: process "/bin/sh -c npm ci" did not complete successfully: exit code: 1
------
 > [build 4/6] RUN npm ci:
------
ERROR: failed to solve: process "/bin/sh -c npm ci" did not complete successfully: exit code: 1
`
	s := Filter(Detect("docker build -t shop ."), []byte(out), 1)
	mustContain(t, s, "npm ERR! code ERESOLVE", "#12 ERROR: process", "failed to solve", "build step lines")
	mustNotContain(t, s, "#40 [internal]")
}

func TestLogsAndKubectl(t *testing.T) {
	out := repeatf(300, "2026-11-24T10:00:%02d INFO request served\n") + strings.Repeat("2026-11-24T10:05:00 ERROR db: connection refused\n", 30) + "2026-11-24T10:06:00 INFO retry ok\n"
	s := Filter(Detect("docker logs web"), []byte(out), 0)
	mustContain(t, s, "ERROR db: connection refused", "last lines:", "retry ok")
	if strings.Count(s, "connection refused") > 3 {
		t.Fatalf("repeats kept:\n%s", s)
	}
	desc := `Name:         web-7c9
Namespace:    default
Node:         pool-a/10.0.0.4
Labels:       app=web
Annotations:  checksum=abc
` + repeatf(60, "  volume-%d:\n    Type: Secret\n") + `Containers:
  web:
    Image:          shop/web:1.4
    State:          Waiting
      Reason:       CrashLoopBackOff
    Last State:     Terminated
      Exit Code:    137
    Ready:          False
    Restart Count:  7
Events:
  Type     Reason   Age   From     Message
  Warning  BackOff  2m    kubelet  Back-off restarting failed container
`
	s = Filter(Detect("kubectl describe pod web-7c9"), []byte(desc), 0)
	mustContain(t, s, "Name:         web-7c9", "CrashLoopBackOff", "Exit Code:    137", "Restart Count:  7", "Events:", "Back-off restarting")
	mustNotContain(t, s, "volume-30", "checksum=abc")
	apply := repeatf(60, "configmap/c%d unchanged\n") + "deployment.apps/web configured\n"
	s = Filter(Detect("kubectl apply -f k8s/"), []byte(apply), 0)
	mustContain(t, s, "deployment.apps/web configured", "60 unchanged")
	mustNotContain(t, s, "c5 unchanged")
}

func TestInstall(t *testing.T) {
	out := repeatf(40, "npm warn deprecated pkg%d@1.0.0: no longer supported\n") +
		"\nadded 1203 packages, and audited 1204 packages in 31s\n\n180 packages are looking for funding\n  run `npm fund` for details\n\n2 moderate severity vulnerabilities\n"
	s := Filter(Detect("npm ci"), []byte(out), 0)
	mustContain(t, s, "install: passed", "added 1203 packages", "2 moderate severity vulnerabilities", "40 deprecation warnings")
	mustNotContain(t, s, "pkg3@")
	fail := repeatf(50, "Collecting dep%d\n") + "ERROR: Could not find a version that satisfies the requirement nope==9.9\nERROR: No matching distribution found for nope==9.9\n"
	s = Filter(Detect("pip install -r requirements.txt"), []byte(fail), 1)
	mustContain(t, s, "No matching distribution found for nope==9.9", "50 progress lines")
	for cmd, want := range map[string]string{"pnpm install --frozen-lockfile": ToolInstall, "yarn": ToolInstall, "uv sync": ToolInstall, "bundle install": ToolInstall, "python -m pip install x": ToolInstall} {
		if got := Detect(cmd); got != want {
			t.Errorf("Detect(%q) = %q", cmd, got)
		}
	}
}

func TestDetectNewTools(t *testing.T) {
	cases := map[string]string{
		"git -C repo status --short":       ToolGitStatus,
		"git --no-pager diff main...HEAD":  ToolGitDiff,
		"git log --oneline -20":            ToolGitLog,
		"git fetch origin":                 ToolGitSync,
		"git grep -n TODO":                 ToolGrep,
		"git commit -m x":                  ToolGeneric,
		"go vet ./...":                     ToolGo,
		"golangci-lint run":                ToolLint,
		"mvn -q package":                   ToolMaven,
		"gmake test":                       ToolMake,
		"pnpm exec tsc -p .":               ToolTSC,
		"npm run eslint":                   ToolESLint,
		"fd -e go":                         ToolFind,
		"grep -rn jest src":                ToolGrep,
		"docker buildx build .":            ToolDockerBuild,
		"docker compose up":                ToolLogs,
		"podman image ls":                  ToolList,
		"kubectl -n prod logs web":         ToolLogs,
		"kubectl rollout status deploy/w":  ToolKubectl,
		"kubectl version":                  ToolGeneric,
		"docker run --rm alpine true":      ToolGeneric,
		"CGO_ENABLED=0 go build ./cmd/...": ToolGo,
	}
	for cmd, want := range cases {
		if got := Detect(cmd); got != want {
			t.Errorf("Detect(%q) = %q, want %q", cmd, got, want)
		}
	}
	if _, lead := DetectLeading("echo go test"); lead {
		t.Error("echo is the command")
	}
	if tool, lead := DetectLeading("CGO_ENABLED=0 go test ./..."); !lead || tool != ToolGo {
		t.Error("assignments come before the command")
	}
	if _, lead := DetectLeading("curl x"); lead {
		t.Error("generic is never leading")
	}
}

func TestCollapseAndDedupe(t *testing.T) {
	got := collapseRuns([]string{"a", "b", "b", "b", "b", "", "", "", "c", "c"})
	want := []string{"a", "b (×4)", "", "", "", "c", "c"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("%q", got)
	}
	if d := dedupeAll([]string{"x", "y", "x"}); strings.Join(d, "|") != "x (×2)|y" {
		t.Fatalf("%q", d)
	}
	// A generic failure with a repeated line is collapsed in the tail.
	s := Filter("generic", []byte(strings.Repeat("retrying connection\n", 500)+"gave up\n"), 1)
	mustContain(t, s, "retrying connection (×", "gave up")
	if n := lineCount(s); n > 5 {
		t.Fatalf("%d lines:\n%s", n, s)
	}
}
