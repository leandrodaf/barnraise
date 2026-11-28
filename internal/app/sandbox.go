package app

import (
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Isolation are the layers Barnraise itself puts around commands, on top
// of (or instead of) the providers' own sandboxes.
type Isolation struct {
	// Landlock confines setup, checks, and the agent sessions of a machine
	// where the provider's own sandbox (bubblewrap) cannot start. Optional.
	Landlock ports.Confiner
	// MicroVM runs the kinds of command MicroVMFor says inside a microVM.
	// Opt-in (daemon.yaml); optional.
	MicroVM    ports.Confiner
	MicroVMFor func(kind string) bool
	// ToolDirs are read-only in every confinement: the tools Barnraise
	// installed (ADR-0046), inside its otherwise denied data folder.
	ToolDirs []string
}

// Kinds of confined command.
const (
	ConfineSessions = "sessions"
	ConfineChecks   = "checks"
	ConfineSetup    = "setup"
)

// confiner picks the layer for a kind of command: the microVM when it is on
// for that kind and the runner works here, otherwise Landlock when the
// kernel has it. nil: nothing confines it (the command runs as before).
func (s *Service) confiner(kind string) (ports.Confiner, string) {
	iso := s.d.Isolation
	if iso.MicroVM != nil && iso.MicroVMFor != nil && iso.MicroVMFor(kind) {
		if sup := iso.MicroVM.Support(); sup.Available {
			return iso.MicroVM, sup.Backend
		}
		s.warnOnce("microvm-"+kind, "the microVM is on in daemon.yaml but cannot run here; Landlock is used instead", "why", iso.MicroVM.Support().String())
	}
	if iso.Landlock != nil {
		if sup := iso.Landlock.Support(); sup.Available {
			return iso.Landlock, sup.Backend
		}
	}
	return nil, ""
}

var warned sync.Map

func (s *Service) warnOnce(key, msg string, args ...any) {
	if _, loaded := warned.LoadOrStore(s.d.Machine+"/"+key, true); !loaded {
		s.d.Log.Warn(msg, args...)
	}
}

// landlockReady reports whether Landlock can confine commands here.
func (s *Service) landlockReady() bool {
	l := s.d.Isolation.Landlock
	return l != nil && l.Support().Available
}

// SandboxSupport reports the layers this machine offers, for status output.
func (s *Service) SandboxSupport() []domain.SandboxSupport {
	var out []domain.SandboxSupport
	if l := s.d.Isolation.Landlock; l != nil {
		out = append(out, l.Support())
	}
	if m := s.d.Isolation.MicroVM; m != nil {
		out = append(out, m.Support())
	}
	return out
}

// proxyPort is the egress proxy's port, 0 when none runs.
func (r *Runtime) proxyPort() int {
	if r.EgressProxy == "" {
		return 0
	}
	_, port, err := net.SplitHostPort(r.EgressProxy)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(port)
	return n
}

// confinement resolves a profile for a command of the project. The
// project's shared stores (worktree.stores) are writable to every confined
// command, like its worktree: setup fills them, checks and sessions use them.
func (s *Service) confinement(p *Project, profile string, work []string, command string, env []string) (domain.Confinement, error) {
	home, _ := os.UserHomeDir()
	cfg := p.Cfg()
	work = append(append([]string(nil), work...), s.storeDirs(p)...)
	return domain.BuildConfinement(domain.ConfineInput{
		Profile: profile, Home: home, Work: work, TmpDir: os.TempDir(), Proxy: s.runtime.proxyPort(),
		DenyRead: cfg.Policy.DenyRead, Project: cfg.Sandbox, Command: command, Hosts: cfg.Policy.AllowHosts, Env: env, Tools: s.d.Isolation.ToolDirs,
	})
}

// repoGit is the repository's .git, where a worktree's commits live.
func repoGit(p *Project) string { return filepath.Join(p.Cfg().Root, ".git") }

// wrapCommand confines a command of kind with profile. It returns the
// command unchanged when the profile is none or nothing can confine here.
func (s *Service) wrapCommand(p *Project, kind, profile, dir string, env []string, name string, args []string) (string, []string, string, error) {
	if profile == domain.SandboxNone {
		return name, args, "", nil
	}
	cf, backend := s.confiner(kind)
	if cf == nil {
		return name, args, "", nil
	}
	conf, err := s.confinement(p, profile, []string{dir, repoGit(p)}, strings.Join(args, " "), env)
	if err != nil {
		return "", nil, "", err
	}
	n, a, err := cf.Wrap(conf, name, args)
	return n, a, backend, err
}

// CheckConfine is how a local check of the project runs on this machine:
// the wrapper of its profile (nil when it runs unconfined) and the
// variables it needs on top of its own (the egress proxy, when the profile
// keeps the network to loopback and the proxy).
func (s *Service) CheckConfine(project string, def domain.CheckDef, dir string, env map[string]string) (*ports.Confine, map[string]string, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, nil, err
	}
	profile := domain.CheckSandbox(def)
	if profile == domain.SandboxNone {
		return nil, nil, nil
	}
	cf, backend := s.confiner(ConfineChecks)
	if cf == nil {
		return nil, nil, nil
	}
	extra := map[string]string{}
	if r := s.runtime; r.EgressProxy != "" {
		proxy := "http://" + r.EgressProxy
		extra["HTTPS_PROXY"], extra["HTTP_PROXY"], extra["https_proxy"], extra["http_proxy"] = proxy, proxy, proxy, proxy
		extra["NO_PROXY"], extra["no_proxy"] = "localhost,127.0.0.1", "localhost,127.0.0.1"
	}
	names := envNames(env, extra)
	names = append(names, "BARNRAISE_ITEM", "BARNRAISE_CHECK", "BARNRAISE_TOUCHED", "BARNRAISE_TOUCHED_FILE", "BARNRAISE_AREAS", "BARNRAISE_BRANCH", "BARNRAISE_COMMIT", "BARNRAISE_MAIN")
	conf, err := s.confinement(p, profile, []string{dir, repoGit(p)}, def.Run, names)
	if err != nil {
		return nil, nil, err
	}
	if conf.Network == domain.NetLocal {
		conf.Hosts = nil // a check reaches no host by name (in a microVM: no network at all)
	}
	return &ports.Confine{Backend: backend, Wrap: func(name string, args []string) (string, []string, error) {
		return cf.Wrap(conf, name, args)
	}}, extra, nil
}

func envNames(ms ...map[string]string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range ms {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// providerAPIHosts are the hosts the AI CLIs talk to; a microVM session
// may reach them on top of the policy's allow_hosts.
var providerAPIHosts = []string{"api.anthropic.com", "statsig.anthropic.com", "claude.ai", "console.anthropic.com",
	"api.openai.com", "chatgpt.com", "auth.openai.com", "generativelanguage.googleapis.com", "oauth2.googleapis.com",
	"cloudcode-pa.googleapis.com", "openrouter.ai"}

// sessionVars are variables a provider adapter sets for its CLI.
var sessionVars = []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "GEMINI_CLI_HOME", "GEMINI_CLI_SYSTEM_SETTINGS_PATH", ports.EndpointKeyEnv, "DISABLE_AUTOUPDATER"}

// microVMSessions reports whether agent sessions run in a microVM here.
func (r *Runtime) microVMSessions() bool {
	_, backend := r.s.confiner(ConfineSessions)
	return backend == "microvm"
}

// sessionConfine confines an agent session run on the machine itself: in a
// microVM when it is on for sessions, otherwise with Landlock where the
// provider's own sandbox cannot start (bubblewrap refused by AppArmor).
// nil: the session runs as before, with the provider's own sandbox.
func (r *Runtime) sessionConfine(p *Project, dir string, acc *domain.Account, provider string, endpoint *ports.Endpoint, env map[string]string) (*ports.Confine, error) {
	cf, backend := r.s.confiner(ConfineSessions)
	if cf == nil || (backend != "microvm" && r.Sandbox) {
		return nil, nil
	}
	home, _ := os.UserHomeDir()
	work := []string{dir, repoGit(p)}
	if acc.ConfigDir != "" {
		work = append(work, acc.ConfigDir)
	} else {
		switch provider {
		case "claude":
			work = append(work, filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json"))
		case "codex":
			work = append(work, filepath.Join(home, ".codex"))
		case "gemini":
			work = append(work, filepath.Join(home, ".gemini"))
		}
	}
	if provider == "claude" {
		work = append(work, filepath.Join(home, ".local", "state", "claude"))
	}
	names := append(envNames(env), sessionVars...)
	conf, err := r.s.confinement(p, domain.SandboxAgent, work, "", names)
	if err != nil {
		return nil, err
	}
	conf.Hosts = append(append([]string(nil), conf.Hosts...), providerAPIHosts...)
	if endpoint != nil {
		if h := hostOf(endpoint.BaseURL); h != "" {
			conf.Hosts = append(conf.Hosts, h)
		}
	}
	return &ports.Confine{Backend: backend, Wrap: func(name string, args []string) (string, []string, error) {
		return cf.Wrap(conf, name, args)
	}}, nil
}

func hostOf(u string) string {
	rest, ok := strings.CutPrefix(u, "https://")
	if !ok {
		rest, _ = strings.CutPrefix(u, "http://")
	}
	host, _, _ := strings.Cut(rest, "/")
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// sandboxAlerts says when this machine runs agents' commands with less
// confinement than it should: no working bubblewrap and no Landlock.
func (s *Service) sandboxAlerts(p *Project) []string {
	r := s.runtime
	if r.Sandbox || p.Cfg().Container.Image != "" || s.landlockReady() || s.d.Isolation.Landlock == nil {
		return nil
	}
	return []string{"machine " + s.d.Machine + " has no sandbox: bubblewrap cannot start and Landlock is unavailable (" +
		s.d.Isolation.Landlock.Support().String() + "); setup, checks and Claude sessions run unconfined, Codex gets no work"}
}
