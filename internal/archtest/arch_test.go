// Package archtest enforces the hexagonal dependency rule and the glossary
// on the whole repository. It only holds tests.
package archtest

import (
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

const module = "github.com/leandrodaf/barnraise"

// archPkg is the part of `go list -json` the rules need.
type archPkg struct {
	ImportPath string
	Standard   bool
	Imports    []string
	Deps       []string
}

var (
	archOnce sync.Once
	archPkgs map[string]*archPkg
	archErr  error
)

// archList loads every package of the module and its dependencies once.
func archList(t *testing.T) map[string]*archPkg {
	t.Helper()
	archOnce.Do(func() {
		cmd := exec.Command("go", "list", "-deps", "-json", module+"/...")
		cmd.Dir = vocabRoot(t)
		out, err := cmd.Output()
		if err != nil {
			if ee, ok := errors.AsType[*exec.ExitError](err); ok {
				err = errors.New(err.Error() + ": " + string(ee.Stderr))
			}
			archErr = err
			return
		}
		archPkgs = map[string]*archPkg{}
		dec := json.NewDecoder(strings.NewReader(string(out)))
		for {
			var p archPkg
			if err := dec.Decode(&p); err == io.EOF {
				break
			} else if err != nil {
				archErr = err
				return
			}
			archPkgs[p.ImportPath] = &p
		}
	})
	if archErr != nil {
		t.Fatalf("go list: %v", archErr)
	}
	return archPkgs
}

// archUnder reports whether pkg is dir or a package below it.
func archUnder(pkg, dir string) bool {
	dir = module + "/" + dir
	return pkg == dir || strings.HasPrefix(pkg, dir+"/")
}

// archOwn returns the module packages below dir.
func archOwn(pkgs map[string]*archPkg, dir string) []*archPkg {
	var out []*archPkg
	for path, p := range pkgs {
		if archUnder(path, dir) {
			out = append(out, p)
		}
	}
	return out
}

// archCheckDeps fails when a package below dir depends, directly or not, on
// a non-standard package outside the allowed module directories.
func archCheckDeps(t *testing.T, dir string, allowed ...string) {
	t.Helper()
	pkgs := archList(t)
	own := archOwn(pkgs, dir)
	if len(own) == 0 {
		t.Fatalf("no packages under %s", dir)
	}
	for _, p := range own {
		for _, d := range p.Deps {
			dp := pkgs[d]
			if dp == nil || dp.Standard {
				continue
			}
			ok := archUnder(d, dir)
			for _, a := range allowed {
				ok = ok || archUnder(d, a)
			}
			if !ok {
				t.Errorf("%s depends on %s; %s may use only the standard library%s", p.ImportPath, d, dir, archAllowedText(allowed))
			}
		}
	}
}

func archAllowedText(allowed []string) string {
	if len(allowed) == 0 {
		return ""
	}
	return " and " + strings.Join(allowed, ", ")
}

// archForbidImports fails when a package below dir imports one of the
// standard packages that belong to adapters (network, databases, processes).
func archForbidImports(t *testing.T, dir string, forbidden ...string) {
	t.Helper()
	for _, p := range archOwn(archList(t), dir) {
		for _, imp := range p.Imports {
			for _, f := range forbidden {
				if imp == f || strings.HasPrefix(imp, f+"/") {
					t.Errorf("%s imports %s, which belongs in an adapter", p.ImportPath, imp)
				}
			}
		}
	}
}

func TestArchDomainIsPure(t *testing.T) {
	archCheckDeps(t, "internal/domain")
	archForbidImports(t, "internal/domain", "net", "os/exec", "database/sql", "os")
}

func TestArchPortsDependOnDomainOnly(t *testing.T) {
	archCheckDeps(t, "internal/ports", "internal/domain")
	archForbidImports(t, "internal/ports", "net", "os/exec", "database/sql")
}

func TestArchAppDependsOnDomainAndPorts(t *testing.T) {
	// No adapters, no NATS, no sqlite, no yaml: all of those are non-standard
	// packages and the dependency check refuses them.
	archCheckDeps(t, "internal/app", "internal/domain", "internal/ports")
	// HTTP clients and databases enter through ports.
	archForbidImports(t, "internal/app", "net/http", "net/rpc", "net/smtp", "database/sql")
}

func TestArchAdaptersDirection(t *testing.T) {
	pkgs := archList(t)
	for _, p := range archOwn(pkgs, "internal/adapters") {
		for _, d := range p.Deps {
			if archUnder(d, "cmd/barnraise") {
				t.Errorf("%s depends on %s: adapters never import the binary", p.ImportPath, d)
			}
			if archUnder(p.ImportPath, "internal/adapters/out") && archUnder(d, "internal/adapters/in") {
				t.Errorf("%s depends on %s: outbound adapters never import inbound adapters", p.ImportPath, d)
			}
		}
	}
	for _, p := range archOwn(pkgs, "internal/adapters/out") {
		for _, imp := range p.Imports {
			if archUnder(imp, "internal/app") {
				t.Errorf("%s imports %s: outbound adapters implement ports, they never call the application", p.ImportPath, imp)
			}
		}
	}
}

// The rules above must see the packages they guard, or they pass vacuously.
func TestArchListSeesTheModule(t *testing.T) {
	pkgs := archList(t)
	for _, dir := range []string{"internal/domain", "internal/ports", "internal/app", "internal/adapters/in/cli", "internal/adapters/out/git", "cmd/barnraise"} {
		if pkgs[module+"/"+dir] == nil {
			t.Errorf("go list did not report %s", dir)
		}
	}
	if p := pkgs[module+"/internal/domain"]; p != nil && len(p.Deps) == 0 {
		t.Error("go list reported no dependencies for the domain")
	}
	// A known non-standard dependency must be seen as one.
	found := false
	if p := pkgs[module+"/internal/config"]; p != nil {
		for _, d := range p.Deps {
			if d == "gopkg.in/yaml.v3" && pkgs[d] != nil && !pkgs[d].Standard {
				found = true
			}
		}
	}
	if !found {
		t.Error("internal/config should depend on the non-standard gopkg.in/yaml.v3")
	}
}
