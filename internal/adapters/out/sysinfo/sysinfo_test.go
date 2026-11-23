package sysinfo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func fakeRoot(t *testing.T, gpu bool) string {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "proc"), 0o755)
	os.WriteFile(filepath.Join(root, "proc/meminfo"), []byte("MemTotal:       32768000 kB\nMemFree:  100 kB\n"), 0o644)
	os.WriteFile(filepath.Join(root, "proc/loadavg"), []byte("3.50 2.00 1.00 2/300 1234\n"), 0o644)
	if gpu {
		os.MkdirAll(filepath.Join(root, "dev/dri"), 0o755)
		os.WriteFile(filepath.Join(root, "dev/dri/renderD128"), nil, 0o644)
	}
	return root
}

const adbOut = `List of devices attached
emulator-5554	device
emulator-5556	offline
emulator-5558	device
R58M123ABC	device

`

func TestProbe(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "/bin/adb" {
			return []byte(adbOut), nil
		}
		return nil, errors.New("unexpected " + name)
	}
	look := func(n string) (string, error) {
		if n == "adb" {
			return "/bin/adb", nil
		}
		return "", errors.New("not found")
	}
	m := Probe(context.Background(), WithRoot(fakeRoot(t, true)), WithCPUs(8), WithCommands(run, look),
		WithHostname(func() (string, error) { return "box", nil }), WithNow(func() time.Time { return now }))
	if m.Name != "box" || m.CPUs != 8 || m.MemGB != 31.3 || m.FreeCPU != 4.5 || !m.GPU || m.Emulators != 2 || !m.LastSeen.Equal(now) {
		t.Fatalf("%+v", m)
	}
	if len(m.Labels) != 2 || m.Labels[0] != "gpu" || m.Labels[1] != "emulators" {
		t.Fatalf("labels %v", m.Labels)
	}
}

func TestProbeNvidiaAndConfiguredSlots(t *testing.T) {
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		return []byte("GPU 0: NVIDIA RTX (UUID: x)\n"), nil
	}
	look := func(n string) (string, error) { return "/bin/" + n, nil }
	m := Probe(context.Background(), WithRoot(fakeRoot(t, false)), WithCPUs(4), WithCommands(run, look), WithEmulatorSlots(3))
	if !m.GPU || m.Emulators != 3 {
		t.Fatalf("%+v", m)
	}
	// Nothing available.
	none := func(string) (string, error) { return "", errors.New("no") }
	m = Probe(context.Background(), WithRoot(t.TempDir()), WithCPUs(4), WithCommands(run, none))
	if m.GPU || m.Emulators != 0 || m.MemGB != 0 || m.FreeCPU != 4 {
		t.Fatalf("%+v", m)
	}
}

func TestProbeReal(t *testing.T) {
	m := Probe(context.Background(), WithCommands(
		func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("no") },
		func(string) (string, error) { return "", errors.New("no") }))
	if m.CPUs < 1 {
		t.Fatalf("%+v", m)
	}
}

func TestSlots(t *testing.T) {
	cases := []struct {
		cpus  int
		mem   float64
		per   float64
		emu   int
		slots int
	}{
		{16, 64, 4, 0, 8}, // cpu-bound: 16/2
		{16, 18, 4, 0, 4}, // mem-bound: (18-2)/4
		{16, 18, 4, 2, 2}, // emulators take memory: (18-2-6)/4
		{1, 2, 4, 0, 1},   // at least one
		{8, 0, 4, 0, 4},   // unknown memory
		{8, 34, 0, 0, 4},  // default per-agent memory
	}
	for _, c := range cases {
		got := Slots(domain.Machine{CPUs: c.cpus, MemGB: c.mem}, c.per, c.emu)
		if got != c.slots {
			t.Errorf("Slots(cpus=%d mem=%v per=%v emu=%d) = %d, want %d", c.cpus, c.mem, c.per, c.emu, got, c.slots)
		}
	}
}
