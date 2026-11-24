package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"runtime"
	"syscall"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// Single version across the cluster: every machine publishes its own binary
// in its object store; a machine that finds a newer version on a peer with
// the same platform fetches it (by hash), verifies it, swaps its executable
// and restarts — only when no agent session is running, so no step breaks.

func platform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// publishBinary stores this binary in the object store and returns its hash.
func (d *daemon) publishBinary(ctx context.Context) string {
	exe, err := os.Executable()
	if err != nil || d.blobs == nil {
		return ""
	}
	f, err := os.Open(exe)
	if err != nil {
		return ""
	}
	defer f.Close()
	h, err := d.blobs.Put(ctx, f)
	if err != nil {
		d.log.Warn("publish binary", "err", err)
		return ""
	}
	return h
}

// watchVersions updates this machine when a peer runs a newer version.
func (d *daemon) watchVersions(ctx context.Context, self string) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if os.Getenv("BARNRAISE_NO_SELF_UPDATE") != "" || d.blobs == nil || len(d.svc.Runtime().Sessions()) > 0 {
			continue
		}
		var target *domain.Machine
		for _, name := range d.svc.Projects() {
			p, err := d.svc.Project(name)
			if err != nil {
				continue
			}
			for _, m := range p.Snapshot().Machines {
				if m.Name == d.df.Machine || m.Binary == "" || m.Binary == self || m.Platform != platform() || m.Left {
					continue
				}
				if b := (domain.Build{Version: m.Version, Time: m.BuildTime}); b.Newer(selfBuild()) && (target == nil || b.Newer(domain.Build{Version: target.Version, Time: target.BuildTime})) {
					mm := *m
					target = &mm
				}
			}
		}
		if target == nil {
			continue
		}
		if err := d.selfUpdate(ctx, target); err != nil {
			d.log.Warn("self-update", "from", target.Name, "version", target.Version, "err", err)
			continue
		}
		return
	}
}

func (d *daemon) selfUpdate(ctx context.Context, m *domain.Machine) error {
	d.log.Info("a newer version runs on the cluster; updating", "from", m.Name, "version", m.Version, "current", version)
	rc, err := d.blobs.Get(ctx, m.Binary)
	if err != nil {
		return fmt.Errorf("fetch binary %s: %w", m.Binary, err)
	}
	defer rc.Close()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	tmp := exe + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), rc); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != m.Binary {
		os.Remove(tmp)
		return fmt.Errorf("binary hash mismatch: got %s, want %s", got, m.Binary)
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return err
	}
	d.log.Info("binary replaced; restarting", "version", m.Version)
	d.svc.Runtime().StopAll()
	d.stopNATS()
	d.closeTailnet()
	_ = d.store.Close()
	_ = os.Remove(d.pidFile)
	return syscall.Exec(exe, os.Args, os.Environ())
}
