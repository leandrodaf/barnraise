//go:build unix

package sysinfo

import "syscall"

// Disk is the free and total space of the filesystem holding path, in bytes.
func (h *Host) Disk(path string) (free, total int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	return int64(st.Bavail) * int64(st.Bsize), int64(st.Blocks) * int64(st.Bsize) //nolint:unconvert // the field types differ by platform
}
