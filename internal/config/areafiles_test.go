package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func write(t *testing.T, root, name, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A repository with a Go backend, a React web app and a React Native app:
// each part describes itself in its own file and Barnraise puts them together.
func TestAreaFilesAreFoundAndMerged(t *testing.T) {
	root := t.TempDir()
	rootYAML := "project: shop\ntemplate: blank\n"
	write(t, root, "barnraise.yaml", rootYAML)
	write(t, root, "backend/barnraise.yaml", "about: REST API\nstack: go\nstandard: STANDARDS.md\nchecks:\n  test: go test ./...\n  lint: { run: golangci-lint run, timeout: 10m, flaky_retries: 1 }\n")
	write(t, root, "web/barnraise.yaml", "stack: react\nalso_when: [backend/api/openapi.yaml]\nchecks:\n  test: npm test\n")
	write(t, root, "apps/mobile/barnraise.yaml", "area: mobile\nstack: react-native\nchecks:\n  e2e: { run: maestro test .maestro, runs_on: \"machine:emulators\" }\n")
	write(t, root, "web/node_modules/pkg/barnraise.yaml", "checks: { x: nope }\n") // dependencies are never searched
	write(t, root, ".hidden/barnraise.yaml", "checks: { x: nope }\n")

	if got := AreaFiles(root); strings.Join(got, " ") != "apps/mobile backend web" {
		t.Fatalf("area files %v", got)
	}
	cfg, err := Compile(root, []byte(rootYAML))
	if err != nil {
		t.Fatal(err)
	}
	if errs := cfg.Validate(); len(errs) != 0 {
		t.Fatal(errs)
	}
	b := cfg.Areas["backend"]
	if b.Dir != "backend" || b.Stack != "go" || b.About != "REST API" || b.Standard != "backend/STANDARDS.md" || len(b.Paths) != 1 || b.Paths[0] != "backend/**" {
		t.Fatalf("backend %+v", b)
	}
	if m := cfg.Areas["mobile"]; m.Dir != "apps/mobile" || m.Paths[0] != "apps/mobile/**" {
		t.Fatalf("mobile %+v", m)
	}
	var names []string
	for n := range cfg.Checks {
		if n != domain.DependencyCheck { // the built-in one is always there
			names = append(names, n)
		}
	}
	if len(names) != 4 || cfg.Checks["main"].Name != "" {
		t.Fatalf("the parts' checks replace the template's guess: %v", names)
	}
	lint := cfg.Checks["backend-lint"]
	if lint.Dir != "backend" || lint.Areas[0] != "backend" || lint.FlakyRetries != 1 || lint.Timeout.Minutes() != 10 {
		t.Fatalf("lint %+v", lint)
	}
	if e2e := cfg.Checks["mobile-e2e"]; e2e.RunsOn != "machine:emulators" || e2e.Dir != "apps/mobile" {
		t.Fatalf("e2e %+v", e2e)
	}
	// Which checks run for a change.
	runs := func(files ...string) string {
		var out []string
		for _, n := range []string{"backend-lint", "backend-test", "mobile-e2e", "web-test"} {
			if ok, _ := cfg.Checks[n].Applies(files, nil, cfg.AreaOfFile); ok {
				out = append(out, n)
			}
		}
		return strings.Join(out, " ")
	}
	for files, want := range map[string]string{
		"backend/internal/user.go":              "backend-lint backend-test",
		"web/src/App.tsx":                       "web-test",
		"apps/mobile/src/Home.tsx":              "mobile-e2e",
		"backend/api/openapi.yaml":              "backend-lint backend-test web-test", // the contract concerns the web app too
		"web/src/api.ts apps/mobile/src/api.ts": "mobile-e2e web-test",
		"README.md":                             "",
	} {
		if got := runs(strings.Fields(files)...); got != want {
			t.Errorf("%s: runs %q, want %q", files, got, want)
		}
	}
	// The flows now run every check that applies, not the single `main`.
	f, _ := cfg.FlowFor("feature")
	st, _ := f.Step("check")
	if st.Type != domain.StepCheck || domain.TrimChecks(st.Run) != "checks" {
		t.Fatalf("check step %+v", st)
	}
}

func TestAreaFilesKeepTheRootInCharge(t *testing.T) {
	// A root that declares its own main check keeps it, and its flows.
	root := t.TempDir()
	y := "project: shop\ntemplate: blank\nchecks:\n  main: make verify\n"
	write(t, root, "barnraise.yaml", y)
	write(t, root, "backend/barnraise.yaml", "checks:\n  test: go test ./...\n")
	cfg, err := Compile(root, []byte(y))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Checks["main"].Run != "make verify" || cfg.Checks["backend-test"].Run == "" {
		t.Fatalf("checks %v", cfg.Checks)
	}
	f, _ := cfg.FlowFor("feature")
	if st, _ := f.Step("check"); domain.TrimChecks(st.Run) != "main" {
		t.Fatalf("the root's flow is untouched: %+v", st)
	}

	for name, tc := range map[string]struct{ rootYAML, part, want string }{
		"flows belong in the root":    {"project: shop\ntemplate: blank\n", "flows: { x: { steps: {} } }\n", "belong in the barnraise.yaml at the root"},
		"project belongs in the root": {"project: shop\ntemplate: blank\n", "project: other\n", "belong in the barnraise.yaml at the root"},
		"area declared twice":         {"project: shop\ntemplate: blank\nareas:\n  backend: { paths: [\"backend/**\"] }\n", "stack: go\n", "declared in barnraise.yaml and in backend/barnraise.yaml"},
		"bad yaml":                    {"project: shop\ntemplate: blank\n", "checks: [oops\n", "backend/barnraise.yaml"},
		"bad timeout":                 {"project: shop\ntemplate: blank\n", "checks:\n  t: { run: x, timeout: soon }\n", "checks.t.timeout"},
	} {
		root := t.TempDir()
		write(t, root, "barnraise.yaml", tc.rootYAML)
		write(t, root, "backend/barnraise.yaml", tc.part)
		if _, err := Compile(root, []byte(tc.rootYAML)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
