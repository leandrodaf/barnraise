//go:build linux

package sysinfo

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// Linux counts processor time in ticks of 1/100 s for every architecture
// Barnraise builds for (USER_HZ).
const tick = 10 * time.Millisecond

var (
	bootOnce sync.Once
	bootAt   time.Time
)

func (h *Host) proc(parts ...string) string {
	root := h.Root
	if root == "" {
		root = "/"
	}
	return filepath.Join(append([]string{root, "proc"}, parts...)...)
}

// bootTime reads when the machine started (process start times count from it).
func (h *Host) bootTime() time.Time {
	read := func() time.Time {
		data, err := os.ReadFile(h.proc("stat"))
		if err != nil {
			return time.Time{}
		}
		for line := range bytes.SplitSeq(data, []byte("\n")) {
			if rest, ok := bytes.CutPrefix(line, []byte("btime ")); ok {
				if sec, err := strconv.ParseInt(string(bytes.TrimSpace(rest)), 10, 64); err == nil {
					return time.Unix(sec, 0)
				}
			}
		}
		return time.Time{}
	}
	if h.Root != "" {
		return read()
	}
	bootOnce.Do(func() { bootAt = read() })
	return bootAt
}

// Processes reads /proc/<pid>/stat of every process: one small read each.
func (h *Host) Processes(buf []ports.Proc) ([]ports.Proc, error) {
	dir, err := os.Open(h.proc())
	if err != nil {
		return buf[:0], err
	}
	names, err := dir.Readdirnames(-1)
	_ = dir.Close()
	if err != nil {
		return buf[:0], err
	}
	boot := h.bootTime()
	page := int64(os.Getpagesize())
	out := buf[:0]
	var scratch [512]byte
	for _, name := range names {
		if name[0] < '0' || name[0] > '9' {
			continue
		}
		pid, err := strconv.Atoi(name)
		if err != nil {
			continue
		}
		f, err := os.Open(h.proc(name, "stat"))
		if err != nil {
			continue // it ended meanwhile
		}
		n, _ := f.Read(scratch[:])
		_ = f.Close()
		if p, ok := parseStat(scratch[:n], pid, page, boot); ok {
			out = append(out, p)
		}
	}
	return out, nil
}

// parseStat reads one /proc/<pid>/stat line. The name is between the first
// "(" and the last ")": it may hold spaces and parentheses itself.
func parseStat(data []byte, pid int, page int64, boot time.Time) (ports.Proc, bool) {
	open, end := bytes.IndexByte(data, '('), bytes.LastIndexByte(data, ')')
	if open < 0 || end < open {
		return ports.Proc{}, false
	}
	p := ports.Proc{PID: pid, Name: string(data[open+1 : end])}
	// After the name: state(3) ppid(4) … utime(14) stime(15) … starttime(22) vsize(23) rss(24).
	field := 2
	var utime, stime int64
	for part := range bytes.FieldsSeq(data[end+1:]) {
		field++
		switch field {
		case 4:
			p.PPID, _ = strconv.Atoi(string(part))
		case 14:
			utime, _ = strconv.ParseInt(string(part), 10, 64)
		case 15:
			stime, _ = strconv.ParseInt(string(part), 10, 64)
		case 22:
			p.Start, _ = strconv.ParseInt(string(part), 10, 64)
		case 24:
			pages, _ := strconv.ParseInt(string(part), 10, 64)
			p.RSS = pages * page
		}
		if field >= 24 {
			break
		}
	}
	if field < 24 {
		return ports.Proc{}, false
	}
	p.CPU = time.Duration(utime+stime) * tick
	if !boot.IsZero() {
		p.StartedAt = boot.Add(time.Duration(p.Start) * tick)
	}
	return p, true
}

// Cwd is the working directory of a process.
func (h *Host) Cwd(pid int) string {
	dir, err := os.Readlink(h.proc(strconv.Itoa(pid), "cwd"))
	if err != nil {
		return ""
	}
	return dir
}

// Cmdline is the argument list of a process.
func (h *Host) Cmdline(pid int) []string {
	f, err := os.Open(h.proc(strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var buf [1024]byte // the first arguments say what it is; the rest is not read
	n, _ := f.Read(buf[:])
	var args []string
	for arg := range bytes.SplitSeq(bytes.TrimRight(buf[:n], "\x00"), []byte{0}) {
		args = append(args, string(arg))
	}
	return args
}
