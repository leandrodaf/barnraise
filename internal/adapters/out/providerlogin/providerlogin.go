// Package providerlogin runs the providers' own logins without a terminal:
// the login prints a link (and, for some, a one-time code) and may wait for
// the code the page shows, so the owner can follow it from another machine.
package providerlogin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// Runner implements ports.ProviderLogin with the CLIs on PATH.
type Runner struct {
	// Bin overrides a provider's program (tests).
	Bin map[string]string
	// Env is the environment the logins start from (default os.Environ).
	Env []string
}

// New returns a runner for the CLIs on PATH.
func New() *Runner { return &Runner{} }

func (r *Runner) bin(provider string) string {
	if b := r.Bin[provider]; b != "" {
		return b
	}
	return provider
}

// env is the login's environment: the account's folder, and no browser
// opened on the machine, since the owner may be at another one.
func (r *Runner) env(spec ports.LoginSpec) []string {
	base := r.Env
	if base == nil {
		base = os.Environ()
	}
	var out []string
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "DISPLAY", "WAYLAND_DISPLAY", "BROWSER", "CLAUDE_CONFIG_DIR", "CODEX_HOME":
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "BROWSER=true", "NO_COLOR=1")
	if spec.Dir != "" {
		switch spec.Provider {
		case "claude":
			out = append(out, "CLAUDE_CONFIG_DIR="+spec.Dir)
		case "codex":
			out = append(out, "CODEX_HOME="+spec.Dir)
		}
	}
	return out
}

func loginArgs(provider string) ([]string, error) {
	switch provider {
	case "claude":
		return []string{"auth", "login"}, nil
	case "codex":
		return []string{"login", "--device-auth"}, nil
	}
	return nil, fmt.Errorf("the login of %q cannot run without a terminal", provider)
}

// claudeInstaller is Claude Code's own installer: no root, no JavaScript runtime; it puts
// claude in ~/.local/bin.
const claudeInstaller = "https://claude.ai/install.sh"

// installThenLogin is the script that installs a provider's CLI and then
// runs its login, for the providers that have an installer of their own;
// "" for the others, and when a test names the program.
func installThenLogin(provider string, overridden bool) string {
	if overridden || provider != "claude" {
		return ""
	}
	if _, err := exec.LookPath("curl"); err != nil {
		return ""
	}
	if _, err := exec.LookPath("bash"); err != nil {
		return ""
	}
	return `echo "Installing Claude Code (` + claudeInstaller + `)…"; curl -fsSL --proto '=https' --tlsv1.2 ` + claudeInstaller + ` | bash || exit 1; ` +
		`c=$(command -v claude || echo "$HOME/.local/bin/claude"); exec "$c" auth login`
}

// Start implements ports.ProviderLogin.
func (r *Runner) Start(ctx context.Context, spec ports.LoginSpec) (ports.LoginProcess, error) {
	args, err := loginArgs(spec.Provider)
	if err != nil {
		return nil, err
	}
	var cmd *exec.Cmd
	if bin, err := exec.LookPath(r.bin(spec.Provider)); err == nil {
		cmd = exec.CommandContext(ctx, bin, args...)
	} else if script := installThenLogin(spec.Provider, r.Bin[spec.Provider] != ""); script != "" {
		// Not installed: the login installs it first, in the same stream,
		// so the owner sees it happen and the link follows.
		cmd = exec.CommandContext(ctx, "sh", "-c", script)
	} else {
		return nil, fmt.Errorf("%s is not installed on this machine", spec.Provider)
	}
	cmd.Env = r.env(spec)
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &process{cmd: cmd, stdin: stdin, out: make(chan string, 16), done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		_ = pw.Close()
	}()
	go func() {
		defer close(p.done)
		defer close(p.out)
		buf := make([]byte, 4096)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				p.out <- string(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return p, nil
}

type process struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   chan string
	done  chan struct{}
	err   error
	stop  sync.Once
}

func (p *process) Output() <-chan string { return p.out }

func (p *process) Input(line string) error {
	_, err := io.WriteString(p.stdin, line+"\n")
	return err
}

func (p *process) Wait() error {
	<-p.done
	return p.err
}

func (p *process) Stop() {
	p.stop.Do(func() {
		_ = p.stdin.Close()
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
	})
}

// LoggedIn implements ports.ProviderLogin: the CLI's own word on its login.
func (r *Runner) LoggedIn(ctx context.Context, spec ports.LoginSpec) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var args []string
	switch spec.Provider {
	case "claude":
		args = []string{"auth", "status", "--json"}
	case "codex":
		args = []string{"login", "status"}
	default:
		return false, fmt.Errorf("the login of %q cannot be checked", spec.Provider)
	}
	cmd := exec.CommandContext(ctx, r.bin(spec.Provider), args...)
	cmd.Env = r.env(spec)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return false, err
	}
	if spec.Provider == "claude" {
		var st struct {
			LoggedIn bool `json:"loggedIn"`
		}
		if i := bytes.IndexByte(out.Bytes(), '{'); i >= 0 && json.Unmarshal(out.Bytes()[i:], &st) == nil {
			return st.LoggedIn, nil
		}
		return false, nil
	}
	return err == nil && !strings.Contains(strings.ToLower(out.String()), "not logged in"), nil
}
