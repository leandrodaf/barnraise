// Package gemini is the provider adapter for the Google Gemini CLI running
// headless with `--output-format stream-json` and a Google account login.
//
// The Gemini CLI has no blocking hook that Barnraise installs, so the
// boundary is set by what the session may do:
//
//   - On the machine itself the session is a reader: the shell, write and
//     edit tools are excluded in the session's settings and the approval mode
//     stays "default", which in headless mode refuses any tool that would ask.
//     Reading the worktree, searching and Barnraise's MCP tools still work,
//     which is what review and long-context reading need.
//   - Inside a container (`container:` in barnraise.yaml) the container is
//     the boundary, and the session runs with `--approval-mode yolo` so it
//     can edit and run commands.
package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/provcommon"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// HostApproval is the approval mode of every session on the machine itself.
const HostApproval = "default"

// ContainerApproval is the mode inside a container, where the container is the boundary.
const ContainerApproval = "yolo"

// HostExcludedTools are the Gemini CLI tools a session on the machine itself never gets.
var HostExcludedTools = []string{"run_shell_command", "write_file", "replace"}

func approvalFor(spec ports.SessionSpec) string {
	if spec.Container != nil {
		return ContainerApproval
	}
	return HostApproval
}

// Options configure the adapter.
type Options struct {
	// Binary is the CLI to run (default "gemini").
	Binary string
	// Command builds every exec.Cmd; tests swap it for a fake binary.
	Command provcommon.CommandFunc
	// ExtraArgs are appended to every session command line.
	ExtraArgs []string
	// ExtraEnvKeys are inherited from the daemon environment on top of the
	// minimal set and GOOGLE_CLOUD_PROJECT.
	ExtraEnvKeys []string
	// Grace is the SIGINT to SIGKILL delay of Stop (default 5s).
	Grace time.Duration
	// DetectTimeout bounds each detection command (default 30s).
	DetectTimeout time.Duration
	// Now is used to resolve relative limit reset times (default time.Now).
	Now func() time.Time
}

// Provider implements ports.Provider for the Gemini CLI.
type Provider struct {
	opts Options

	mu   sync.Mutex
	caps *ports.Capabilities
}

// New returns a Gemini adapter.
func New(opts Options) *Provider {
	if opts.Binary == "" {
		opts.Binary = "gemini"
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
func (p *Provider) Name() string { return "gemini" }

// Family implements ports.Provider.
func (p *Provider) Family() string { return "google" }

// Detect runs `gemini --version` and `gemini --help` once and derives the
// capabilities. A successful result is cached; failures are not.
func (p *Provider) Detect(ctx context.Context) (ports.Capabilities, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.caps != nil {
		return *p.caps, nil
	}
	run := func(args ...string) (string, error) {
		return provcommon.Output(ctx, p.opts.Command, p.opts.DetectTimeout, p.opts.Binary, args...)
	}
	out, err := run("--version")
	if err != nil {
		return ports.Capabilities{}, fmt.Errorf("gemini: detect version: %w", err)
	}
	_, verText, ok := provcommon.ParseVersion(out)
	if !ok {
		return ports.Capabilities{}, fmt.Errorf("gemini: no version in %q", strings.TrimSpace(out))
	}
	help, err := run("--help")
	if err != nil {
		return ports.Capabilities{}, fmt.Errorf("gemini: detect help: %w", err)
	}
	caps := capabilitiesFrom(verText, help)
	p.caps = &caps
	return caps, nil
}

func capabilitiesFrom(verText, help string) ports.Capabilities {
	commands := commandNames(help)
	has := func(flag string) bool {
		return regexp.MustCompile(`(^|[\s,])` + regexp.QuoteMeta(flag) + `\b`).MatchString(help)
	}
	c := ports.Capabilities{Version: verText}
	c.Headless = has("--prompt") || has("--output-format")
	c.Stream = c.Headless && strings.Contains(help, "stream-json")
	c.Resume = has("--resume")
	c.Fork = false
	// The CLI's own hooks are not wired to Barnraise's policy: no hook, no
	// blocking. The boundary is the excluded tools on the machine and the
	// container otherwise (see the package comment).
	c.Hooks = false
	c.HookBlock = false
	c.Subagents = false
	c.Skills = commands["skills"]
	c.MCP = commands["mcp"]
	// Gemini's own sandbox (Docker, Podman or Seatbelt) is not used by Barnraise.
	c.Sandbox = false
	c.Messaging = false
	c.SessionState = false
	c.UsageLimits = false
	c.ModelSwitch = false
	c.JSONSchema = false
	c.Images = false
	return c
}

// reCommand matches the subcommands of a yargs help text ("  gemini mcp  Manage MCP servers").
var reCommand = regexp.MustCompile(`(?m)^\s+gemini ([a-z][a-z0-9-]*)\b`)

// commandNames lists the subcommands of a yargs help text.
func commandNames(help string) map[string]bool {
	out := map[string]bool{}
	i := strings.Index(help, "Commands:")
	if i < 0 {
		return out
	}
	sec := help[i:]
	if j := strings.Index(sec, "\n\n"); j >= 0 {
		sec = sec[:j]
	}
	for _, m := range reCommand.FindAllStringSubmatch(sec, -1) {
		out[m[1]] = true
	}
	return out
}

type mcpServer struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// mcpServers turns a Claude-style MCP config (file path or inline JSON with
// "mcpServers") into the Gemini CLI's settings shape. Every server is
// trusted: the owner allowed it for the role, and a headless session cannot
// answer a confirmation.
func mcpServers(cfg string) (map[string]map[string]any, []string, error) {
	data := []byte(strings.TrimSpace(cfg))
	if len(data) == 0 || data[0] != '{' {
		b, err := os.ReadFile(cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("gemini: mcp config: %w", err)
		}
		data = b
	}
	var doc struct {
		MCPServers map[string]mcpServer `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("gemini: mcp config: %w", err)
	}
	out := map[string]map[string]any{}
	names := make([]string, 0, len(doc.MCPServers))
	for n, s := range doc.MCPServers {
		names = append(names, n)
		srv := map[string]any{"trust": true}
		switch {
		case s.URL != "" && s.Type == "sse":
			srv["url"] = s.URL
			if len(s.Headers) > 0 {
				srv["headers"] = s.Headers
			}
		case s.URL != "":
			srv["httpUrl"] = s.URL
			if len(s.Headers) > 0 {
				srv["headers"] = s.Headers
			}
		default:
			srv["command"] = s.Command
			if len(s.Args) > 0 {
				srv["args"] = s.Args
			}
			if len(s.Env) > 0 {
				srv["env"] = s.Env
			}
		}
		out[n] = srv
	}
	sort.Strings(names)
	return out, names, nil
}

// Settings builds the session's settings document, loaded by the CLI as its
// system settings (GEMINI_CLI_SYSTEM_SETTINGS_PATH), which take precedence
// over the account's own. It also returns the MCP server names to allow.
func (p *Provider) Settings(spec ports.SessionSpec) ([]byte, []string, error) {
	doc := map[string]any{}
	var names []string
	if spec.MCPConfig != "" {
		servers, n, err := mcpServers(spec.MCPConfig)
		if err != nil {
			return nil, nil, err
		}
		doc["mcpServers"] = servers
		names = n
	}
	if spec.Container == nil {
		doc["tools"] = map[string]any{"exclude": HostExcludedTools}
	}
	if spec.MaxTurns > 0 {
		doc["model"] = map[string]any{"maxSessionTurns": spec.MaxTurns}
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	return b, names, err
}

// Args builds the command line of a session (without the binary). The prompt
// is read from stdin: piped input makes the CLI run headless. Effort,
// SettingsFile and JSONSchema have no Gemini equivalent and are ignored.
func (p *Provider) Args(spec ports.SessionSpec, mcpNames []string) []string {
	args := []string{"--output-format", "stream-json", "--approval-mode", approvalFor(spec), "--extensions", "none"}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if len(mcpNames) > 0 {
		args = append(args, "--allowed-mcp-server-names", strings.Join(mcpNames, ","))
	}
	if spec.ResumeID != "" {
		args = append(args, "--resume", spec.ResumeID)
	}
	return append(args, p.opts.ExtraArgs...)
}

// Prompt is the text sent on stdin. The CLI has no flag that adds to its
// system prompt (GEMINI_SYSTEM_MD replaces it whole), so Barnraise's system
// prompt goes first, in its own section.
func Prompt(spec ports.SessionSpec) string {
	if strings.TrimSpace(spec.SystemPrompt) == "" {
		return spec.Prompt
	}
	return "<system>\n" + spec.SystemPrompt + "\n</system>\n\n" + spec.Prompt
}

// Env builds the environment of a session.
func (p *Provider) Env(spec ports.SessionSpec) *provcommon.Env {
	env := provcommon.MinimalEnv(append([]string{"GOOGLE_CLOUD_PROJECT"}, p.opts.ExtraEnvKeys...)...)
	env.Merge(spec.Env)
	if spec.Account.ConfigDir != "" {
		// The CLI keeps its login and settings in $GEMINI_CLI_HOME/.gemini.
		env.Set("GEMINI_CLI_HOME", spec.Account.ConfigDir)
	}
	return env
}

// Open starts `gemini --output-format stream-json`; the prompt goes through stdin.
func (p *Provider) Open(ctx context.Context, spec ports.SessionSpec) (ports.Session, error) {
	return p.OpenSession(ctx, spec)
}

// settingsPath picks where the session's settings go: next to the MCP config
// Barnraise wrote for the session (inside the worktree, so a container sees
// it at the same path), or a temporary folder otherwise.
func settingsPath(spec ports.SessionSpec) (string, func(), error) {
	if c := strings.TrimSpace(spec.MCPConfig); c != "" && !strings.HasPrefix(c, "{") {
		path := filepath.Join(filepath.Dir(c), "gemini-settings.json")
		return path, func() { _ = os.Remove(path) }, nil
	}
	dir, err := os.MkdirTemp("", "barnraise-gemini-")
	if err != nil {
		return "", nil, err
	}
	return filepath.Join(dir, "settings.json"), func() { _ = os.RemoveAll(dir) }, nil
}

// OpenSession is Open returning the concrete session.
func (p *Provider) OpenSession(ctx context.Context, spec ports.SessionSpec) (*provcommon.Session, error) {
	if spec.Workdir == "" {
		return nil, fmt.Errorf("gemini: workdir is required")
	}
	settings, names, err := p.Settings(spec)
	if err != nil {
		return nil, err
	}
	path, cleanup, err := settingsPath(spec)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, settings, 0o600); err != nil {
		cleanup()
		return nil, err
	}
	args := p.Args(spec, names)
	name := p.opts.Binary
	env := p.Env(spec)
	env.Set("GEMINI_CLI_SYSTEM_SETTINGS_PATH", path)
	if spec.Container != nil {
		name, args = provcommon.ContainerArgs(spec.Container, env, spec.Workdir, name, args)
	} else {
		var err error
		if name, args, err = provcommon.HostArgs(spec, env, name, args); err != nil {
			cleanup()
			return nil, err
		}
	}
	cmd := p.opts.Command(ctx, name, args...)
	cmd.Dir = spec.Workdir
	cmd.Env = append(cmd.Env, env.List()...)

	parser := NewParser(domain.TokenUsage{
		Agent:    spec.Agent,
		Account:  spec.Account.ID,
		Provider: "gemini",
		Model:    spec.Model,
		Role:     spec.Role,
	})
	parser.Now = p.opts.Now
	return provcommon.Start(ctx, cmd, parser, provcommon.StartOptions{
		Name:    "gemini",
		Timeout: spec.Timeout,
		Grace:   p.opts.Grace,
		Stdin:   strings.NewReader(Prompt(spec)),
		OnExit:  cleanup,
	})
}

var _ ports.Provider = (*Provider)(nil)
