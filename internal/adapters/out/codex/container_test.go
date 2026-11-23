package codex

import (
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// On the machine itself Codex is always in workspace-write. Inside a
// container the container is the boundary and bubblewrap cannot nest.
func TestSandboxModeFollowsTheContainer(t *testing.T) {
	p := New(Options{})
	host, err := p.Args(ports.SessionSpec{Workdir: "/w"}, "")
	if err != nil || !strings.Contains(strings.Join(host, " "), "--sandbox workspace-write") {
		t.Fatalf("host %v %v", host, err)
	}
	box, err := p.Args(ports.SessionSpec{Workdir: "/w", Container: &ports.ContainerSpec{Image: "agent"}}, "")
	if err != nil || !strings.Contains(strings.Join(box, " "), "--sandbox danger-full-access") {
		t.Fatalf("container %v %v", box, err)
	}
	for _, a := range append(host, box...) {
		if strings.Contains(a, "dangerously-bypass") {
			t.Fatalf("the bypass flag is never used: %v", a)
		}
	}
}
