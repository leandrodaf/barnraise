package gemini

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// On the machine itself a Gemini session is a reader: approval mode
// "default" and no shell, write or edit tools. Inside a container the
// container is the boundary and the session may act.
func TestApprovalFollowsTheContainer(t *testing.T) {
	p := New(Options{})
	host := p.Args(ports.SessionSpec{Workdir: "/w"}, nil)
	if !strings.Contains(strings.Join(host, " "), "--approval-mode default") {
		t.Fatalf("host %v", host)
	}
	box := p.Args(ports.SessionSpec{Workdir: "/w", Container: &ports.ContainerSpec{Image: "agent"}}, nil)
	if !strings.Contains(strings.Join(box, " "), "--approval-mode yolo") {
		t.Fatalf("container %v", box)
	}
	if slices.Contains(host, "--yolo") || slices.Contains(box, "--yolo") || slices.Contains(host, "--sandbox") {
		t.Fatalf("unexpected flags: %v %v", host, box)
	}

	excluded := func(spec ports.SessionSpec) []string {
		b, _, err := p.Settings(spec)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Tools struct{ Exclude []string } `json:"tools"`
		}
		_ = json.Unmarshal(b, &doc)
		return doc.Tools.Exclude
	}
	if got := excluded(ports.SessionSpec{Workdir: "/w"}); !slices.Equal(got, HostExcludedTools) {
		t.Fatalf("host excludes %v", got)
	}
	if got := excluded(ports.SessionSpec{Workdir: "/w", Container: &ports.ContainerSpec{Image: "agent"}}); len(got) != 0 {
		t.Fatalf("container excludes %v", got)
	}
}
