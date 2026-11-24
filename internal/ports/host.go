package ports

import (
	"errors"
	"time"
)

// ErrNoProcesses says this platform's processes cannot be listed cheaply.
var ErrNoProcesses = errors.New("processes are not listed on this platform")

// Proc is one process as the system shows it in a single cheap read.
type Proc struct {
	PID  int
	PPID int
	Name string
	// Start identifies the process together with the pid (pids are reused).
	Start int64
	// CPU is the processor time used since it started (user and system).
	CPU time.Duration
	// RSS is the memory resident now, in bytes.
	RSS       int64
	StartedAt time.Time
}

// Host reads the machine: its processes and its disk. It is asked on a
// timer, never in a request, and each call must stay cheap.
type Host interface {
	// Processes appends every process of the machine to buf[:0].
	Processes(buf []Proc) ([]Proc, error)
	// Cwd is the working directory of a process ("" when it cannot be read).
	Cwd(pid int) string
	// Cmdline is the argument list of a process.
	Cmdline(pid int) []string
	// Disk is the free and total space of the filesystem holding path.
	Disk(path string) (free, total int64)
}
