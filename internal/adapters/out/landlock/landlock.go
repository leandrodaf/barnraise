// Package landlock confines commands with Landlock, the Linux security
// module an unprivileged process can apply to itself: no root, no user
// namespaces (which AppArmor forbids on recent Ubuntu, and without which
// bubblewrap cannot start). The daemon does not confine itself: it wraps a
// command as `barnraise sandbox-exec --spec <json> -- <command…>`, and that
// small helper applies the rules to itself and then executes the command,
// which inherits them with every process it starts. Other systems build
// with a version that reports Landlock unavailable.
package landlock

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Backend is the name of this layer.
const Backend = "landlock"

// ExecCommand is the hidden subcommand that applies a confinement and runs
// the command.
const ExecCommand = "sandbox-exec"

// MinNetABI is the first Landlock ABI that restricts TCP bind and connect.
const MinNetABI = 4

// Confiner implements ports.Confiner with Landlock.
type Confiner struct {
	// Binary is the barnraise program that runs sandbox-exec.
	Binary string
	// probe reports the Landlock ABI of the running kernel (0: none).
	probe    func() (int, error)
	once     sync.Once
	support  domain.SandboxSupport
	lookPath func(string) (string, error)
}

var _ ports.Confiner = (*Confiner)(nil)

// New returns a confiner whose wrapper runs binary.
func New(binary string) *Confiner {
	return &Confiner{Binary: binary, probe: abiVersion, lookPath: exec.LookPath}
}

// Support probes the kernel once.
func (c *Confiner) Support() domain.SandboxSupport {
	c.once.Do(func() { c.support = supportFor(c.probe) })
	return c.support
}

func supportFor(probe func() (int, error)) domain.SandboxSupport {
	s := domain.SandboxSupport{Backend: Backend}
	abi, err := probe()
	switch {
	case err != nil:
		s.Reason = err.Error()
	case abi <= 0:
		s.Reason = "the kernel has no Landlock (it needs Linux 5.13 or later with landlock in the lsm= list)"
	default:
		s.Available, s.ABI, s.Network = true, abi, abi >= MinNetABI
	}
	return s
}

// Wrap returns `barnraise sandbox-exec --spec <json> -- name args…`. The
// folder of the program itself is added read-only, so a CLI installed in an
// unusual place still starts.
func (c *Confiner) Wrap(conf domain.Confinement, name string, args []string) (string, []string, error) {
	if s := c.Support(); !s.Available {
		return "", nil, fmt.Errorf("landlock: %s", s.String())
	}
	if c.Binary == "" {
		return "", nil, errors.New("landlock: the path of the barnraise binary is unknown")
	}
	conf.ReadOnly = append(append([]string(nil), conf.ReadOnly...), filepath.Dir(c.Binary))
	if p, err := c.lookPath(name); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			conf.ReadOnly = append(conf.ReadOnly, filepath.Dir(abs))
			if real, err := filepath.EvalSymlinks(abs); err == nil && real != abs {
				conf.ReadOnly = append(conf.ReadOnly, filepath.Dir(real))
			}
		}
	}
	if conf.Command == "" {
		conf.Command = strings.Join(append([]string{name}, args...), " ")
	}
	spec, err := json.Marshal(conf)
	if err != nil {
		return "", nil, err
	}
	out := []string{ExecCommand, "--spec", string(spec), "--", name}
	return c.Binary, append(out, args...), nil
}

// ErrDenied is returned by Exec when the command breaks a command rule.
var ErrDenied = errors.New("command refused by the sandbox profile")

// Exec applies spec to the current process and replaces it with the
// command. It returns only on failure: a denied command (ErrDenied), a
// kernel without Landlock, or a program that cannot start. It fails closed:
// it never runs the command unconfined.
func Exec(specJSON string, argv []string) error {
	var conf domain.Confinement
	if err := json.Unmarshal([]byte(specJSON), &conf); err != nil {
		return fmt.Errorf("sandbox-exec: bad --spec: %w", err)
	}
	if len(argv) == 0 {
		return errors.New("sandbox-exec: no command")
	}
	text := conf.Command
	if text == "" {
		text = strings.Join(argv, " ")
	}
	if rule := conf.DeniedCommand(text); rule != "" {
		return fmt.Errorf("%w %s: %s", ErrDenied, conf.Profile, rule)
	}
	prog, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("sandbox-exec: %w", err)
	}
	if err := Restrict(conf); err != nil {
		return fmt.Errorf("sandbox-exec: %w", err)
	}
	return execve(prog, argv, os.Environ())
}

// Restrict confines the current process (and every process it starts
// afterwards) to conf.
func Restrict(conf domain.Confinement) error {
	abi, err := abiVersion()
	if err != nil {
		return err
	}
	if abi <= 0 {
		return errors.New("the kernel has no Landlock")
	}
	return restrict(Plan(conf, EphemeralRange), abi)
}
