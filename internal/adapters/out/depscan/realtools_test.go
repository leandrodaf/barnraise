package depscan

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/leandrodaf/barnraise/internal/adapters/out/execx"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// These reach api.osv.dev; they run only with BARNRAISE_REAL_NET=1.
func realNet(t *testing.T) {
	if os.Getenv("BARNRAISE_REAL_NET") != "1" {
		t.Skip("set BARNRAISE_REAL_NET=1 to query the real OSV")
	}
}

func checkRealFindings(t *testing.T, rep Report, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Failing) == 0 {
		t.Fatalf("golang.org/x/net v0.7.0 and lodash 4.17.20 have high advisories; got %+v", rep)
	}
	t.Logf("%s: %d checked, %d new, %d failing", rep.Scanner, rep.Checked, len(rep.New), len(rep.Failing))
	for _, f := range rep.Failing {
		t.Logf("  %s %s %v %s", f.Package.Name, f.Package.Version, f.IDs, f.Severity)
	}
}

func TestRealOSVAPI(t *testing.T) {
	realNet(t)
	dir, base := repo(t)
	s := New(Options{Exec: execx.New(), LookPath: noScanner})
	rep, err := s.Scan(context.Background(), spec(dir, base, "go.mod", "web/package.json"), domain.DependencyPolicy{})
	checkRealFindings(t, rep, err)
}

func TestRealOSVScanner(t *testing.T) {
	realNet(t)
	if _, err := exec.LookPath("osv-scanner"); err != nil {
		t.Skip("osv-scanner is not installed")
	}
	dir, base := repo(t)
	s := New(Options{Exec: execx.New()})
	rep, err := s.Scan(context.Background(), spec(dir, base, "go.mod", "web/package.json"), domain.DependencyPolicy{Scanner: "osv-scanner"})
	checkRealFindings(t, rep, err)
}
