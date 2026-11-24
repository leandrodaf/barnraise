package domain

import (
	"strings"
)

// The catalog of capabilities: every feature whose status depends on the
// machine or on a switch. A nil cfg in an On function is a new project's
// defaults, so an evaluation without projects still says what a project
// would get here.

const (
	fixBwrapInstall = "sudo apt install bubblewrap  (Fedora: sudo dnf install bubblewrap; Arch: sudo pacman -S bubblewrap)"
	// An AppArmor profile lets bwrap alone make user namespaces, and it lasts
	// across reboots; the sysctl opens them to every program and is lost.
	fixBwrapUserns = `printf 'abi <abi/4.0>,\ninclude <tunables/global>\nprofile bwrap /usr/bin/bwrap flags=(unconfined) {\n  userns,\n}\n' | sudo tee /etc/apparmor.d/bwrap >/dev/null && sudo apparmor_parser -r /etc/apparmor.d/bwrap`
	fixKVM         = "sudo usermod -aG kvm $USER, then log out and in again (and turn on virtualization in the firmware if /dev/kvm is missing)"
	fixLandlock    = "boot Linux 5.13 or later with landlock in the lsm= list of the kernel command line (most distributions since 2022 have it)"
	fixEgress      = "start the daemon with the egress proxy: barnraise up --egress 127.0.0.1:7421 (or BARNRAISE_EGRESS=127.0.0.1:7421)"
	fixDocker      = "install Docker Engine with the compose plugin (https://docs.docker.com/engine/install/) and add yourself to the docker group: sudo usermod -aG docker $USER"
	fixGit         = "install git 2.25 or later (sudo apt install git, brew install git)"
)

func cfgOr(cfg *ProjectConfig) *ProjectConfig {
	if cfg != nil {
		return cfg
	}
	return defaultCapConfig
}

// defaultCapConfig is what a new barnraise.yaml gets for the switches the
// catalog reads (the defaults of internal/config).
var defaultCapConfig = &ProjectConfig{
	Merge:      MergeDef{Rerere: true, Structural: "auto"},
	Egress:     EgressDef{RepoScope: true},
	Knowledge:  KnowledgeDef{Symbols: "auto", Map: RepoMapDef{Enabled: true}},
	Outfilter:  DefaultOutfilter(),
	StaleReads: DefaultStaleReads(),
	Lessons:    DefaultLessons(),
}

func egressOn(_ *ProjectConfig, m MachineSettings) bool { return m.Egress != "" }

func hasBuiltinCheck(cfg *ProjectConfig, runsOn string) bool {
	for _, ch := range cfgOr(cfg).Checks {
		if ch.RunsOn == runsOn {
			return true
		}
	}
	return false
}

// usesProvider: a role lists the provider, or (no role lists any) it is
// Claude, the default.
func usesProvider(cfg *ProjectConfig, name string) bool {
	c := cfgOr(cfg)
	listed := false
	for _, r := range c.Roles {
		for _, p := range r.Providers {
			listed = true
			if p == name {
				return true
			}
		}
	}
	return !listed && name == "claude"
}

// staticCapabilities are the catalog's fixed entries.
var staticCapabilities = []Capability{
	// security
	{ID: "security.trust", Title: "Bound session tokens, chained event log, provenance of hooks", Group: CapGroupSecurity, Kind: CapNative},
	{ID: "egress.proxy", Title: "Egress allow-list proxy", Group: CapGroupSecurity, Kind: CapNative, On: egressOn, Enable: fixEgress},
	{ID: "egress.broker", Title: "Provider key broker", Group: CapGroupSecurity, Kind: CapNative, On: egressOn, Enable: fixEgress,
		Requires: []Requirement{{Kind: ReqEgressCA, Auto: true, Fix: "let the daemon write <data dir>/egress (it creates the CA when the proxy starts)",
			Fallback: "provider keys reach the sessions as environment variables, as before"}}},
	{ID: "egress.inspect", Title: "TLS inspection and signed receipts", Group: CapGroupSecurity, Kind: CapNative, On: egressOn, Enable: fixEgress,
		Requires: []Requirement{{Kind: ReqEgressCA, Auto: true, Fix: "let the daemon write <data dir>/egress (it creates the CA when the proxy starts)",
			Fallback: "TLS is not terminated: nothing is scanned for secrets and no receipt is signed"}}},
	{ID: "egress.rules", Title: "Method and path rules (writes only to the project's repository)", Group: CapGroupSecurity, Kind: CapNative,
		On: func(cfg *ProjectConfig, m MachineSettings) bool { return m.Egress != "" }, Enable: fixEgress,
		Requires: []Requirement{{Kind: ReqEgressCA, Auto: true, Fix: "let the daemon write <data dir>/egress (it creates the CA when the proxy starts)",
			Fallback: "path rules apply to plain HTTP only; HTTPS is filtered by host"}}},
	{ID: "security.gitleaks", Title: "Secret scan with gitleaks", Group: CapGroupSecurity, Kind: CapOptional,
		Requires: []Requirement{{Kind: ReqTool, Name: "gitleaks", Tool: "gitleaks", Fix: "install gitleaks (https://github.com/gitleaks/gitleaks#installing)",
			Fallback: "Barnraise's built-in secret patterns scan every delivery"}}},

	// isolation
	{ID: "sandbox.landlock", Title: "Landlock confinement of setup, checks and sessions", Group: CapGroupIsolation, Kind: CapNative, OS: []string{"linux"},
		Requires: []Requirement{
			{Kind: ReqLandlock, Min: "1", Fix: fixLandlock},
			{Kind: ReqLandlock, Min: "4", Fix: "Linux 6.7 or later restricts TCP ports with Landlock",
				Fallback: "files are confined, but the kernel cannot restrict TCP: the network rules of the profiles do not apply"},
		},
		Fallback: ""},
	{ID: "sandbox.bubblewrap", Title: "Bubblewrap sandbox (Codex, ACP terminals)", Group: CapGroupIsolation, Kind: CapOptional, OS: []string{"linux"},
		Requires: []Requirement{
			{Kind: ReqTool, Name: "bwrap", Fix: fixBwrapInstall, Root: true},
			{Kind: ReqBwrap, Fix: fixBwrapUserns, Root: true},
		},
		Fallback: "Landlock confines the sessions instead where the kernel has it; Codex gets no work on a machine with neither"},
	{ID: "sandbox.microvm", Title: "MicroVM isolation", Group: CapGroupIsolation, Kind: CapOptional, OS: []string{"linux", "darwin"},
		On:     func(_ *ProjectConfig, m MachineSettings) bool { return m.IsolationBackend == "microvm" },
		Enable: "daemon.yaml: isolation: {backend: microvm, microvm: {image: <OCI image with your toolchain>}}",
		Requires: []Requirement{
			{Kind: ReqTool, Name: "smolvm", Tool: "smolvm", Fix: "install smolvm (https://github.com/smol-machines/smolvm)"},
			{Kind: ReqKVM, OS: []string{"linux"}, Fix: fixKVM, Root: true},
			{Kind: ReqSetting, Name: "isolation.microvm.image", Auto: true, Fix: "daemon.yaml: isolation.microvm.image: <OCI image with your toolchain>"},
		},
		Fallback: "commands run under Landlock instead"},
	{ID: "sandbox.profiles", Title: "Sandbox profiles (setup, check, session)", Group: CapGroupIsolation, Kind: CapNative,
		Requires: []Requirement{{Kind: ReqConfiner, Fix: fixLandlock + "; or turn the microVM on (daemon.yaml isolation.backend: microvm)",
			Fallback: "no layer confines commands on this machine: setup, checks and sessions run unconfined, as before"}}},

	// merge
	{ID: "merge.rerere", Title: "Recorded conflict resolutions (rerere)", Group: CapGroupMerge, Kind: CapNative,
		On: func(cfg *ProjectConfig, _ MachineSettings) bool { return cfgOr(cfg).Merge.Rerere }, Enable: "barnraise.yaml: merge.rerere: true",
		Requires: []Requirement{{Kind: ReqTool, Name: "git", Fix: fixGit}}},
	{ID: "merge.structural", Title: "Structural merge (mergiraf)", Group: CapGroupMerge, Kind: CapOptional,
		On:     func(cfg *ProjectConfig, _ MachineSettings) bool { return cfgOr(cfg).Merge.Structural != "off" },
		Enable: "barnraise.yaml: merge.structural: auto",
		Requires: []Requirement{{Kind: ReqTool, Name: "mergiraf", Tool: "mergiraf", Fix: "install mergiraf (cargo install --locked mergiraf)",
			Fallback: "git's line-based merge resolves conflicts, as before"}}},

	// checks
	{ID: "checks.dependencies", Title: "Dependency check (OSV)", Group: CapGroupChecks, Kind: CapNative,
		On: func(cfg *ProjectConfig, _ MachineSettings) bool {
			return hasBuiltinCheck(cfg, RunsOnDependencies) && !cfgOr(cfg).Dependencies.Off
		},
		Enable: "barnraise.yaml: checks: {deps: {runs_on: builtin:dependencies}}",
		Requires: []Requirement{{Kind: ReqTool, Name: "osv-scanner", Tool: "osv-scanner", Fix: "install osv-scanner (https://google.github.io/osv-scanner/installation/)",
			Fallback: "the lockfiles are checked against the OSV API over HTTPS (it needs the network)"}}},
	{ID: "checks.ast-grep", Title: "Structural lint check (builtin:ast-grep)", Group: CapGroupChecks, Kind: CapOptional,
		On:       func(cfg *ProjectConfig, _ MachineSettings) bool { return hasBuiltinCheck(cfg, RunsOnASTGrep) },
		Enable:   "barnraise.yaml: checks: {structure: {runs_on: builtin:ast-grep}} with rules in sgconfig.yml",
		Requires: []Requirement{{Kind: ReqTool, Name: "ast-grep", Tool: "ast-grep", Fix: "install ast-grep (https://ast-grep.github.io/guide/quick-start.html)"}}},

	// context
	{ID: "context.search", Title: "Trigram code search", Group: CapGroupContext, Kind: CapNative},
	{ID: "context.map", Title: "Repository map", Group: CapGroupContext, Kind: CapNative,
		On: func(cfg *ProjectConfig, _ MachineSettings) bool { return cfgOr(cfg).Knowledge.Map.Enabled }, Enable: "barnraise.yaml: knowledge.map.enabled: true"},
	{ID: "context.symbols", Title: "Symbols by syntax tree (ast-grep)", Group: CapGroupContext, Kind: CapOptional,
		On: func(cfg *ProjectConfig, _ MachineSettings) bool { return cfgOr(cfg).Knowledge.Symbols != "native" }, Enable: "barnraise.yaml: knowledge.symbols: auto",
		Requires: []Requirement{{Kind: ReqTool, Name: "ast-grep", Tool: "ast-grep", Fix: "install ast-grep (https://ast-grep.github.io/guide/quick-start.html)",
			Fallback: "the native extractor finds symbols (Go by its parser, other languages by patterns)"}}},
	{ID: "context.stale-reads", Title: "Stale read warning", Group: CapGroupContext, Kind: CapNative,
		On: func(cfg *ProjectConfig, _ MachineSettings) bool { return cfgOr(cfg).StaleReads.Enabled }, Enable: "barnraise.yaml: stale_reads.enabled: true"},
	{ID: "context.lessons", Title: "Lessons from rework", Group: CapGroupContext, Kind: CapNative,
		On: func(cfg *ProjectConfig, _ MachineSettings) bool { return cfgOr(cfg).Lessons.Enabled }, Enable: "barnraise.yaml: lessons.enabled: true"},

	// tokens
	{ID: "tokens.outfilter", Title: "Output filters with recoverable full outputs", Group: CapGroupTokens, Kind: CapNative},
	{ID: "tokens.rtk", Title: "Output compression delegated to rtk", Group: CapGroupTokens, Kind: CapOptional,
		On:     func(cfg *ProjectConfig, _ MachineSettings) bool { return cfgOr(cfg).Outfilter.Delegate != DelegateOff },
		Enable: "barnraise.yaml: outfilter.delegate: auto",
		Requires: []Requirement{{Kind: ReqTool, Name: "rtk", Tool: "rtk", Fix: "install rtk (https://github.com/rtk-ai/rtk)",
			Fallback: "Barnraise's own filters compress the outputs"}}},

	// worktrees
	{ID: "worktrees.git", Title: "Git worktrees per item", Group: CapGroupWorktrees, Kind: CapNative,
		Requires: []Requirement{{Kind: ReqTool, Name: "git", Min: "2.25", Fix: fixGit}}},
	{ID: "worktrees.services", Title: "Services per item (docker compose)", Group: CapGroupWorktrees, Kind: CapOptional,
		On:     func(cfg *ProjectConfig, _ MachineSettings) bool { return cfgOr(cfg).Worktree.Isolate.Compose == "on" },
		Enable: "barnraise.yaml: worktree.isolate.compose: on (auto uses it where a compose file is at the worktree root)",
		Requires: []Requirement{
			{Kind: ReqTool, Name: "docker", Fix: fixDocker, Root: true},
			{Kind: ReqCompose, Fix: fixDocker, Root: true},
		}},
	{ID: "worktrees.build-cache", Title: "Build caches copied copy-on-write", Group: CapGroupWorktrees, Kind: CapNative,
		On:     func(cfg *ProjectConfig, _ MachineSettings) bool { return len(cfgOr(cfg).Worktree.BuildCache) > 0 },
		Enable: "barnraise.yaml: worktree.build_cache: [<folders>]",
		Requires: []Requirement{{Kind: ReqReflink, Fix: "keep the data folder (BARNRAISE_DATA_DIR) on Btrfs, XFS, bcachefs or APFS",
			Fallback: "build caches are not shared: each worktree builds from scratch (or is copied in full with worktree.build_cache_copy)"}}},
	{ID: "worktrees.hooks", Title: "Worktree phase hooks", Group: CapGroupWorktrees, Kind: CapNative,
		On: func(cfg *ProjectConfig, _ MachineSettings) bool {
			h := cfgOr(cfg).Worktree.Hooks
			return h.PostCreate != "" || h.PreSession != "" || h.PostIntegrate != "" || h.PreRemove != ""
		},
		Enable: "barnraise.yaml: worktree.hooks.post_create (pre_session, post_integrate, pre_remove)"},
	{ID: "worktrees.containers", Title: "Agent sessions in containers", Group: CapGroupWorktrees, Kind: CapOptional,
		On:       func(cfg *ProjectConfig, _ MachineSettings) bool { return cfgOr(cfg).Container.Image != "" },
		Enable:   "barnraise.yaml: container.image: <image with the AI CLI and your toolchain>",
		Requires: []Requirement{{Kind: ReqTool, Name: "docker", Fix: fixDocker, Root: true}}},

	// telemetry
	{ID: "telemetry.otel", Title: "OpenTelemetry export (shipped hook plugin)", Group: CapGroupTelemetry, Kind: CapPlugin,
		On: func(cfg *ProjectConfig, _ MachineSettings) bool {
			for _, p := range cfgOr(cfg).Plugins {
				if strings.Join(p.Args, " ") == "plugin otel" {
					return true
				}
			}
			return false
		},
		Enable: "barnraise.yaml: plugins: {otel: {command: barnraise, args: [plugin, otel], events: [\"*\"]}}  (see docs/plugins.md)"},
}

// providerCapabilities are the AI command-line programs.
func providerCapabilities() []Capability {
	var out []Capability
	for _, p := range []struct{ name, title, fix string }{
		{"claude", "Claude Code", "npm install -g @anthropic-ai/claude-code"},
		{"codex", "Codex CLI", "npm install -g @openai/codex"},
		{"gemini", "Gemini CLI", "npm install -g @google/gemini-cli"},
	} {
		name := p.name
		out = append(out, Capability{ID: "providers." + name, Title: p.title, Group: CapGroupProviders, Kind: CapOptional,
			On:     func(cfg *ProjectConfig, _ MachineSettings) bool { return usesProvider(cfg, name) },
			Enable: "barnraise.yaml: roles.<role>.providers: [" + name + "]",
			Requires: []Requirement{
				{Kind: ReqTool, Name: name, Fix: p.fix},
				{Kind: ReqLogin, Name: name, Fix: "barnraise account add " + name},
			}})
	}
	return out
}

// CapabilityCatalog is the catalog for these projects: the fixed entries,
// the providers, and one entry per configured plugin, ACP agent and the
// machine's network.
func CapabilityCatalog(cfgs []*ProjectConfig, m MachineSettings) []Capability {
	out := append([]Capability(nil), staticCapabilities...)
	out = append(out, providerCapabilities()...)
	out = append(out, Capability{ID: "cluster.network", Title: "Network between machines (" + def0(m.Network, "auto") + ")", Group: CapGroupCluster,
		Kind: CapOptional, Requires: networkRequirements(m)})
	seenPlugin, seenACP := map[string]bool{}, map[string]bool{}
	for _, cfg := range cfgs {
		if cfg == nil {
			continue
		}
		for _, name := range sortedKeys(cfg.Plugins) {
			p := cfg.Plugins[name]
			if seenPlugin[name] || p.Command == "" || strings.Join(p.Args, " ") == "plugin otel" {
				continue
			}
			seenPlugin[name] = true
			out = append(out, Capability{ID: "plugins." + name, Title: "Plugin " + name, Group: CapGroupPlugins, Kind: CapPlugin,
				Requires: []Requirement{{Kind: ReqTool, Name: p.Command, Fix: "install " + p.Command + " on this machine, on the daemon's PATH (or set plugins." + name + ".command to its absolute path)"}}})
		}
		for _, name := range sortedKeys(cfg.ACPProviders) {
			p := cfg.ACPProviders[name]
			if seenACP[name] || p.Command == "" {
				continue
			}
			seenACP[name] = true
			out = append(out, Capability{ID: "providers.acp." + name, Title: "ACP agent " + name, Group: CapGroupProviders, Kind: CapOptional,
				Requires: []Requirement{{Kind: ReqTool, Name: p.Command, Fix: "install " + p.Command + " on this machine (or set providers." + name + ".command to its absolute path)"}}})
		}
	}
	return out
}

// networkRequirements are what the machine's network mode needs: auto
// uses Tailscale when it is installed and the local network otherwise.
func networkRequirements(m MachineSettings) []Requirement {
	switch m.Network {
	case "lan", "tsnet":
		return nil
	case "tailscale":
		return []Requirement{{Kind: ReqTool, Name: "tailscale", Fix: "install Tailscale (https://tailscale.com/download) and log in: tailscale up"}}
	case "plugin":
		if m.NetworkPlugin == "" {
			return []Requirement{{Kind: ReqSetting, Name: "network_plugin.command", Fix: "daemon.yaml: network_plugin: {command: <program>}"}}
		}
		return []Requirement{{Kind: ReqTool, Name: m.NetworkPlugin, Fix: "install the network plugin " + m.NetworkPlugin + " on the daemon's PATH"}}
	}
	return []Requirement{{Kind: ReqTool, Name: "tailscale", Fix: "install Tailscale (https://tailscale.com/download) or set daemon.yaml network: lan",
		Fallback: "machines find each other on the local network (or through the addresses in daemon.yaml peers)"}}
}
