// Package codex is the provider adapter for the OpenAI Codex CLI running
// `codex exec --json` with a ChatGPT login. Commands always run in the
// workspace-write sandbox; the bypass flags are never used.
package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
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

// Sandbox is the only sandbox mode the adapter uses.
const Sandbox = "workspace-write"

// Options configure the adapter.
type Options struct {
	// Binary is the CLI to run (default "codex").
	Binary string
	// Command builds every exec.Cmd; tests swap it for a fake binary.
	Command provcommon.CommandFunc
	// ExtraArgs are appended to every session command line (before the prompt).
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

// Provider implements ports.Provider for Codex.
type Provider struct {
	opts Options

	mu       sync.Mutex
	caps     *ports.Capabilities
	features map[string]bool
}

// New returns a Codex adapter.
func New(opts Options) *Provider {
	if opts.Binary == "" {
		opts.Binary = "codex"
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
func (p *Provider) Name() string { return "codex" }

// Family implements ports.Provider.
func (p *Provider) Family() string { return "openai" }

// Detect runs `codex --version`, `codex --help`, `codex exec --help` and
// `codex features list` once and derives the capabilities. A successful
// result is cached; failures are not.
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
		return ports.Capabilities{}, fmt.Errorf("codex: detect version: %w", err)
	}
	_, verText, ok := provcommon.ParseVersion(out)
	if !ok {
		return ports.Capabilities{}, fmt.Errorf("codex: no version in %q", strings.TrimSpace(out))
	}
	help, err := run("--help")
	if err != nil {
		return ports.Capabilities{}, fmt.Errorf("codex: detect help: %w", err)
	}
	execHelp, err := run("exec", "--help")
	if err != nil {
		return ports.Capabilities{}, fmt.Errorf("codex: detect exec help: %w", err)
	}
	// Older CLIs have no feature list; that only turns optional capabilities off.
	featuresOut, _ := run("features", "list")

	caps, features := capabilitiesFrom(verText, help, execHelp, featuresOut)
	p.caps, p.features = &caps, features
	return caps, nil
}

// Features returns the enabled state of each feature flag seen by Detect.
func (p *Provider) Features() map[string]bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]bool, len(p.features))
	maps.Copy(out, p.features)
	return out
}

var reFeature = regexp.MustCompile(`(?m)^([a-z0-9_]+)\s+(stable|experimental|beta|under development|deprecated|removed)\s+(true|false)\s*$`)

func capabilitiesFrom(verText, help, execHelp, featuresOut string) (ports.Capabilities, map[string]bool) {
	features := map[string]bool{}
	for _, m := range reFeature.FindAllStringSubmatch(featuresOut, -1) {
		if m[2] == "removed" {
			continue
		}
		features[m[1]] = m[3] == "true"
	}
	commands := commandNames(help)
	execCommands := commandNames(execHelp)
	has := func(s, flag string) bool {
		return regexp.MustCompile(`(^|[\s,])` + regexp.QuoteMeta(flag) + `\b`).MatchString(s)
	}
	anyFeature := func(prefix string) bool {
		for k, v := range features {
			if v && strings.HasPrefix(k, prefix) {
				return true
			}
		}
		return false
	}

	c := ports.Capabilities{Version: verText}
	c.Headless = commands["exec"]
	c.Stream = c.Headless && has(execHelp, "--json")
	c.Resume = execCommands["resume"]
	c.Fork = execCommands["fork"]
	c.Hooks = features["hooks"]
	// Codex hooks can observe; blocking a tool call is not relied upon.
	c.HookBlock = false
	c.Subagents = features["multi_agent"] || features["multi_agent_v2"]
	c.Skills = anyFeature("skill")
	c.MCP = commands["mcp"]
	c.Sandbox = has(execHelp, "--sandbox") && strings.Contains(execHelp, Sandbox)
	c.Messaging = commands["queue"]
	c.SessionState = false
	c.UsageLimits = false
	c.ModelSwitch = false
	c.JSONSchema = has(execHelp, "--output-schema")
	c.Images = has(execHelp, "--image")
	return c, features
}

var reCommand = regexp.MustCompile(`(?m)^  ([a-z][a-z0-9-]*)\s`)

// commandNames lists the subcommands of a clap help text.
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

// tomlString quotes s as a TOML basic string (JSON escapes are valid TOML).
func tomlString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(buf.String(), "\n")
}

var reBareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func tomlKey(s string) string {
	if reBareKey.MatchString(s) {
		return s
	}
	return tomlString(s)
}

type mcpServer struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
}

// mcpOverrides turns a Claude-style MCP config (file path or inline JSON with
// "mcpServers") into codex `-c mcp_servers.<name>.…` overrides.
func mcpOverrides(cfg string) ([]string, error) {
	data := []byte(strings.TrimSpace(cfg))
	if len(data) == 0 || data[0] != '{' {
		b, err := os.ReadFile(cfg)
		if err != nil {
			return nil, fmt.Errorf("codex: mcp config: %w", err)
		}
		data = b
	}
	var doc struct {
		MCPServers map[string]mcpServer `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("codex: mcp config: %w", err)
	}
	names := make([]string, 0, len(doc.MCPServers))
	for n := range doc.MCPServers {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []string
	for _, n := range names {
		s := doc.MCPServers[n]
		key := "mcp_servers." + tomlKey(n)
		if s.URL != "" {
			out = append(out, "-c", key+".url="+tomlString(s.URL))
			continue
		}
		out = append(out, "-c", key+".command="+tomlString(s.Command))
		if len(s.Args) > 0 {
			parts := make([]string, len(s.Args))
			for i, a := range s.Args {
				parts[i] = tomlString(a)
			}
			out = append(out, "-c", key+".args=["+strings.Join(parts, ",")+"]")
		}
		if len(s.Env) > 0 {
			keys := make([]string, 0, len(s.Env))
			for k := range s.Env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			parts := make([]string, len(keys))
			for i, k := range keys {
				parts[i] = tomlString(k) + "=" + tomlString(s.Env[k])
			}
			out = append(out, "-c", key+".env={"+strings.Join(parts, ",")+"}")
		}
	}
	return out, nil
}

// Args builds the command line of a session (without the binary). schemaFile
// is the path of the output schema, if any. The prompt is read from stdin ("-").
// SettingsFile and MaxTurns have no codex equivalent and are ignored.
func (p *Provider) Args(spec ports.SessionSpec, schemaFile string) ([]string, error) {
	var args []string
	if spec.ResumeID != "" {
		// `exec resume` takes neither --sandbox nor --cd: the sandbox goes in as
		// a config override and the working directory is the process cwd.
		args = []string{"exec", "resume", "--json", "-c", "sandbox_mode=" + tomlString(Sandbox)}
	} else {
		args = []string{"exec", "--json", "--sandbox", Sandbox}
		if spec.Workdir != "" {
			args = append(args, "--cd", spec.Workdir)
		}
	}
	args = append(args, "--skip-git-repo-check")
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+tomlString(spec.Effort))
	}
	if spec.SystemPrompt != "" {
		args = append(args, "-c", "developer_instructions="+tomlString(spec.SystemPrompt))
	}
	if spec.MCPConfig != "" {
		mcp, err := mcpOverrides(spec.MCPConfig)
		if err != nil {
			return nil, err
		}
		args = append(args, mcp...)
	}
	if schemaFile != "" {
		args = append(args, "--output-schema", schemaFile)
	}
	args = append(args, p.opts.ExtraArgs...)
	if spec.ResumeID != "" {
		args = append(args, spec.ResumeID)
	}
	return append(args, "-"), nil
}

// Env builds the environment of a session.
func (p *Provider) Env(spec ports.SessionSpec) *provcommon.Env {
	env := provcommon.MinimalEnv(p.opts.ExtraEnvKeys...)
	env.Merge(spec.Env)
	if spec.Account.ConfigDir != "" {
		env.Set("CODEX_HOME", spec.Account.ConfigDir)
	}
	return env
}

// Open starts `codex exec --json`; the prompt goes through stdin.
func (p *Provider) Open(ctx context.Context, spec ports.SessionSpec) (ports.Session, error) {
	return p.OpenSession(ctx, spec)
}

// OpenSession is Open returning the concrete session.
func (p *Provider) OpenSession(ctx context.Context, spec ports.SessionSpec) (*provcommon.Session, error) {
	if spec.Workdir == "" {
		return nil, fmt.Errorf("codex: workdir is required")
	}
	var cleanup func()
	schemaFile := ""
	if s := strings.TrimSpace(spec.JSONSchema); s != "" {
		if strings.HasPrefix(s, "{") {
			dir, err := os.MkdirTemp("", "barnraise-codex-schema-")
			if err != nil {
				return nil, err
			}
			schemaFile = filepath.Join(dir, "schema.json")
			if err := os.WriteFile(schemaFile, []byte(s), 0o600); err != nil {
				_ = os.RemoveAll(dir)
				return nil, err
			}
			cleanup = func() { _ = os.RemoveAll(dir) }
		} else {
			schemaFile = s // already a file path
		}
	}
	fail := func(err error) (*provcommon.Session, error) {
		if cleanup != nil {
			cleanup()
		}
		return nil, err
	}
	args, err := p.Args(spec, schemaFile)
	if err != nil {
		return fail(err)
	}
	name := p.opts.Binary
	env := p.Env(spec)
	if spec.Container != nil {
		name, args = provcommon.ContainerArgs(spec.Container, env, spec.Workdir, name, args)
	} else if spec.RunAsUser != "" {
		name, args = provcommon.SudoArgs(spec.RunAsUser, env, name, args)
	}
	cmd := p.opts.Command(ctx, name, args...)
	cmd.Dir = spec.Workdir
	cmd.Env = append(cmd.Env, env.List()...)

	parser := NewParser(domain.TokenUsage{
		Agent:    spec.Agent,
		Account:  spec.Account.ID,
		Provider: "codex",
		Model:    spec.Model,
		Role:     spec.Role,
	})
	parser.Now = p.opts.Now
	return provcommon.Start(ctx, cmd, parser, provcommon.StartOptions{
		Name:    "codex",
		Timeout: spec.Timeout,
		Grace:   p.opts.Grace,
		Stdin:   strings.NewReader(spec.Prompt),
		OnExit:  cleanup,
	})
}

var _ ports.Provider = (*Provider)(nil)
