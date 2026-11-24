// Package microvm runs a confined command inside a microVM through an
// external runner (smolvm by default, or any program the owner configures
// with a command template). It is opt-in: daemon.yaml turns it on, and a
// machine without the runner, or without hardware virtualization, reports
// it unavailable and keeps using Landlock. Barnraise does not ship the
// runner; `barnraise tools install smolvm` downloads its official build
// on the owner's request (ADR-0045).
package microvm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Backend is the name of this layer.
const Backend = "microvm"

// DefaultRunner is the runner looked up on the PATH.
const DefaultRunner = "smolvm"

// DefaultTemplate is the command line for smolvm, from its documentation
// (`smolvm machine run --net --image alpine -- uname -a`, `--volume
// host:guest`, `--allow-host`, `--cpus`, `--mem`). Placeholders:
//
//	{runner}   the runner program
//	{image}    the image
//	{cpus}     "--cpus N" (nothing when not set)
//	{memory}   "--mem N" in MiB (nothing when not set)
//	{volumes}  "--volume path:path" per writable path, "--volume path:path:ro" per read-only one
//	{network}  nothing (no network: loopback inside the VM only), "--net", or one "--allow-host h" per host
//	{env}      "--env NAME" per variable the command needs (values in the runner's environment)
//	{workdir}  the folder the command runs in
//	{command}  the command and its arguments
//
// A placeholder that is a whole argument expands to zero or more
// arguments; inside a longer argument it is replaced as text.
var DefaultTemplate = []string{"{runner}", "machine", "run", "--image", "{image}", "{cpus}", "{memory}", "{volumes}", "{network}", "{env}",
	"--", "sh", "-c", `cd "$1" && shift && exec "$@"`, "barnraise-microvm", "{workdir}", "{command}"}

// Options configures the runner.
type Options struct {
	Runner   string   // program (default smolvm)
	Template []string // command template (default DefaultTemplate)
	Image    string   // OCI image with the project's toolchain (and the AI CLI for sessions)
	CPUs     int
	MemoryMB int
	// Mounts are more host paths given to every VM ("path" or "path:ro").
	Mounts []string
	// LookPath and KVM are replaceable in tests.
	LookPath func(string) (string, error)
	KVM      func() bool
	// Version runs the runner to read its version (default: `<runner> --version`).
	Version func(ctx context.Context, runner string) (string, error)
}

// Confiner implements ports.Confiner with a microVM runner.
type Confiner struct {
	o       Options
	once    sync.Once
	support domain.SandboxSupport
	path    string
}

var _ ports.Confiner = (*Confiner)(nil)

// New returns the microVM confiner.
func New(o Options) *Confiner {
	if o.Runner == "" {
		o.Runner = DefaultRunner
	}
	if len(o.Template) == 0 {
		o.Template = DefaultTemplate
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.KVM == nil {
		o.KVM = hasKVM
	}
	if o.Version == nil {
		o.Version = runVersion
	}
	return &Confiner{o: o}
}

func hasKVM() bool {
	if goruntime.GOOS != "linux" {
		return true // macOS uses Hypervisor.framework; the runner says if it cannot start
	}
	_, err := os.Stat("/dev/kvm")
	return err == nil
}

func runVersion(ctx context.Context, runner string) (string, error) {
	out, err := exec.CommandContext(ctx, runner, "--version").Output()
	return strings.TrimSpace(string(out)), err
}

// Support looks the runner up once.
func (c *Confiner) Support() domain.SandboxSupport {
	c.once.Do(func() {
		s := domain.SandboxSupport{Backend: Backend}
		p, err := c.o.LookPath(c.o.Runner)
		switch {
		case err != nil:
			s.Reason = c.o.Runner + " is not installed (or not on the PATH)"
		case !c.o.KVM():
			s.Reason = "no hardware virtualization: /dev/kvm is missing"
		case c.o.Image == "" && usesImage(c.o.Template):
			s.Reason = "isolation.microvm.image is not set"
		default:
			c.path = p
			s.Available = true
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if v, err := c.o.Version(ctx, p); err == nil && v != "" {
				s.Reason = firstLine(v)
			}
		}
		c.support = s
	})
	return c.support
}

func usesImage(tpl []string) bool {
	for _, a := range tpl {
		if strings.Contains(a, "{image}") {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

// Wrap expands the template for one command.
func (c *Confiner) Wrap(conf domain.Confinement, name string, args []string) (string, []string, error) {
	if s := c.Support(); !s.Available {
		return "", nil, fmt.Errorf("microvm: %s", s.String())
	}
	if len(conf.Work) == 0 {
		return "", nil, errors.New("microvm: the command has no folder to work in")
	}
	argv := Expand(c.o.Template, c.values(conf, name, args))
	if len(argv) == 0 {
		return "", nil, errors.New("microvm: the command template is empty")
	}
	if rule := conf.DeniedCommand(def(conf.Command, strings.Join(append([]string{name}, args...), " "))); rule != "" {
		return "", nil, fmt.Errorf("command refused by the sandbox profile %s: %s", conf.Profile, rule)
	}
	return argv[0], argv[1:], nil
}

func def(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func (c *Confiner) values(conf domain.Confinement, name string, args []string) map[string][]string {
	v := map[string][]string{
		"{runner}":  {def(c.path, c.o.Runner)},
		"{image}":   {c.o.Image},
		"{workdir}": {conf.Work[0]},
		"{command}": append([]string{name}, args...),
	}
	if c.o.CPUs > 0 {
		v["{cpus}"] = []string{"--cpus", strconv.Itoa(c.o.CPUs)}
	}
	if c.o.MemoryMB > 0 {
		v["{memory}"] = []string{"--mem", strconv.Itoa(c.o.MemoryMB)}
	}
	var vols []string
	seen := map[string]bool{}
	vol := func(p string, ro bool) {
		p = filepath.Clean(p)
		if seen[p] {
			return
		}
		if _, err := os.Stat(p); err != nil {
			return
		}
		seen[p] = true
		spec := p + ":" + p
		if ro {
			spec += ":ro"
		}
		vols = append(vols, "--volume", spec)
	}
	for _, w := range conf.Work {
		vol(w, false)
	}
	// The project's shared stores (and other writable folders) reach the VM
	// at the same path; the guest keeps its own /tmp, /var/tmp and /dev.
	for _, p := range conf.ReadWrite {
		if guestOwned(p) {
			continue
		}
		vol(p, false)
	}
	// The tools Barnraise installed reach the VM read-only at the same path.
	for _, t := range conf.Tools {
		vol(t, true)
	}
	for _, m := range c.o.Mounts {
		p, ro := strings.CutSuffix(m, ":ro")
		vol(p, ro)
	}
	v["{volumes}"] = vols
	switch conf.Network {
	case domain.NetOpen, "":
		v["{network}"] = []string{"--net"}
	case domain.NetLocal:
		// Inside the VM loopback is its own; with no allowed host the VM
		// has no network at all, which is what a check asks for.
		v["{network}"] = hostFlags(conf.Hosts)
	default:
		if len(conf.Hosts) == 0 {
			v["{network}"] = []string{"--net"}
		} else {
			v["{network}"] = hostFlags(conf.Hosts)
		}
	}
	var env []string
	for _, k := range conf.Env {
		env = append(env, "--env", k)
	}
	v["{env}"] = env
	return v
}

func hostFlags(hosts []string) []string {
	var out []string
	for _, h := range hosts {
		out = append(out, "--allow-host", h)
	}
	return out
}

// Expand replaces the placeholders of a template.
func Expand(tpl []string, vals map[string][]string) []string {
	var out []string
	for _, a := range tpl {
		if v, ok := vals[a]; ok {
			out = append(out, v...)
			continue
		}
		if isPlaceholder(a) {
			continue // a known placeholder with nothing to say
		}
		for k, v := range vals {
			if strings.Contains(a, k) {
				a = strings.ReplaceAll(a, k, strings.Join(v, " "))
			}
		}
		out = append(out, a)
	}
	return out
}

var placeholders = []string{"{runner}", "{image}", "{cpus}", "{memory}", "{volumes}", "{network}", "{env}", "{workdir}", "{command}"}

func isPlaceholder(a string) bool {
	for _, p := range placeholders {
		if a == p {
			return true
		}
	}
	return false
}

// guestOwned reports whether p is a folder the VM's own system provides
// (its scratch space and devices), never mounted from the host.
func guestOwned(p string) bool {
	switch filepath.Clean(p) {
	case "/", "/tmp", "/var/tmp", "/dev", filepath.Clean(os.TempDir()):
		return true
	}
	return false
}
