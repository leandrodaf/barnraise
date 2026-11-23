// Package claude is the provider adapter for the Claude Code CLI running in
// headless mode (`claude -p --output-format stream-json`) with a subscription
// login. Capabilities are detected from the installed CLI.
package claude

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/provcommon"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Options configure the adapter.
type Options struct {
	// Binary is the CLI to run (default "claude").
	Binary string
	// Command builds every exec.Cmd; tests swap it for a fake binary.
	Command provcommon.CommandFunc
	// ExtraArgs are appended to every session command line.
	ExtraArgs []string
	// ExtraEnvKeys are inherited from the daemon environment on top of the minimal set.
	ExtraEnvKeys []string
	// Grace is the SIGINT to SIGKILL delay of Stop (default 5s).
	Grace time.Duration
	// DetectTimeout bounds each detection command (default 30s).
	DetectTimeout time.Duration
	// Now is used to resolve relative limit reset times (default time.Now).
	Now func() time.Time
}

// Provider implements ports.Provider for Claude Code.
type Provider struct {
	opts Options

	mu    sync.Mutex
	caps  *ports.Capabilities
	flags map[string]bool
}

// New returns a Claude Code adapter.
func New(opts Options) *Provider {
	if opts.Binary == "" {
		opts.Binary = "claude"
	}
	if opts.Command == nil {
		opts.Command = provcommon.DefaultCommand
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Provider{opts: opts}
}

// Name implements ports.Provider.
func (p *Provider) Name() string { return "claude" }

// Family implements ports.Provider.
func (p *Provider) Family() string { return "anthropic" }

var reFlag = regexp.MustCompile(`--[a-z][a-z0-9-]*`)

// Detect runs `claude --version`, `claude --help` and `claude agents --help`
// once and derives the capabilities from what the installed CLI offers.
// A successful result is cached; failures are not.
func (p *Provider) Detect(ctx context.Context) (ports.Capabilities, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.caps != nil {
		return *p.caps, nil
	}
	out, err := provcommon.Output(ctx, p.opts.Command, p.opts.DetectTimeout, p.opts.Binary, "--version")
	if err != nil {
		return ports.Capabilities{}, fmt.Errorf("claude: detect version: %w", err)
	}
	ver, verText, ok := provcommon.ParseVersion(out)
	if !ok {
		return ports.Capabilities{}, fmt.Errorf("claude: no version in %q", strings.TrimSpace(out))
	}
	help, err := provcommon.Output(ctx, p.opts.Command, p.opts.DetectTimeout, p.opts.Binary, "--help")
	if err != nil {
		return ports.Capabilities{}, fmt.Errorf("claude: detect help: %w", err)
	}
	// The agents subcommand is optional; a failure only means no session state.
	agentsHelp, _ := provcommon.Output(ctx, p.opts.Command, p.opts.DetectTimeout, p.opts.Binary, "agents", "--help")

	caps, flags := capabilitiesFrom(ver, verText, help, agentsHelp)
	p.caps, p.flags = &caps, flags
	return caps, nil
}

// Flags returns the long flags seen in `claude --help` (after Detect).
func (p *Provider) Flags() map[string]bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]bool, len(p.flags))
	maps.Copy(out, p.flags)
	return out
}

func capabilitiesFrom(ver [3]int, verText, help, agentsHelp string) (ports.Capabilities, map[string]bool) {
	flags := map[string]bool{}
	for _, f := range reFlag.FindAllString(help, -1) {
		flags[f] = true
	}
	lower := strings.ToLower(help)
	hasCommand := func(name string) bool {
		return regexp.MustCompile(`(?m)^\s+` + regexp.QuoteMeta(name) + `\b`).MatchString(commandsSection(help))
	}
	c := ports.Capabilities{Version: verText}
	c.Headless = flags["--print"]
	c.Stream = c.Headless && strings.Contains(help, "stream-json")
	c.Resume = flags["--resume"]
	c.Fork = flags["--fork-session"]
	// Settings hooks (PreToolUse… with permissionDecision/exit 2) exist in every
	// CLI that takes --settings; --include-hook-events confirms them in the stream.
	c.Hooks = flags["--settings"] && (flags["--include-hook-events"] || provcommon.AtLeast(ver, 1, 0, 38))
	c.HookBlock = c.Hooks
	c.Subagents = flags["--agents"] || flags["--agent"]
	c.Skills = strings.Contains(lower, "skill")
	c.MCP = flags["--mcp-config"]
	// The native sandbox (bubblewrap on Linux) is set through settings, not a flag.
	c.Sandbox = flags["--settings"] && provcommon.AtLeast(ver, 2, 0, 24)
	// Sending a message into a running session needs stream-json input.
	c.Messaging = false
	c.SessionState = hasCommand("agents") && strings.Contains(agentsHelp, "--json")
	// rate_limit_event in the stream and rate_limits in the status line.
	c.UsageLimits = c.Stream && provcommon.AtLeast(ver, 2, 1, 80)
	c.ModelSwitch = flags["--model"] && provcommon.AtLeast(ver, 2, 0, 0)
	c.JSONSchema = flags["--json-schema"]
	c.Images = true // the Read tool reads images in every version
	return c, flags
}

// commandsSection returns the part of the help after "Commands:".
func commandsSection(help string) string {
	if i := strings.Index(help, "\nCommands:"); i >= 0 {
		return help[i:]
	}
	return ""
}

// flagKnown reports whether a flag may be used: true when detection did not
// run yet (the CLI will complain if it is missing) or when the help lists it.
func (p *Provider) flagKnown(flag string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.flags == nil {
		return true
	}
	return p.flags[flag]
}

// Args builds the command line of a session (without the binary).
func (p *Provider) Args(spec ports.SessionSpec) []string {
	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.Effort != "" && p.flagKnown("--effort") {
		args = append(args, "--effort", spec.Effort)
	}
	if spec.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", spec.SystemPrompt)
	}
	if spec.MCPConfig != "" {
		args = append(args, "--mcp-config", spec.MCPConfig)
		if p.flagKnown("--strict-mcp-config") {
			// Only Barnraise's MCP server: the account owner's own servers
			// (mail, ads, cloud…) are not the agent's to use, and their tool
			// lists cost tokens in every session.
			args = append(args, "--strict-mcp-config")
		}
	}
	if spec.SettingsFile != "" {
		args = append(args, "--settings", spec.SettingsFile)
		if p.flagKnown("--setting-sources") {
			// The session's settings are the ones Barnraise wrote plus the
			// project's; the account owner's personal settings, plugins and
			// hooks stay out.
			args = append(args, "--setting-sources", "project,local")
		}
	}
	if spec.ResumeID != "" {
		args = append(args, "--resume", spec.ResumeID)
	}
	if spec.MaxTurns > 0 {
		// --max-turns is accepted by -p although `claude --help` does not list it.
		args = append(args, "--max-turns", strconv.Itoa(spec.MaxTurns))
	}
	if spec.JSONSchema != "" && p.flagKnown("--json-schema") {
		args = append(args, "--json-schema", spec.JSONSchema)
	}
	return append(args, p.opts.ExtraArgs...)
}

// Env builds the environment of a session.
func (p *Provider) Env(spec ports.SessionSpec) *provcommon.Env {
	env := provcommon.MinimalEnv(p.opts.ExtraEnvKeys...)
	env.Merge(spec.Env)
	if spec.Account.ConfigDir != "" {
		env.Set("CLAUDE_CONFIG_DIR", spec.Account.ConfigDir)
	}
	env.Set("DISABLE_AUTOUPDATER", "1")
	return env
}

// Open starts a headless session. The prompt goes through stdin, so its size
// is not bound by the argument length limit.
func (p *Provider) Open(ctx context.Context, spec ports.SessionSpec) (ports.Session, error) {
	return p.OpenSession(ctx, spec)
}

// OpenSession is Open returning the concrete session (pid, stderr tail).
func (p *Provider) OpenSession(ctx context.Context, spec ports.SessionSpec) (*provcommon.Session, error) {
	if spec.Workdir == "" {
		return nil, fmt.Errorf("claude: workdir is required")
	}
	name, args := p.opts.Binary, p.Args(spec)
	env := p.Env(spec)
	if spec.RunAsUser != "" {
		name, args = provcommon.SudoArgs(spec.RunAsUser, env, name, args)
	}
	cmd := p.opts.Command(ctx, name, args...)
	cmd.Dir = spec.Workdir
	cmd.Env = append(cmd.Env, env.List()...)

	parser := NewParser(domain.TokenUsage{
		Agent:    spec.Agent,
		Account:  spec.Account.ID,
		Provider: "claude",
		Model:    spec.Model,
		Role:     spec.Role,
	})
	parser.Now = p.opts.Now
	return provcommon.Start(ctx, cmd, parser, provcommon.StartOptions{
		Name:    "claude",
		Timeout: spec.Timeout,
		Grace:   p.opts.Grace,
		Stdin:   strings.NewReader(spec.Prompt),
	})
}

var _ ports.Provider = (*Provider)(nil)
