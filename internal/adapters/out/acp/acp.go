// Package acp is the provider adapter for any agent program that speaks
// the Agent Client Protocol (agentclientprotocol.com): JSON-RPC 2.0 over the
// program's stdin and stdout. Barnraise is the protocol's client. One adapter
// serves every agent configured as `type: acp` in barnraise.yaml, so a new
// agent needs no adapter or output parser of its own.
//
// The protocol is implemented here from its specification; the version in
// use is negotiated in initialize and every version-specific message lives in
// its own dialect (v1.go), so a later version is an addition.
package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/provcommon"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Name is the adapter's name (the `type` of the providers it runs).
const Name = domain.ProviderTypeACP

// Options configure the adapter.
type Options struct {
	// Command builds every exec.Cmd (agent, terminals, git); tests swap it.
	Command provcommon.CommandFunc
	// ExtraEnvKeys are inherited from the daemon environment on top of the minimal set.
	ExtraEnvKeys []string
	// Grace is how long the agent gets to end after its turn or a cancel (default 5s).
	Grace time.Duration
	// Sandbox reports whether terminals can run in bubblewrap; nil probes once.
	Sandbox func() bool
	// Version is Barnraise's version, told to the agent.
	Version string
	// Now is used to resolve relative limit reset times (default time.Now).
	Now func() time.Time
}

// Provider implements ports.Provider for ACP agents.
type Provider struct {
	opts      Options
	probeOnce sync.Once
	sandbox   bool
}

// New returns the ACP adapter.
func New(opts Options) *Provider {
	if opts.Command == nil {
		opts.Command = provcommon.DefaultCommand
	}
	if opts.Grace <= 0 {
		opts.Grace = provcommon.DefaultGrace
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	return &Provider{opts: opts}
}

// Name implements ports.Provider.
func (p *Provider) Name() string { return Name }

// Family implements ports.Provider; the model family of an ACP agent is
// set per provider in `models.families` (default: the provider's name).
func (p *Provider) Family() string { return Name }

// Detect implements ports.Provider. What an agent can do is only known
// once it answers initialize, at the start of each session; these are the
// capabilities of the client side, which Barnraise always has.
func (p *Provider) Detect(context.Context) (ports.Capabilities, error) {
	return ports.Capabilities{Version: fmt.Sprintf("acp/%d", latest().Version()), Headless: true, Stream: true, Resume: true,
		HookBlock: true, MCP: true, Sandbox: p.sandboxWorks()}, nil
}

func (p *Provider) sandboxWorks() bool {
	p.probeOnce.Do(func() {
		if p.opts.Sandbox != nil {
			p.sandbox = p.opts.Sandbox()
		} else {
			p.sandbox = probeSandbox()
		}
	})
	return p.sandbox
}

// Open implements ports.Provider: it starts the agent program and runs one
// prompt turn with the session's prompt.
func (p *Provider) Open(ctx context.Context, spec ports.SessionSpec) (ports.Session, error) {
	if spec.ACP == nil || strings.TrimSpace(spec.ACP.Command) == "" {
		return nil, fmt.Errorf("acp: the session has no agent program (providers.<name>.command in barnraise.yaml)")
	}
	if spec.Workdir == "" {
		return nil, fmt.Errorf("acp: workdir is required")
	}
	return p.start(ctx, spec)
}

// loadPolicy reads the compiled policy the daemon wrote for the session;
// without one (a session opened outside the runtime) the default rules
// apply to the working folder.
func loadPolicy(spec ports.SessionSpec) (domain.Policy, error) {
	if path := spec.Env["BARNRAISE_POLICY"]; path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return domain.Policy{}, fmt.Errorf("acp: policy: %w", err)
		}
		var pol domain.Policy
		if err := json.Unmarshal(data, &pol); err != nil {
			return domain.Policy{}, fmt.Errorf("acp: policy %s: %w", path, err)
		}
		return pol, nil
	}
	pol := domain.DefaultPolicy()
	pol.Home, _ = os.UserHomeDir()
	pol.Worktree = spec.Workdir
	return pol, nil
}

// existsOnMain reports whether a worktree-relative path exists on the main
// branch (locked files may be created but not changed).
func (p *Provider) existsOnMain(worktree, mainBranch string) func(string) bool {
	if mainBranch == "" {
		mainBranch = "main"
	}
	return func(rel string) bool {
		for _, ref := range []string{"refs/remotes/origin/" + mainBranch, "refs/heads/" + mainBranch} {
			cmd := p.opts.Command(context.Background(), "git", "-C", worktree, "cat-file", "-e", ref+":"+filepath.ToSlash(rel))
			if cmd.Run() == nil {
				return true
			}
		}
		return false
	}
}

// mcpServers reads the MCP configuration Barnraise wrote for the session
// (its own server and the ones the owner allowed for the role).
func mcpServers(path string) ([]mcpServer, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("acp: mcp config: %w", err)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("acp: mcp config %s: %w", path, err)
	}
	names := make([]string, 0, len(cfg.MCPServers))
	for n := range cfg.MCPServers {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]mcpServer, 0, len(names))
	for _, n := range names {
		s := cfg.MCPServers[n]
		out = append(out, mcpServer{Name: n, Command: s.Command, Args: s.Args, Env: s.Env, URL: s.URL, Headers: s.Headers})
	}
	return out, nil
}

var _ ports.Provider = (*Provider)(nil)
