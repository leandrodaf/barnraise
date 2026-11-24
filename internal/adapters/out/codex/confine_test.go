package codex

import (
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// Confined by Barnraise (Landlock or a microVM), Codex's own sandbox is
// off: the wrapper is the boundary, and bubblewrap may not start there.
func TestSandboxModeUnderBarnraiseConfinement(t *testing.T) {
	p := New(Options{})
	spec := ports.SessionSpec{Workdir: "/w", Confine: &ports.Confine{Backend: "landlock"}}
	args, err := p.Args(spec, "")
	if err != nil || !strings.Contains(strings.Join(args, " "), "--sandbox danger-full-access") {
		t.Fatalf("%v %v", args, err)
	}
	spec.ResumeID = "s1"
	args, _ = p.Args(spec, "")
	if !strings.Contains(strings.Join(args, " "), `sandbox_mode="danger-full-access"`) {
		t.Fatalf("resume: %v", args)
	}
}
