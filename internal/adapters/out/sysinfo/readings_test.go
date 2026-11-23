package sysinfo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReadingsFromTheSystem(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("proc/meminfo", "MemTotal:       32000000 kB\nMemFree:         1000000 kB\nMemAvailable:    8000000 kB\n")
	write("proc/loadavg", "3.50 2.00 1.00 2/900 1234\n")
	write("sys/class/hwmon/hwmon0/name", "nvme\n")
	write("sys/class/hwmon/hwmon0/temp1_input", "71000\n") // a disk, not the processor
	write("sys/class/hwmon/hwmon1/name", "k10temp\n")
	write("sys/class/hwmon/hwmon1/temp1_input", "45625\n")
	write("sys/class/hwmon/hwmon1/temp2_input", "52250\n")
	write("sys/class/drm/gpu0/device/gpu_busy_percent", "37\n")
	write("sys/class/drm/gpu0/device/mem_info_vram_used", "2147483648\n")
	write("sys/class/drm/gpu0/device/mem_info_vram_total", "8589934592\n")
	p := &prober{root: root, cpus: 8, look: func(string) (string, error) { return "", os.ErrNotExist }}
	if got := p.cpuTemp(); got != 52.3 {
		t.Fatalf("cpu temp %v: the hottest reading of the processor's own sensor", got)
	}
	if got := p.memAvailableGB(); got < 7.5 || got > 7.7 {
		t.Fatalf("available memory %v", got)
	}
	gpus := p.gpus(context.Background())
	if len(gpus) != 1 || gpus[0].UtilPct != 37 || gpus[0].MemUsedGB != 2 || gpus[0].MemTotalGB != 8 {
		t.Fatalf("amd gpu %+v", gpus)
	}
	// Without a known processor sensor, the hottest thermal zone is used.
	bare := t.TempDir()
	root = bare
	write("sys/class/thermal/thermal_zone0/temp", "38000\n")
	write("sys/class/thermal/thermal_zone1/temp", "61000\n")
	write("sys/class/thermal/thermal_zone2/temp", "999000\n") // not a temperature
	if got := (&prober{root: bare}).cpuTemp(); got != 61 {
		t.Fatalf("thermal zones %v", got)
	}
	if got := (&prober{root: t.TempDir()}).cpuTemp(); got != 0 {
		t.Fatalf("no sensor: %v", got)
	}
}

func TestParseNvidiaSMI(t *testing.T) {
	gpus := ParseNvidiaSMI("NVIDIA GeForce RTX 5060 Ti, 12, 1141, 8151, 42\nTesla T4, 100, 15000, 15360, 81\nbroken line\n")
	if len(gpus) != 2 {
		t.Fatalf("%+v", gpus)
	}
	if g := gpus[0]; g.Name != "NVIDIA GeForce RTX 5060 Ti" || g.UtilPct != 12 || g.MemUsedGB != 1.1 || g.MemTotalGB != 8 || g.TempC != 42 {
		t.Fatalf("first %+v", g)
	}
	if g := gpus[1]; g.UtilPct != 100 || g.TempC != 81 {
		t.Fatalf("second %+v", g)
	}
	if len(ParseNvidiaSMI("")) != 0 {
		t.Fatal("empty output")
	}
}
