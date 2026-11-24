package daemoncfg

import (
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// CapabilitySettings are the machine's switches the capability catalog
// reads (ADR-0046); egress is the egress proxy's address ("" when it does
// not run).
func (f *File) CapabilitySettings(egress string) domain.MachineSettings {
	m := domain.MachineSettings{Egress: strings.TrimSpace(egress), Network: f.Network}
	if f.Isolation.Backend == "microvm" {
		m.IsolationBackend = "microvm"
	}
	m.MicroVMImage = f.Isolation.MicroVM.Image
	if m.MicroVMImage == "" && len(f.Isolation.MicroVM.Command) > 0 {
		m.MicroVMImage = "(the command template's own)"
	}
	m.MicroVMRunner = f.Isolation.MicroVM.Runner
	if f.NetworkPlugin != nil {
		m.NetworkPlugin = strings.TrimSpace(f.NetworkPlugin.Command)
	}
	return m
}
