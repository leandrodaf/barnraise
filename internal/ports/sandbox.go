package ports

import "github.com/leandrodaf/barnraise/internal/domain"

// Confiner runs commands inside an OS layer that Barnraise applies itself
// (Landlock on Linux, a microVM): the whole process tree of the command is
// confined, whatever it runs.
type Confiner interface {
	// Support says whether this machine can confine commands, and how well.
	Support() domain.SandboxSupport
	// Wrap returns the program and arguments that run name with args
	// confined by c. The caller starts them as it would have started name.
	Wrap(c domain.Confinement, name string, args []string) (string, []string, error)
}

// Confine wraps an agent session's command line: the session runs confined
// by Barnraise instead of (or on top of) the provider's own sandbox.
type Confine struct {
	// Backend is the layer: landlock or microvm. With it set, a provider
	// whose own sandbox cannot start here turns that sandbox off: the
	// wrapper is the boundary.
	Backend string `json:"backend"`
	// Wrap turns the CLI's program and arguments into the confined ones.
	Wrap func(name string, args []string) (string, []string, error) `json:"-"`
}

// Apply wraps name and args; a nil Confine leaves them as they are.
func (c *Confine) Apply(name string, args []string) (string, []string, error) {
	if c == nil || c.Wrap == nil {
		return name, args, nil
	}
	return c.Wrap(name, args)
}
