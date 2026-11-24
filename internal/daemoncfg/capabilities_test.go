package daemoncfg

import "testing"

func TestCapabilitySettings(t *testing.T) {
	f := &File{Network: "plugin", NetworkPlugin: &NetworkPlugin{Command: " mynet "},
		Isolation: Isolation{Backend: "microvm", MicroVM: MicroVM{Runner: "smolvm", Command: []string{"x", "{command}"}}}}
	m := f.CapabilitySettings(" 127.0.0.1:7421 ")
	if m.Egress != "127.0.0.1:7421" || m.IsolationBackend != "microvm" || m.MicroVMImage == "" || m.MicroVMRunner != "smolvm" ||
		m.Network != "plugin" || m.NetworkPlugin != "mynet" {
		t.Fatalf("%+v", m)
	}
	if m := (&File{Isolation: Isolation{Backend: "landlock", MicroVM: MicroVM{Image: "img"}}}).CapabilitySettings(""); m.IsolationBackend != "" || m.MicroVMImage != "img" {
		t.Fatalf("%+v", m)
	}
}
