package daemoncfg

import (
	"fmt"
	"slices"
	"strings"
)

// Isolation is `isolation:` in daemon.yaml: an extra layer this machine puts
// around agent sessions and checks. Off by default; Landlock (when the
// kernel has it) is used without any key.
type Isolation struct {
	// Backend is "" (Landlock where the provider's own sandbox cannot
	// start, and for setup and checks) or microvm.
	Backend string  `yaml:"backend,omitempty"`
	MicroVM MicroVM `yaml:"microvm,omitempty"`
}

// MicroVM configures the microVM runner (smolvm, or another program).
type MicroVM struct {
	Runner   string   `yaml:"runner,omitempty"`  // program; default smolvm
	Command  []string `yaml:"command,omitempty"` // template; default smolvm's command line
	Image    string   `yaml:"image,omitempty"`   // OCI image with the toolchain (and the AI CLIs for sessions)
	CPUs     int      `yaml:"cpus,omitempty"`
	MemoryMB int      `yaml:"memory_mb,omitempty"`
	Mounts   []string `yaml:"mounts,omitempty"` // more host paths ("path" or "path:ro")
	// For lists what runs in the VM: sessions, checks, setup (default all).
	For []string `yaml:"for,omitempty"`
}

// MicroVMKinds are the kinds of command a microVM can run.
var MicroVMKinds = []string{"sessions", "checks", "setup"}

// MicroVMOn reports whether kind runs in a microVM on this machine.
func (i Isolation) MicroVMOn(kind string) bool {
	if i.Backend != "microvm" {
		return false
	}
	if len(i.MicroVM.For) == 0 {
		return true
	}
	return slices.Contains(i.MicroVM.For, kind)
}

// Validate reports mistakes in the isolation keys.
func (i Isolation) Validate() error {
	switch i.Backend {
	case "", "landlock", "microvm":
	default:
		return fmt.Errorf("isolation.backend %q is not microvm (leave it empty for Landlock)", i.Backend)
	}
	for _, k := range i.MicroVM.For {
		if !slices.Contains(MicroVMKinds, k) {
			return fmt.Errorf("isolation.microvm.for: %q is not one of %s", k, strings.Join(MicroVMKinds, ", "))
		}
	}
	if i.MicroVM.CPUs < 0 || i.MicroVM.MemoryMB < 0 {
		return fmt.Errorf("isolation.microvm: cpus and memory_mb cannot be negative")
	}
	if c := i.MicroVM.Command; len(c) > 0 && !slices.Contains(c, "{command}") {
		return fmt.Errorf("isolation.microvm.command must contain {command} as a whole argument")
	}
	if i.Backend == "microvm" && i.MicroVM.Image == "" && len(i.MicroVM.Command) == 0 {
		return fmt.Errorf("isolation.microvm.image is required: an image with the project's toolchain (and the AI CLIs for sessions)")
	}
	return nil
}
