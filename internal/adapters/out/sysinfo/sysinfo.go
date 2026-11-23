// Package sysinfo probes the local machine (CPUs, memory, load, GPU,
// Android emulators) and computes how many agents it can run.
package sysinfo

import (
	"bufio"
	"bytes"
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// Option configures Probe.
type Option func(*prober)

type prober struct {
	root      string // filesystem root for /proc and /dev (tests)
	cpus      int
	emuSlots  int
	run       func(ctx context.Context, name string, args ...string) ([]byte, error)
	look      func(string) (string, error)
	hostname  func() (string, error)
	now       func() time.Time
	cmdBudget time.Duration
}

// WithRoot reads /proc and /dev under root (tests).
func WithRoot(root string) Option { return func(p *prober) { p.root = root } }

// WithCPUs overrides the CPU count (tests).
func WithCPUs(n int) Option { return func(p *prober) { p.cpus = n } }

// WithEmulatorSlots reports configured emulator slots instead of counting
// running emulators with adb.
func WithEmulatorSlots(n int) Option { return func(p *prober) { p.emuSlots = n } }

// WithCommands replaces command execution and lookup (tests).
func WithCommands(run func(ctx context.Context, name string, args ...string) ([]byte, error), look func(string) (string, error)) Option {
	return func(p *prober) { p.run, p.look = run, look }
}

// WithHostname replaces os.Hostname (tests).
func WithHostname(f func() (string, error)) Option { return func(p *prober) { p.hostname = f } }

// WithNow sets the clock for LastSeen (tests).
func WithNow(f func() time.Time) Option { return func(p *prober) { p.now = f } }

// Probe describes this machine. FreeCPU is idle cores: CPUs minus the
// 1-minute load average, never below zero. Missing sources leave zeros.
func Probe(ctx context.Context, opts ...Option) domain.Machine {
	p := &prober{
		root: "/", cpus: runtime.NumCPU(), emuSlots: -1, look: exec.LookPath, hostname: os.Hostname,
		now: time.Now, cmdBudget: 3 * time.Second,
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
	}
	for _, o := range opts {
		o(p)
	}
	m := domain.Machine{CPUs: p.cpus, LastSeen: p.now().UTC()}
	if h, err := p.hostname(); err == nil {
		m.Name = h
	}
	m.MemGB = p.memGB()
	if load, ok := p.load1(); ok {
		m.FreeCPU = math.Max(0, float64(m.CPUs)-load)
	} else {
		m.FreeCPU = float64(m.CPUs)
	}
	m.GPU = p.gpu(ctx)
	if p.emuSlots >= 0 {
		m.Emulators = p.emuSlots
	} else {
		m.Emulators = p.emulators(ctx)
	}
	if m.GPU {
		m.Labels = append(m.Labels, "gpu")
	}
	if m.Emulators > 0 {
		m.Labels = append(m.Labels, "emulators")
	}
	return m
}

func (p *prober) path(s string) string { return filepath.Join(p.root, s) }

func (p *prober) memGB() float64 {
	data, err := os.ReadFile(p.path("proc/meminfo"))
	if err != nil {
		return 0
	}
	return parseMemTotal(data)
}

func parseMemTotal(data []byte) float64 {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && f[0] == "MemTotal:" {
			kb, err := strconv.ParseFloat(f[1], 64)
			if err != nil {
				return 0
			}
			return math.Round(kb/1024/1024*10) / 10
		}
	}
	return 0
}

func (p *prober) load1() (float64, bool) {
	data, err := os.ReadFile(p.path("proc/loadavg"))
	if err != nil {
		return 0, false
	}
	f := strings.Fields(string(data))
	if len(f) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	return v, err == nil
}

func (p *prober) gpu(ctx context.Context) bool {
	if bin, err := p.look("nvidia-smi"); err == nil {
		c, cancel := context.WithTimeout(ctx, p.cmdBudget)
		defer cancel()
		if out, err := p.run(c, bin, "-L"); err == nil && strings.Contains(string(out), "GPU") {
			return true
		}
	}
	matches, _ := filepath.Glob(p.path("dev/dri/renderD*"))
	return len(matches) > 0
}

func (p *prober) emulators(ctx context.Context) int {
	bin, err := p.look("adb")
	if err != nil {
		return 0
	}
	c, cancel := context.WithTimeout(ctx, p.cmdBudget)
	defer cancel()
	out, err := p.run(c, bin, "devices")
	if err != nil {
		return 0
	}
	return CountEmulators(out)
}

// CountEmulators counts running emulators in `adb devices` output.
func CountEmulators(out []byte) int {
	n := 0
	for l := range strings.SplitSeq(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 && strings.HasPrefix(f[0], "emulator-") && f[1] == "device" {
			n++
		}
	}
	return n
}

// Capacity formula constants.
const (
	CPUsPerAgent      = 2.0 // an agent plus its builds
	ReservedMemGB     = 2.0 // the system and Barnraise itself
	MemPerEmulatorGB  = 3.0 // an Android emulator with its build
	DefaultAgentMemGB = 4.0
)

// Slots is how many agents the machine runs at once:
// min(CPUs / CPUsPerAgent, (MemGB - ReservedMemGB - emulatorSlots*MemPerEmulatorGB) / perAgentMemGB),
// rounded down and at least 1. perAgentMemGB <= 0 uses DefaultAgentMemGB.
// Unknown memory (0) leaves the CPU bound alone.
func Slots(m domain.Machine, perAgentMemGB float64, emulatorSlots int) int {
	if perAgentMemGB <= 0 {
		perAgentMemGB = DefaultAgentMemGB
	}
	cpu := math.Floor(float64(m.CPUs) / CPUsPerAgent)
	slots := cpu
	if m.MemGB > 0 {
		mem := math.Floor((m.MemGB - ReservedMemGB - float64(max(emulatorSlots, 0))*MemPerEmulatorGB) / perAgentMemGB)
		slots = math.Min(cpu, mem)
	}
	if slots < 1 || math.IsNaN(slots) {
		return 1
	}
	return int(slots)
}
