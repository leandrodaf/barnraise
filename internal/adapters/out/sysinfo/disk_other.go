//go:build !unix

package sysinfo

// Disk is unknown on this platform.
func (h *Host) Disk(string) (free, total int64) { return 0, 0 }
