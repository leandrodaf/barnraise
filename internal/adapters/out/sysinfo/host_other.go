//go:build !linux

package sysinfo

import "github.com/leandrodaf/barnraise/internal/ports"

// Processes is not available: only Linux lists every process in one cheap pass.
func (h *Host) Processes(buf []ports.Proc) ([]ports.Proc, error) {
	return buf[:0], ports.ErrNoProcesses
}

// Cwd is unknown on this platform.
func (h *Host) Cwd(int) string { return "" }

// Cmdline is unknown on this platform.
func (h *Host) Cmdline(int) []string { return nil }
