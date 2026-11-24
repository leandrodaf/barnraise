package main

import (
	"github.com/leandrodaf/barnraise/internal/adapters/out/landlock"
	"github.com/leandrodaf/barnraise/internal/adapters/out/microvm"
	"github.com/leandrodaf/barnraise/internal/adapters/out/toolinstall"
	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/daemoncfg"
)

// isolation builds Barnraise's own confinement layers: Landlock always (it
// reports itself unavailable where the kernel has none), the microVM only
// when daemon.yaml turns it on.
func (d *daemon) isolation(binary string) app.Isolation {
	iso := app.Isolation{Landlock: landlock.New(binary), ToolDirs: []string{toolinstall.Root(d.dataDir)}}
	cfg := d.df.Isolation
	if err := cfg.Validate(); err != nil {
		d.log.Error("daemon.yaml: isolation ignored", "err", err)
		cfg = daemoncfg.Isolation{}
	}
	if cfg.Backend == "microvm" {
		iso.MicroVM = microvm.New(MicroVMOptions(cfg.MicroVM))
		iso.MicroVMFor = cfg.MicroVMOn
	}
	d.log.Info("sandbox", "landlock", iso.Landlock.Support().String())
	if iso.MicroVM != nil {
		d.log.Info("sandbox", "microvm", iso.MicroVM.Support().String(), "for", cfg.MicroVM.For)
	}
	d.iso = iso
	return iso
}

// MicroVMOptions turns daemon.yaml's keys into the runner's options.
func MicroVMOptions(m daemoncfg.MicroVM) microvm.Options {
	return microvm.Options{Runner: m.Runner, Template: m.Command, Image: m.Image, CPUs: m.CPUs, MemoryMB: m.MemoryMB, Mounts: m.Mounts}
}

// sandboxLabels are the machine labels of its confinement layers: a check
// can ask for a machine that confines it (`runs_on: machine:landlock`).
func (d *daemon) sandboxLabels() []string {
	var out []string
	if d.iso.Landlock != nil && d.iso.Landlock.Support().Available {
		out = append(out, "landlock")
	}
	if d.iso.MicroVM != nil && d.iso.MicroVM.Support().Available {
		out = append(out, "microvm")
	}
	return out
}
