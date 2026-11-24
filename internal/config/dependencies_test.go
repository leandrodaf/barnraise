package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func TestDependenciesKey(t *testing.T) {
	root := t.TempDir()
	cfg, err := Compile(root, []byte("checks:\n  main: make test\n"))
	if err != nil {
		t.Fatal(err)
	}
	d, ok := cfg.Checks[domain.DependencyCheck]
	if !ok || d.RunsOn != domain.RunsOnDependencies || cfg.Dependencies.Off {
		t.Fatalf("an existing project gets the check by default: %+v", cfg.Checks)
	}
	if errs := cfg.Validate(); len(errs) > 0 {
		t.Fatal(errs)
	}
	st, _ := cfg.Flows["feature"].Step("check")
	if got := cfg.AutoChecks(st, []string{"main"}); !reflect.DeepEqual(got, []string{"main", domain.DependencyCheck}) {
		t.Fatalf("the check step of the blank flow runs it: %v (%+v)", got, st)
	}

	for _, off := range []string{"off", "false", "no"} {
		cfg, err := Compile(root, []byte("dependencies: "+off+"\n"))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := cfg.Checks[domain.DependencyCheck]; ok || !cfg.Dependencies.Off {
			t.Fatalf("dependencies: %s keeps the check", off)
		}
	}

	cfg, err = Compile(root, []byte("dependencies:\n  fail_at: Critical\n  unknown: medium\n  ignore: [GHSA-aaaa-bbbb-cccc]\n  scanner: api\n  api: https://osv.internal/\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := domain.DependencyPolicy{FailAt: "critical", Unknown: "medium", Ignore: []string{"GHSA-aaaa-bbbb-cccc"}, Scanner: "api", API: "https://osv.internal"}
	if !reflect.DeepEqual(cfg.Dependencies, want) {
		t.Fatalf("policy %+v", cfg.Dependencies)
	}
	cfg, _ = Compile(root, []byte("dependencies: {check: false}\n"))
	if !cfg.Dependencies.Off {
		t.Fatal("check: false")
	}
	if _, err := Compile(root, []byte("dependencies: sometimes\n")); err == nil || !strings.Contains(err.Error(), "use off, or a mapping") {
		t.Fatalf("bad scalar: %v", err)
	}
	cfg, _ = Compile(root, []byte("dependencies: {fail_at: severe}\n"))
	if errs := cfg.Validate(); len(errs) != 1 || !strings.Contains(errs[0].Error(), "dependencies.fail_at") {
		t.Fatalf("validate: %v", errs)
	}
}

func TestASTGrepCheckProblems(t *testing.T) {
	old := lookPath
	defer func() { lookPath = old }()
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	root := t.TempDir()
	write(t, root, FileName, "checks:\n  main: make test\n  structure: {runs_on: \"builtin:ast-grep\"}\n  rust-rules: {runs_on: \"builtin:ast-grep\", run: \"-c rules/sg.yml\"}\n")
	problems, err := Check(root)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(problems, "\n")
	if !strings.Contains(all, "check structure runs ast-grep, which is not on the PATH") || !strings.Contains(all, "check structure runs ast-grep, but . has no sgconfig.yml") ||
		strings.Contains(all, "rust-rules runs ast-grep, but") {
		t.Fatalf("%s", all)
	}
	lookPath = func(string) (string, error) { return "/usr/bin/ast-grep", nil }
	if err := os.WriteFile(filepath.Join(root, "sgconfig.yml"), []byte("ruleDirs: [rules]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	problems, _ = Check(root)
	if strings.Contains(strings.Join(problems, "\n"), "ast-grep") {
		t.Fatalf("%v", problems)
	}
	if !namesConfig("--config=x.yml") || namesConfig("--color never") {
		t.Fatal("namesConfig")
	}
}
