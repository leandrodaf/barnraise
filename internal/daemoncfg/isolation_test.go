package daemoncfg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsolation(t *testing.T) {
	if (Isolation{}).MicroVMOn("sessions") {
		t.Fatal("off by default")
	}
	vm := Isolation{Backend: "microvm", MicroVM: MicroVM{Image: "img"}}
	if !vm.MicroVMOn("checks") || vm.Validate() != nil {
		t.Fatal("on for everything")
	}
	vm.MicroVM.For = []string{"checks"}
	if vm.MicroVMOn("sessions") || !vm.MicroVMOn("checks") {
		t.Fatal("only checks")
	}
	for _, bad := range []Isolation{
		{Backend: "gvisor"},
		{Backend: "microvm"},
		{Backend: "microvm", MicroVM: MicroVM{Image: "i", For: []string{"builds"}}},
		{Backend: "microvm", MicroVM: MicroVM{Image: "i", CPUs: -1}},
		{Backend: "microvm", MicroVM: MicroVM{Command: []string{"vm", "run"}}},
	} {
		if bad.Validate() == nil {
			t.Errorf("%+v must be refused", bad)
		}
	}
	if (Isolation{Backend: "microvm", MicroVM: MicroVM{Command: []string{"vm", "--", "{command}"}}}).Validate() != nil {
		t.Fatal("a template without an image is fine")
	}
}

func TestIsolationRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.yaml")
	yaml := "machine: m\nisolation:\n  backend: microvm\n  microvm:\n    image: ghcr.io/me/agents:1\n    cpus: 2\n    memory_mb: 4096\n    for: [checks]\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Isolation.Backend != "microvm" || f.Isolation.MicroVM.Image != "ghcr.io/me/agents:1" || f.Isolation.MicroVM.MemoryMB != 4096 || !f.Isolation.MicroVMOn("checks") {
		t.Fatalf("%+v", f.Isolation)
	}
}
