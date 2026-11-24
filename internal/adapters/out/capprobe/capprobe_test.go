package capprobe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/execx"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

type fakeExec struct {
	calls atomic.Int32
	out   map[string]string
}

func (f *fakeExec) Run(_ context.Context, c ports.Cmd) (ports.ExecResult, error) {
	f.calls.Add(1)
	key := filepath.Base(c.Name) + " " + strings.Join(c.Args, " ")
	if v, ok := f.out[key]; ok {
		return ports.ExecResult{Stdout: []byte(v)}, nil
	}
	return ports.ExecResult{ExitCode: 1, Stderr: []byte("unknown")}, nil
}

func TestProbe(t *testing.T) {
	data := t.TempDir()
	bin := filepath.Join(data, "tools", "bin")
	_ = os.MkdirAll(filepath.Join(data, "accounts", "claude-1"), 0o700)
	_ = os.WriteFile(filepath.Join(data, "accounts", "claude-1", ".credentials.json"), []byte("{}"), 0o600)
	_ = os.MkdirAll(filepath.Join(data, "accounts", "codex-1"), 0o700) // empty: not logged in
	_ = os.MkdirAll(filepath.Join(data, "accounts", "nobody"), 0o700)
	_ = os.WriteFile(filepath.Join(data, "accounts", "stray-1"), nil, 0o600)
	_ = os.MkdirAll(filepath.Join(data, "egress"), 0o700)
	_ = os.WriteFile(filepath.Join(data, "egress", "ca.pem"), []byte("x"), 0o600)
	paths := map[string]string{"git": "/usr/bin/git", "docker": "/usr/bin/docker", "bwrap": "/usr/bin/bwrap", "rtk": filepath.Join(bin, "rtk")}
	ex := &fakeExec{out: map[string]string{
		"git --version": "git version 2.43.0", "docker --version": "Docker version 27.1.1, build x",
		"docker compose version": "Docker Compose version v2.29.1", "rtk --version": "rtk 1.2.1",
	}}
	now := time.Unix(1700000000, 0)
	p := New(Options{Exec: ex, DataDir: data, ToolsBin: bin, GOOS: "linux", GOARCH: "arm64", Now: func() time.Time { return now },
		LookPath: func(n string) (string, error) {
			if v, ok := paths[n]; ok {
				return v, nil
			}
			return "", errors.New("not found")
		},
		Landlock: func() domain.SandboxSupport { return domain.SandboxSupport{Available: true, ABI: 4} },
		Bwrap:    func() bool { return false },
		KVM:      func() (bool, bool) { return true, false },
		Reflink:  func(string) (bool, error) { return false, nil },
	})
	m := p.Probe(context.Background(), ports.ProbeRequest{Programs: []string{"git", "docker", "rtk", "bwrap", "mergiraf"}})
	if m.Platform() != "linux/arm64" || !m.ProbedAt.Equal(now) {
		t.Fatalf("header %+v", m)
	}
	if g := m.Tool("git"); !g.Found || g.Version != "2.43.0" || g.Managed {
		t.Fatalf("git %+v", g)
	}
	if r := m.Tool("rtk"); !r.Managed || r.Version != "1.2.1" {
		t.Fatalf("rtk %+v", r)
	}
	if b := m.Tool("bwrap"); !b.Found || b.Err == "" {
		t.Fatalf("bwrap's failed --version is kept: %+v", b)
	}
	if m.Tool("mergiraf").Found {
		t.Fatal("mergiraf")
	}
	if m.Compose != "2.29.1" || m.LandlockABI != 4 || m.BwrapUsable || !strings.Contains(m.BwrapReason, "user namespaces") ||
		!m.KVMPresent || m.KVMAccess || m.Reflink || m.ReflinkReason == "" || !m.EgressCA {
		t.Fatalf("probe %+v", m)
	}
	if !m.Logins["claude"] || m.Logins["codex"] || len(m.Logins) != 1 {
		t.Fatalf("logins %v", m.Logins)
	}
	calls := ex.calls.Load()
	// Cached: the same programs (or fewer) cost nothing.
	p.Probe(context.Background(), ports.ProbeRequest{Programs: []string{"git"}})
	if ex.calls.Load() != calls {
		t.Fatal("cache not used")
	}
	// A program it never looked for probes again.
	p.Probe(context.Background(), ports.ProbeRequest{Programs: []string{"git", "goose"}})
	if ex.calls.Load() == calls {
		t.Fatal("new program not probed")
	}
	calls = ex.calls.Load()
	now = now.Add(DefaultTTL + time.Second)
	p.Probe(context.Background(), ports.ProbeRequest{Programs: []string{"git"}})
	if ex.calls.Load() == calls {
		t.Fatal("stale cache used")
	}
	calls = ex.calls.Load()
	p.Invalidate()
	p.Probe(context.Background(), ports.ProbeRequest{Programs: []string{"git"}})
	if ex.calls.Load() == calls {
		t.Fatal("invalidated cache used")
	}
	calls = ex.calls.Load()
	p.Probe(context.Background(), ports.ProbeRequest{Programs: []string{"git"}, Refresh: true})
	if ex.calls.Load() == calls {
		t.Fatal("refresh ignored")
	}
}

func TestProbeOtherSystemsAndErrors(t *testing.T) {
	p := New(Options{GOOS: "darwin", GOARCH: "arm64", DataDir: t.TempDir(),
		LookPath: func(string) (string, error) { return "", errors.New("no") },
		Landlock: func() domain.SandboxSupport { return domain.SandboxSupport{Reason: "not linux", ABI: 3} },
		Reflink:  func(string) (bool, error) { return false, errors.New("disk on fire") },
	})
	m := p.Probe(context.Background(), ports.ProbeRequest{})
	if m.LandlockABI != 0 || m.LandlockReason != "not linux" || !m.KVMAccess || m.BwrapReason != "" {
		t.Fatalf("darwin %+v", m)
	}
	if !strings.Contains(m.ReflinkReason, "disk on fire") {
		t.Fatalf("reflink %q", m.ReflinkReason)
	}
	if len(m.Tools) != len(domain.ProbedTools) {
		t.Fatalf("the catalog's programs by default: %d", len(m.Tools))
	}
	lin := New(Options{GOOS: "linux", GOARCH: "amd64", LookPath: func(string) (string, error) { return "", errors.New("no") }})
	if m := lin.Probe(context.Background(), ports.ProbeRequest{Programs: []string{"x"}}); m.BwrapReason != "bubblewrap is not installed" {
		t.Fatalf("no bwrap %+v", m)
	}
}

// TestProbeRealMachine runs the real probe with the real runner: it must
// answer quickly and never fail, whatever this machine has.
func TestProbeRealMachine(t *testing.T) {
	if testing.Short() {
		t.Skip("probes the machine")
	}
	p := New(Options{Exec: execx.New(), DataDir: t.TempDir()})
	start := time.Now()
	m := p.Probe(context.Background(), ports.ProbeRequest{})
	if m.OS == "" || time.Since(start) > 30*time.Second {
		t.Fatalf("probe %+v in %s", m, time.Since(start))
	}
	if g := m.Tool("git"); g.Found && g.Version == "" {
		t.Fatalf("git without a version: %+v", g)
	}
	if present, _ := kvm(); present && m.OS == "linux" && !m.KVMPresent {
		t.Fatal("kvm")
	}
}
