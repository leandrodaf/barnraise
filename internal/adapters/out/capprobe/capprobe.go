// Package capprobe finds what this machine has for Barnraise's capability
// catalog (ADR-0045): the programs of the catalog and of the projects'
// configuration, with their versions (each `--version` under a timeout),
// the kernel's Landlock ABI, whether bubblewrap can start, access to
// /dev/kvm, copy-on-write clones on the data folder, the egress CA and the
// provider accounts logged in here. The answer is cached for a while and
// dropped after a tool is installed.
package capprobe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/acp"
	"github.com/leandrodaf/barnraise/internal/adapters/out/egress"
	"github.com/leandrodaf/barnraise/internal/adapters/out/landlock"
	"github.com/leandrodaf/barnraise/internal/adapters/out/reflink"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// DefaultTTL is how long an answer is reused.
const DefaultTTL = 5 * time.Minute

// VersionTimeout bounds each program's `--version`.
const VersionTimeout = 5 * time.Second

// Options configure the probe; every field has a default.
type Options struct {
	Exec     ports.Exec
	LookPath func(string) (string, error)
	// DataDir is Barnraise's data folder (worktrees, accounts, egress).
	DataDir string
	// ToolsBin is the folder of the tools Barnraise installed.
	ToolsBin string
	TTL      time.Duration
	Now      func() time.Time
	GOOS     string
	GOARCH   string
	Landlock func() domain.SandboxSupport
	Bwrap    func() bool
	// KVM reports whether /dev/kvm exists and whether this user opens it.
	KVM     func() (present, access bool)
	Reflink func(dir string) (bool, error)
}

// Prober implements ports.CapabilityProber.
type Prober struct {
	o      Options
	mu     sync.Mutex
	cached *domain.MachineProbe
}

var _ ports.CapabilityProber = (*Prober)(nil)

// New returns a probe.
func New(o Options) *Prober {
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.TTL <= 0 {
		o.TTL = DefaultTTL
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.GOOS == "" {
		o.GOOS, o.GOARCH = goruntime.GOOS, goruntime.GOARCH
	}
	if o.Landlock == nil {
		o.Landlock = func() domain.SandboxSupport { return landlock.New("").Support() }
	}
	if o.Bwrap == nil {
		o.Bwrap = acp.BubblewrapWorks
	}
	if o.KVM == nil {
		o.KVM = kvm
	}
	if o.Reflink == nil {
		o.Reflink = reflink.Supported
	}
	return &Prober{o: o}
}

func kvm() (bool, bool) {
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return false, false
	}
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return true, false
	}
	_ = f.Close()
	return true, true
}

// Invalidate drops the cached answer.
func (p *Prober) Invalidate() {
	p.mu.Lock()
	p.cached = nil
	p.mu.Unlock()
}

// Probe returns the machine's answer, from the cache while it is fresh and
// knows every program asked for.
func (p *Prober) Probe(ctx context.Context, r ports.ProbeRequest) domain.MachineProbe {
	progs := append([]string(nil), r.Programs...)
	if len(progs) == 0 {
		progs = domain.CapabilityPrograms(nil, domain.MachineSettings{})
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if c := p.cached; c != nil && !r.Refresh && p.o.Now().Sub(c.ProbedAt) < p.o.TTL && knowsAll(c, progs) {
		return *c
	}
	m := p.probe(ctx, progs)
	p.cached = &m
	return m
}

func knowsAll(m *domain.MachineProbe, progs []string) bool {
	for _, n := range progs {
		if _, ok := m.Tools[n]; !ok {
			return false
		}
	}
	return true
}

func (p *Prober) probe(ctx context.Context, progs []string) domain.MachineProbe {
	m := domain.MachineProbe{OS: p.o.GOOS, Arch: p.o.GOARCH, ProbedAt: p.o.Now().UTC(), Tools: map[string]domain.ToolProbe{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, name := range progs {
		wg.Go(func() {
			t := p.tool(ctx, name)
			mu.Lock()
			m.Tools[name] = t
			mu.Unlock()
		})
	}
	wg.Go(func() {
		ll := p.o.Landlock()
		mu.Lock()
		m.LandlockABI, m.LandlockReason = ll.ABI, ll.Reason
		if !ll.Available {
			m.LandlockABI = 0
		}
		mu.Unlock()
	})
	wg.Wait()
	if p.o.GOOS == "linux" {
		if m.Tools["bwrap"].Found || p.lookable("bwrap") {
			m.BwrapUsable = p.o.Bwrap()
			if !m.BwrapUsable {
				m.BwrapReason = "bubblewrap is installed, but it cannot start a sandbox here (unprivileged user namespaces are refused, often by AppArmor)"
			}
		} else {
			m.BwrapReason = "bubblewrap is not installed"
		}
		m.KVMPresent, m.KVMAccess = p.o.KVM()
	} else {
		m.KVMPresent, m.KVMAccess = true, true // macOS uses Hypervisor.framework
	}
	if d := m.Tools["docker"]; d.Found && p.o.Exec != nil {
		if out, err := p.run(ctx, d.Path, "compose", "version"); err == nil {
			m.Compose = def(domain.ParseToolVersion(out), "unknown")
		}
	}
	if p.o.DataDir != "" {
		ok, err := p.o.Reflink(filepath.Join(p.o.DataDir, "worktrees"))
		m.Reflink = ok
		switch {
		case err != nil:
			m.ReflinkReason = "The data folder could not be probed: " + err.Error()
		case !ok:
			m.ReflinkReason = "The filesystem of the data folder (" + p.o.DataDir + ") cannot clone files copy-on-write"
		}
		_, err = os.Stat(filepath.Join(p.o.DataDir, "egress", egress.CACertFile))
		m.EgressCA = err == nil
		m.Logins = logins(filepath.Join(p.o.DataDir, "accounts"))
	}
	return m
}

func (p *Prober) lookable(name string) bool {
	_, err := p.o.LookPath(name)
	return err == nil
}

// tool looks a program up and reads its version.
func (p *Prober) tool(ctx context.Context, name string) domain.ToolProbe {
	t := domain.ToolProbe{Name: name}
	path, err := p.o.LookPath(name)
	if err != nil {
		return t
	}
	t.Found, t.Path = true, path
	if p.o.ToolsBin != "" && filepath.Dir(path) == filepath.Clean(p.o.ToolsBin) {
		t.Managed = true
	}
	if p.o.Exec == nil {
		return t
	}
	out, err := p.run(ctx, path, domain.ProbedToolArgs(name)...)
	if err != nil {
		t.Err = err.Error()
	}
	t.Version = domain.ParseToolVersion(out)
	return t
}

func (p *Prober) run(ctx context.Context, name string, args ...string) (string, error) {
	res, err := p.o.Exec.Run(ctx, ports.Cmd{Name: name, Args: args, Env: os.Environ(), Timeout: VersionTimeout})
	out := string(res.Stdout) + "\n" + string(res.Stderr)
	if err == nil && res.ExitCode != 0 {
		return out, &exitError{code: res.ExitCode}
	}
	return out, err
}

type exitError struct{ code int }

func (e *exitError) Error() string { return "exit status " + strconv.Itoa(e.code) }

// logins are the providers with an account folder that holds something:
// `barnraise account add <provider>` keeps each login in accounts/<provider>-N.
func logins(dir string) map[string]bool {
	out := map[string]bool{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		i := strings.LastIndexByte(e.Name(), '-')
		if i <= 0 {
			continue
		}
		prov := e.Name()[:i]
		if !slices.Contains([]string{"claude", "codex", "gemini"}, prov) {
			continue
		}
		if inner, err := os.ReadDir(filepath.Join(dir, e.Name())); err == nil && len(inner) > 0 {
			out[prov] = true
		}
	}
	return out
}

func def(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
