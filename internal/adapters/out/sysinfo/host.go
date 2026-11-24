package sysinfo

// Host reads the machine's processes and disk for the daemon's inventory.
// Root is where /proc is looked for ("" is the real one); tests point it at
// a folder.
type Host struct {
	Root string
}

// NewHost returns the reader of this machine.
func NewHost() *Host { return &Host{} }
