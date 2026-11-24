package cli

import (
	"bytes"
	"strings"
	"testing"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/domain"
)

func TestWriteCapabilities(t *testing.T) {
	res := &pb.CapabilitiesResponse{Machines: []*pb.MachineCapabilities{
		{Machine: "alpha", Platform: "linux/amd64", ProbedAt: "2026-11-24T12:00:00Z", Counts: map[string]int32{"active": 1, "degraded": 1, "missing": 1},
			Items: []*pb.CapabilityItem{
				{Id: "merge.structural", Title: "Structural merge", Group: "merge", Status: "degraded", Reason: "mergiraf is not installed.",
					Fallback: "line merge", Fix: "barnraise tools install mergiraf", Tool: "mergiraf"},
				{Id: "security.trust", Title: "Trust", Group: "security", Status: "active", Version: "1.0"},
				{Id: "sandbox.bubblewrap", Title: "Bubblewrap", Group: "isolation", Status: "missing", Reason: "refused.", Fix: "sudo apt install bubblewrap", NeedsRoot: true},
			},
			Tools: []*pb.CapabilityTool{{Id: "rtk", Pinned: "1.2.1", Installed: "1.2.1", Managed: true, Available: true},
				{Id: "smolvm", Pinned: "1.37.0"}, {Id: "mergiraf", Pinned: "0.20.0", Installed: "0.19", Path: "/usr/bin/mergiraf", Available: true}}},
		{Machine: "beta", Error: "unreachable"},
	}}
	var b bytes.Buffer
	writeCapabilities(&b, res)
	out := b.String()
	for _, want := range []string{"Machine alpha (linux/amd64)", "1 active, 1 degraded, 1 missing", "security\n  ✔ active", "Trust (1.0)",
		"falls back: line merge", "fix:   barnraise tools install mergiraf", "needs root", "Machine beta: unreachable"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Index(out, "security") > strings.Index(out, "isolation") || strings.Index(out, "isolation") > strings.Index(out, "merge\n") {
		t.Errorf("groups out of order:\n%s", out)
	}
	b.Reset()
	writeTools(&b, res)
	out = b.String()
	for _, want := range []string{"installed by Barnraise: 1.2.1", "not installed; no build for this platform", "on the PATH: 0.19 (/usr/bin/mergiraf)", "Machine beta: unreachable"} {
		if !strings.Contains(out, want) {
			t.Errorf("tools missing %q in:\n%s", want, out)
		}
	}
	b.Reset()
	writeToolResults(&b, []*pb.ToolInstallResult{{Machine: "a", Tool: "rtk", Ok: true, Changed: true, Version: "1"}, {Machine: "a", Tool: "x", Error: "boom"},
		{Machine: "a", Tool: "rtk", Ok: true}}, false)
	writeToolResults(&b, []*pb.ToolInstallResult{{Machine: "a", Tool: "rtk", Ok: true, Changed: true}, {Machine: "a", Tool: "rtk", Ok: true}}, true)
	out = b.String()
	for _, want := range []string{"✔ rtk 1 installed on a", "✘ x on a: boom", "already installed", "rtk removed from a", "nothing to remove"} {
		if !strings.Contains(out, want) {
			t.Errorf("results missing %q in:\n%s", want, out)
		}
	}
	if groupOrder("nope") != len(domain.CapGroups) {
		t.Fatal("unknown group last")
	}
}

func TestMachineFlags(t *testing.T) {
	if (machineFlags{all: true, machines: []string{"a"}}).check() == nil {
		t.Fatal("contradiction")
	}
	if !(machineFlags{}).onlyHere("a") || !(machineFlags{machines: []string{"a"}}).onlyHere("a") ||
		(machineFlags{machines: []string{"b"}}).onlyHere("a") || (machineFlags{all: true}).onlyHere("a") {
		t.Fatal("onlyHere")
	}
	if !attentionOn(&pb.CapabilityItem{Status: "degraded", Reason: "Switched off"}) || attentionOn(&pb.CapabilityItem{Status: "missing", Reason: "Switched off, x"}) ||
		!attentionOn(&pb.CapabilityItem{Status: "missing", Reason: "No claude account"}) {
		t.Fatal("attention")
	}
}
