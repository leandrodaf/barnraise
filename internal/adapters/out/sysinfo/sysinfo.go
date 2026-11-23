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
	m.FreeMemGB = p.memAvailableGB()
	if load, ok := p.load1(); ok {
		m.Load1 = load
		m.FreeCPU = math.Max(0, float64(m.CPUs)-load)
	} else {
		m.FreeCPU = float64(m.CPUs)
	}
	m.TempC = p.cpuTemp()
	m.GPUs = p.gpus(ctx)
	m.GPU = len(m.GPUs) > 0 || p.gpu(ctx)
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

// memAvailableGB is what the kernel estimates new work can use without swapping.
func (p *prober) memAvailableGB() float64 {
	data, err := os.ReadFile(p.path("proc/meminfo"))
	if err != nil {
		return 0
	}
	return parseMemField(data, "MemAvailable:")
}

func parseMemTotal(data []byte) float64 { return parseMemField(data, "MemTotal:") }

func parseMemField(data []byte, field string) float64 {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && f[0] == field {
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

// cpuSensors are the hwmon drivers that report the processor's own temperature.
var cpuSensors = map[string]bool{"k10temp": true, "coretemp": true, "zenpower": true, "cpu_thermal": true, "cpu-thermal": true, "soc_thermal": true}

// cpuTemp is the processor's temperature in °C: the hottest reading of its
// own sensor, or of the thermal zones when no such sensor is known. 0 = unknown.
func (p *prober) cpuTemp() float64 {
	read := func(path string) (float64, bool) {
		data, err := os.ReadFile(path)
		if err != nil {
			return 0, false
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
		if err != nil {
			return 0, false
		}
		c := v / 1000
		return c, c > 0 && c < 150
	}
	best := 0.0
	dirs, _ := filepath.Glob(p.path("sys/class/hwmon/hwmon*"))
	for _, d := range dirs {
		name, _ := os.ReadFile(filepath.Join(d, "name"))
		if !cpuSensors[strings.TrimSpace(string(name))] {
			continue
		}
		inputs, _ := filepath.Glob(filepath.Join(d, "temp*_input"))
		for _, in := range inputs {
			if c, ok := read(in); ok && c > best {
				best = c
			}
		}
	}
	if best == 0 {
		zones, _ := filepath.Glob(p.path("sys/class/thermal/thermal_zone*/temp"))
		for _, z := range zones {
			if c, ok := read(z); ok && c > best {
				best = c
			}
		}
	}
	return math.Round(best*10) / 10
}

// gpus reads the live state of each GPU: NVIDIA through nvidia-smi, AMD
// through the kernel's own counters.
func (p *prober) gpus(ctx context.Context) []domain.GPUStat {
	var out []domain.GPUStat
	if bin, err := p.look("nvidia-smi"); err == nil {
		c, cancel := context.WithTimeout(ctx, p.cmdBudget)
		defer cancel()
		if data, err := p.run(c, bin, "--query-gpu=name,utilization.gpu,memory.used,memory.total,temperature.gpu", "--format=csv,noheader,nounits"); err == nil {
			out = append(out, ParseNvidiaSMI(string(data))...)
		}
	}
	// A device shows up under more than one name there (its display entry and
	// its render entry): each physical device is read once.
	entries, _ := filepath.Glob(p.path("sys/class/drm/*/device/gpu_busy_percent"))
	seen := map[string]bool{}
	for _, busy := range entries {
		dir := filepath.Dir(busy)
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			real = dir
		}
		if seen[real] {
			continue
		}
		seen[real] = true
		num := func(name string) float64 {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				return 0
			}
			v, _ := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
			return v
		}
		const gb = 1 << 30
		out = append(out, domain.GPUStat{Name: filepath.Base(filepath.Dir(dir)), UtilPct: num("gpu_busy_percent"),
			MemUsedGB: math.Round(num("mem_info_vram_used")/gb*10) / 10, MemTotalGB: math.Round(num("mem_info_vram_total")/gb*10) / 10})
	}
	return out
}

// ParseNvidiaSMI reads `nvidia-smi --query-gpu=name,utilization.gpu,memory.used,memory.total,temperature.gpu --format=csv,noheader,nounits`.
func ParseNvidiaSMI(out string) []domain.GPUStat {
	var gpus []domain.GPUStat
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, ",")
		if len(f) < 5 {
			continue
		}
		num := func(s string) float64 { v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64); return v }
		gpus = append(gpus, domain.GPUStat{Name: strings.TrimSpace(f[0]), UtilPct: num(f[1]),
			MemUsedGB: math.Round(num(f[2])/1024*10) / 10, MemTotalGB: math.Round(num(f[3])/1024*10) / 10, TempC: num(f[4])})
	}
	return gpus
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
