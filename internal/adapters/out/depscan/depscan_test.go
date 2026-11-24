package depscan

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/execx"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

func pk(eco, name, v string) Package { return Package{eco, name, v} }

func TestParsers(t *testing.T) {
	cases := map[string][]Package{
		"go.mod":              {pk(ecoGo, "github.com/spf13/cobra", "1.8.0"), pk(ecoGo, "golang.org/x/net", "0.7.0"), pk(ecoGo, "golang.org/x/text", "0.3.5"), pk(ecoGo, "stdlib", "1.22.5")},
		"package-lock.json":   {pk(ecoNPM, "@babel/core", "7.24.0"), pk(ecoNPM, "lodash", "4.17.20"), pk(ecoNPM, "semver", "6.3.1")},
		"npm-shrinkwrap.json": {pk(ecoNPM, "express", "4.17.1"), pk(ecoNPM, "qs", "6.7.0")},
		"pnpm-lock.yaml":      {pk(ecoNPM, "@types/node", "20.11.0"), pk(ecoNPM, "lodash", "4.17.20"), pk(ecoNPM, "minimist", "1.2.5"), pk(ecoNPM, "react", "18.2.0")},
		"yarn.lock":           {pk(ecoNPM, "@babel/code-frame", "7.23.5"), pk(ecoNPM, "lodash", "4.17.20")},
		"berry/yarn.lock":     {pk(ecoNPM, "axios", "1.6.2")},
		"requirements.txt":    {pk(ecoPyPI, "django", "4.2.1"), pk(ecoPyPI, "pillow", "10.0.0"), pk(ecoPyPI, "requests", "2.31.0")},
		"Pipfile.lock":        {pk(ecoPyPI, "pytest", "7.4.0"), pk(ecoPyPI, "urllib3", "1.26.5")},
		"poetry.lock":         {pk(ecoPyPI, "jinja2", "3.1.2"), pk(ecoPyPI, "markupsafe", "2.1.3")},
		"uv.lock":             {pk(ecoPyPI, "idna", "3.6")},
		"Cargo.lock":          {pk(ecoCargo, "time", "0.1.43")},
		"Gemfile.lock":        {pk(ecoGems, "nokogiri", "1.15.4"), pk(ecoGems, "racc", "1.7.1"), pk(ecoGems, "rails", "7.0.8")},
		"gradle.lockfile":     {pk(ecoMaven, "com.fasterxml.jackson.core:jackson-databind", "2.13.0"), pk(ecoMaven, "org.yaml:snakeyaml", "1.33")},
		"pom.xml":             {pk(ecoMaven, "junit:junit", "4.13.1"), pk(ecoMaven, "org.apache.logging.log4j:log4j-core", "2.14.1")},
	}
	for file, want := range cases {
		data, err := os.ReadFile(filepath.Join("testdata", "lockfiles", filepath.FromSlash(file)))
		if err != nil {
			t.Fatal(err)
		}
		p := parserFor(file)
		if p == nil {
			t.Fatalf("%s: no parser", file)
		}
		if got := p(data); !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %v\nwant %v", file, got, want)
		}
	}
	if parserFor("package.json") != nil || parserFor("src/main.go") != nil {
		t.Fatal("package.json pins nothing; a source file is no lockfile")
	}
	if got := parsePackageLock([]byte("not json")); got != nil {
		t.Fatalf("broken lockfile = %v", got)
	}
}

func TestCVSS3Score(t *testing.T) {
	for vec, want := range map[string]float64{
		"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H": 9.8,
		"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:H": 7.5,
		"CVSS:3.1/AV:N/AC:L/PR:N/UI:R/S:C/C:L/I:L/A:N": 6.1,
		"CVSS:3.0/AV:N/AC:L/PR:L/UI:N/S:C/C:H/I:H/A:H": 9.9,
		"CVSS:3.1/AV:L/AC:H/PR:H/UI:R/S:U/C:N/I:N/A:N": 0,
		"CVSS:3.1/AV:P/AC:H/PR:H/UI:R/S:U/C:L/I:N/A:N": 1.6,
	} {
		got, ok := CVSS3Score(vec)
		if !ok || got != want {
			t.Errorf("%s = %v, %v; want %v", vec, got, ok, want)
		}
	}
	for _, bad := range []string{"", "CVSS:2.0/AV:N", "CVSS:3.1/AV:X/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H", "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:Q/C:H/I:H/A:H", "AV:N/AC:L"} {
		if _, ok := CVSS3Score(bad); ok {
			t.Errorf("%q should not score", bad)
		}
	}
}

// fakeOSV serves the recorded records in testdata/osv.
func fakeOSV(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	raw, err := os.ReadFile("testdata/osv/affected.json")
	if err != nil {
		t.Fatal(err)
	}
	var affected map[string][]string
	if err := json.Unmarshal(raw, &affected); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/querybatch":
			var body struct {
				Queries []osvQuery `json:"queries"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			var res struct {
				Results []map[string]any `json:"results"`
			}
			for _, q := range body.Queries {
				var vulns []map[string]string
				for _, id := range affected[q.Package.Ecosystem+" "+q.Package.Name+" "+q.Version] {
					vulns = append(vulns, map[string]string{"id": id, "modified": "2024-01-01T00:00:00Z"})
				}
				m := map[string]any{}
				if len(vulns) > 0 {
					m["vulns"] = vulns
				}
				res.Results = append(res.Results, m)
			}
			_ = json.NewEncoder(w).Encode(res)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/vulns/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/vulns/")
			data, err := os.ReadFile(filepath.Join("testdata", "osv", "vulns", filepath.Base(id)+".json"))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const baseGoMod = "module example.com/shop\n\ngo 1.22.1\n\nrequire golang.org/x/text v0.3.0\n"
const headGoMod = "module example.com/shop\n\ngo 1.22.1\n\nrequire (\n\tgolang.org/x/net v0.7.0\n\tgolang.org/x/text v0.3.5\n\tgithub.com/spf13/cobra v1.8.0\n)\n"
const headLock = `{"lockfileVersion": 3, "packages": {"": {}, "node_modules/lodash": {"version": "4.17.20"}, "node_modules/left-pad": {"version": "1.3.0"}}}`

// repo makes a repository whose main has the base go.mod and whose
// branch adds dependencies; it returns the worktree and the base commit.
func repo(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	writeFile(t, dir, "go.mod", baseGoMod)
	writeFile(t, dir, "README.md", "shop\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "base")
	base := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "checkout", "-qb", "item")
	writeFile(t, dir, "go.mod", headGoMod)
	writeFile(t, dir, "web/package.json", `{"dependencies": {"lodash": "^4.17.15"}}`)
	writeFile(t, dir, "web/package-lock.json", headLock)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "item")
	return dir, base
}

func noScanner(string) (string, error) { return "", exec.ErrNotFound }

func spec(dir, base string, touched ...string) ports.CheckSpec {
	return ports.CheckSpec{Def: domain.CheckDef{Name: "dependencies", RunsOn: domain.RunsOnDependencies, Timeout: time.Minute}, Dir: dir, Base: base, Touched: touched}
}

func TestNativeScanReportsOnlyWhatTheChangeBringsIn(t *testing.T) {
	dir, base := repo(t)
	srv, _ := fakeOSV(t)
	s := New(Options{Exec: execx.New(), LookPath: noScanner})
	pol := domain.DependencyPolicy{API: srv.URL}
	res, err := s.Check(context.Background(), spec(dir, base, "go.mod", "web/package.json", "README.md"), pol)
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed || res.MainRed {
		t.Fatalf("an added high vulnerability must fail the check: %+v", res)
	}
	sum := res.Summary
	for _, want := range []string{
		"1 known vulnerability at or above high",
		"go.mod: golang.org/x/net 0.7.0: GHSA-net1-xxxx-yyyy, GO-2023-0001, CVE-2023-1111 (high 7.5)",
		"Fixed in 0.17.0",
		"Below high, not failing:",
		"web/package-lock.json: lodash 4.17.20: GHSA-lodash-0000-0000, CVE-2020-28500 (medium)",
		"dependencies.ignore",
	} {
		if !strings.Contains(sum, want) {
			t.Errorf("summary misses %q:\n%s", want, sum)
		}
	}
	if strings.Contains(sum, "x/text") {
		t.Errorf("x/text was already vulnerable on main: not this change's finding\n%s", sum)
	}

	// The owner accepts the advisory (by its alias): the check passes and says so.
	pol.Ignore = []string{"CVE-2023-1111"}
	res, _ = s.Check(context.Background(), spec(dir, base, "go.mod", "web/package-lock.json"), pol)
	if !res.Passed || !strings.Contains(res.Summary, "Accepted in dependencies.ignore: golang.org/x/net GHSA-net1-xxxx-yyyy") {
		t.Fatalf("ignored advisory: %+v", res)
	}

	// A stricter project fails on the medium one too; a laxer one passes.
	res, _ = s.Check(context.Background(), spec(dir, base, "web/package-lock.json"), domain.DependencyPolicy{API: srv.URL, FailAt: "medium"})
	if res.Passed || !strings.Contains(res.Summary, "at or above medium") {
		t.Fatalf("fail_at medium: %+v", res)
	}
	res, _ = s.Check(context.Background(), spec(dir, base, "go.mod"), domain.DependencyPolicy{API: srv.URL, FailAt: "critical"})
	if !res.Passed {
		t.Fatalf("fail_at critical: %+v", res)
	}
}

func TestNativeScanFindsTheChangeByItself(t *testing.T) {
	dir, base := repo(t)
	srv, _ := fakeOSV(t)
	// No touched files and no base: the scanner finds main and the diff.
	s := New(Options{Exec: execx.New(), LookPath: noScanner, MainRefs: []string{"refs/remotes/origin/main", "refs/heads/main"}})
	rep, err := s.Scan(context.Background(), spec(dir, ""), domain.DependencyPolicy{API: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.BaseKnown || !reflect.DeepEqual(rep.Files, []string{"go.mod", "web/package-lock.json"}) || len(rep.Failing) != 1 || rep.Checked != 5 {
		t.Fatalf("report %+v (base %s)", rep, base)
	}
}

func TestNothingToScan(t *testing.T) {
	dir, base := repo(t)
	srv, calls := fakeOSV(t)
	s := New(Options{Exec: execx.New(), LookPath: noScanner})
	res, err := s.Check(context.Background(), spec(dir, base, "README.md", "src/app.go"), domain.DependencyPolicy{API: srv.URL})
	if err != nil || !res.Passed || res.Summary != "no dependency lockfile changed" || calls.Load() != 0 {
		t.Fatalf("no manifest: %+v %v (calls %d)", res, err, calls.Load())
	}
	// A lockfile that changes nothing pinned asks nothing.
	writeFile(t, dir, "go.sum", "")
	res, _ = s.Check(context.Background(), spec(dir, "HEAD", "go.sum"), domain.DependencyPolicy{API: srv.URL})
	if !res.Passed || !strings.Contains(res.Summary, "no dependency was added or updated in go.mod") || calls.Load() != 0 {
		t.Fatalf("unchanged go.mod: %+v (calls %d)", res, calls.Load())
	}
}

func TestOSVUnreachableHoldsForTheOwner(t *testing.T) {
	dir, base := repo(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down for maintenance", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	s := New(Options{Exec: execx.New(), LookPath: noScanner})
	res, err := s.Check(context.Background(), spec(dir, base, "go.mod"), domain.DependencyPolicy{API: srv.URL})
	if err != nil || res.Passed || !res.MainRed || !strings.Contains(res.Summary, "answered 503") || !strings.Contains(res.Summary, "not the change's fault") {
		t.Fatalf("unreachable OSV: %+v %v", res, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Check(ctx, spec(dir, base, "go.mod"), domain.DependencyPolicy{API: srv.URL}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled = %v", err)
	}
}

// fakeScanner writes an osv-scanner stand-in that prints the recorded
// output for the branch's go.mod (it has x/net) or the base's.
func fakeScanner(t *testing.T, exit int) (string, string) {
	t.Helper()
	dir := t.TempDir()
	head, _ := filepath.Abs("testdata/osv/scanner-head.json")
	base, _ := filepath.Abs("testdata/osv/scanner-base.json")
	argsLog := filepath.Join(dir, "args")
	script := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in --lockfile=*) f=\"${a#--lockfile=}\";; esac; done\n" +
		"echo \"$@\" >> '" + argsLog + "'\n" +
		"if [ " + strconv.Itoa(exit) + " -gt 1 ]; then echo 'osv-scanner: boom' >&2; exit " + strconv.Itoa(exit) + "; fi\n" +
		"if grep -q 'golang.org/x/net' \"$f\"; then cat '" + head + "'; else cat '" + base + "'; fi\nexit " + strconv.Itoa(exit) + "\n"
	bin := filepath.Join(dir, "osv-scanner")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsLog
}

func TestOSVScannerWhenInstalled(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir, base := repo(t)
	bin, argsLog := fakeScanner(t, 1)
	s := New(Options{Exec: execx.New(), LookPath: func(string) (string, error) { return bin, nil }})
	res, err := s.Check(context.Background(), spec(dir, base, "go.mod"), domain.DependencyPolicy{API: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed || !strings.Contains(res.Summary, "(osv-scanner)") || !strings.Contains(res.Summary, "golang.org/x/net 0.7.0: GHSA-net1-xxxx-yyyy") ||
		!strings.Contains(res.Summary, "(high 7.5)") || strings.Contains(res.Summary, "x/text") {
		t.Fatalf("osv-scanner result: %+v", res)
	}
	args, _ := os.ReadFile(argsLog)
	if lines := strings.Split(strings.TrimSpace(string(args)), "\n"); len(lines) != 2 || !strings.Contains(lines[0], "--format=json --lockfile="+filepath.Join(dir, "go.mod")) {
		t.Fatalf("osv-scanner runs on the branch's go.mod and the base's copy: %q", args)
	}

	// A scanner that breaks is not the change's fault.
	bad, _ := fakeScanner(t, 127)
	s = New(Options{Exec: execx.New(), LookPath: func(string) (string, error) { return bad, nil }})
	res, _ = s.Check(context.Background(), spec(dir, base, "go.mod"), domain.DependencyPolicy{})
	if !res.MainRed || !strings.Contains(res.Summary, "exited with 127: osv-scanner: boom") {
		t.Fatalf("broken scanner: %+v", res)
	}

	// Required but missing.
	s = New(Options{Exec: execx.New(), LookPath: noScanner})
	res, _ = s.Check(context.Background(), spec(dir, base, "go.mod"), domain.DependencyPolicy{Scanner: "osv-scanner"})
	if !res.MainRed || !strings.Contains(res.Summary, "osv-scanner is not installed") {
		t.Fatalf("missing scanner: %+v", res)
	}
}

func TestTargetsFollowManifestsToTheirLockfiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	writeFile(t, dir, "packages/ui/package.json", "{}")
	writeFile(t, dir, "rust/Cargo.lock", "")
	writeFile(t, dir, "rust/Cargo.toml", "")
	s := New(Options{Exec: execx.New()})
	got := s.targets(context.Background(), dir, "", []string{"packages/ui/package.json", "rust/Cargo.toml", "deleted/go.mod", "Gemfile", "docs/a.md"})
	if !reflect.DeepEqual(got, []string{"pnpm-lock.yaml", "rust/Cargo.lock"}) {
		t.Fatalf("targets = %v", got)
	}
}

func TestReportText(t *testing.T) {
	if got := (Report{}).Text(domain.DependencyPolicy{}); got != "no dependency lockfile changed" {
		t.Fatal(got)
	}
	r := Report{Files: []string{"go.mod"}, Scanner: "the OSV API", Checked: 3}
	if got := r.Text(domain.DependencyPolicy{}); got != "no new known vulnerability in the dependencies of go.mod (the OSV API)" {
		t.Fatal(got)
	}
	f := Finding{File: "go.mod", Package: pk(ecoGo, "x", "1"), IDs: []string{"GO-1"}}
	r.New = []Finding{f}
	if got := r.Text(domain.DependencyPolicy{FailAt: "critical"}); !strings.Contains(got, "no new known vulnerability at or above critical") || !strings.Contains(got, "(unrated)") {
		t.Fatal(got)
	}
	r.Failing, r.New = []Finding{f, f}, []Finding{f, f}
	if got := r.Text(domain.DependencyPolicy{}); !strings.HasPrefix(got, "2 known vulnerabilities at or above high") {
		t.Fatal(got)
	}
}
