package checks

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

func TestBuiltinKindsRunHereAndKeepTheirLog(t *testing.T) {
	var got ports.CheckSpec
	r := newRunner(t, Options{Builtin: map[string]Builtin{
		domain.RunsOnDependencies: func(_ context.Context, spec ports.CheckSpec) (domain.CheckResult, []byte, error) {
			got = spec
			return domain.CheckResult{Passed: false, MainRed: false, Summary: "1 known vulnerability", Duration: time.Second}, []byte("full report\n"), nil
		},
	}})
	s := spec(t.TempDir(), "")
	s.Def = domain.CheckDef{Name: "dependencies", RunsOn: domain.RunsOnDependencies}
	s.Base = "b4se"
	res, pending, err := r.Run(context.Background(), s)
	if err != nil || pending {
		t.Fatalf("err=%v pending=%v", err, pending)
	}
	if res.Passed || res.Summary != "1 known vulnerability" || res.RunsOn != domain.RunsOnDependencies || res.Duration != time.Second || res.Check != "dependencies" || got.Base != "b4se" {
		t.Fatalf("%+v (spec %+v)", res, got)
	}
	if log, _ := os.ReadFile(res.LogPath); string(log) != "full report\n" {
		t.Fatalf("log %q", log)
	}

	// A kind that is not built in is an error, not a pass.
	s.Def.RunsOn = "builtin:nope"
	if _, _, err := r.Run(context.Background(), s); err == nil || !strings.Contains(err.Error(), "no built-in check kind builtin:nope") {
		t.Fatalf("unknown kind: %v", err)
	}
}

func TestDefaultBuiltinsAreWired(t *testing.T) {
	r := NewRunner(Options{Dependencies: domain.DependencyPolicy{FailAt: "critical"}})
	for _, k := range domain.BuiltinChecks {
		if r.o.Builtin[k] == nil {
			t.Fatalf("%s is not wired", k)
		}
	}
	// The dependencies check of a change without manifests passes at once.
	s := spec(t.TempDir(), "")
	s.Def = domain.CheckDef{Name: "dependencies", RunsOn: domain.RunsOnDependencies}
	s.Touched = []string{"README.md"}
	r.o.LogDir = t.TempDir()
	res, _, err := r.Run(context.Background(), s)
	if err != nil || !res.Passed || res.Summary != "no dependency lockfile changed" {
		t.Fatalf("%+v %v", res, err)
	}
}
