package domain

import (
	"strings"
	"testing"
	"time"
)

// fullProbe is a Linux machine with everything.
func fullProbe() MachineProbe {
	tools := map[string]ToolProbe{}
	for _, t := range ProbedTools {
		tools[t.Name] = ToolProbe{Name: t.Name, Found: true, Path: "/usr/bin/" + t.Name, Version: "99.0.0"}
	}
	return MachineProbe{OS: "linux", Arch: "amd64", ProbedAt: time.Unix(1700000000, 0), Tools: tools,
		LandlockABI: 6, BwrapUsable: true, KVMPresent: true, KVMAccess: true, Reflink: true, Compose: "2.29.0", EgressCA: true,
		Logins: map[string]bool{"claude": true, "codex": true, "gemini": true}}
}

func without(p MachineProbe, tools ...string) MachineProbe {
	cp := map[string]ToolProbe{}
	for k, v := range p.Tools {
		cp[k] = v
	}
	for _, t := range tools {
		delete(cp, t)
	}
	p.Tools = cp
	return p
}

func byID(items []CapabilityItem) map[string]CapabilityItem {
	out := map[string]CapabilityItem{}
	for _, it := range items {
		out[it.ID] = it
	}
	return out
}

func TestEvaluateCapabilities(t *testing.T) {
	on := MachineSettings{Egress: "127.0.0.1:7421", IsolationBackend: "microvm", MicroVMImage: "img"}
	cfgRTKOff := &ProjectConfig{Outfilter: OutfilterDef{Delegate: DelegateOff}, Merge: MergeDef{Structural: "off"}}
	deps := &ProjectConfig{Checks: map[string]CheckDef{"deps": {RunsOn: RunsOnDependencies}, "lint": {RunsOn: RunsOnASTGrep}},
		Worktree:  WorktreeOpts{BuildCache: []string{"build"}, Isolate: WorktreeIsolation{Compose: "on"}, Hooks: WorktreeHooks{PostCreate: "make"}},
		Container: ContainerDef{Image: "x"}, Knowledge: KnowledgeDef{Map: RepoMapDef{Enabled: true}},
		Roles: map[string]RoleDef{"dev": {Providers: []string{"codex"}}}}
	tests := []struct {
		name     string
		probe    MachineProbe
		settings MachineSettings
		cfgs     []*ProjectConfig
		id       string
		status   string
		reason   string // substring
		fix      string // substring
		tool     string
		root     bool
		fallback string
	}{
		{name: "trust always on", probe: fullProbe(), id: "security.trust", status: CapActive, reason: "Always on"},
		{name: "egress off is available", probe: fullProbe(), id: "egress.proxy", status: CapAvailable, fix: "--egress"},
		{name: "egress on is active", probe: fullProbe(), settings: on, id: "egress.proxy", status: CapActive},
		{name: "broker without CA degrades", probe: func() MachineProbe { p := fullProbe(); p.EgressCA = false; return p }(), settings: on,
			id: "egress.broker", status: CapDegraded, fallback: "environment variables"},
		{name: "broker off ignores the CA it creates", probe: func() MachineProbe { p := fullProbe(); p.EgressCA = false; return p }(),
			id: "egress.broker", status: CapAvailable},
		{name: "landlock active", probe: fullProbe(), id: "sandbox.landlock", status: CapActive},
		{name: "landlock old ABI degrades", probe: func() MachineProbe { p := fullProbe(); p.LandlockABI = 3; return p }(),
			id: "sandbox.landlock", status: CapDegraded, reason: "ABI is 3", fallback: "cannot restrict TCP"},
		{name: "landlock none is missing", probe: func() MachineProbe { p := fullProbe(); p.LandlockABI = 0; p.LandlockReason = "no lsm"; return p }(),
			id: "sandbox.landlock", status: CapMissing, reason: "no lsm", fix: "lsm="},
		{name: "landlock on darwin unsupported", probe: func() MachineProbe { p := fullProbe(); p.OS = "darwin"; return p }(),
			id: "sandbox.landlock", status: CapUnsupported, reason: "linux only"},
		{name: "bwrap missing shows the root command", probe: without(fullProbe(), "bwrap"), id: "sandbox.bubblewrap", status: CapDegraded,
			fix: "apt install bubblewrap", root: true, fallback: "Landlock"},
		{name: "bwrap refused userns", probe: func() MachineProbe { p := fullProbe(); p.BwrapUsable = false; return p }(),
			id: "sandbox.bubblewrap", status: CapDegraded, fix: "apparmor", root: true},
		{name: "microvm off but runnable", probe: fullProbe(), id: "sandbox.microvm", status: CapAvailable, fix: "backend: microvm"},
		{name: "microvm off without smolvm offers the install", probe: without(fullProbe(), "smolvm"), id: "sandbox.microvm", status: CapMissing,
			reason: "Switched off", fix: "barnraise tools install smolvm", tool: "smolvm"},
		{name: "microvm on without kvm access", probe: func() MachineProbe { p := fullProbe(); p.KVMAccess = false; return p }(), settings: on,
			id: "sandbox.microvm", status: CapDegraded, fix: "usermod -aG kvm", root: true, fallback: "Landlock"},
		{name: "microvm on darwin ignores kvm", probe: func() MachineProbe { p := fullProbe(); p.OS, p.Arch, p.KVMPresent = "darwin", "arm64", false; return p }(),
			settings: on, id: "sandbox.microvm", status: CapActive},
		{name: "microvm on without image", probe: fullProbe(), settings: MachineSettings{IsolationBackend: "microvm"},
			id: "sandbox.microvm", status: CapDegraded, fix: "isolation.microvm.image"},
		{name: "microvm on windows unsupported", probe: func() MachineProbe { p := fullProbe(); p.OS = "windows"; return p }(),
			id: "sandbox.microvm", status: CapUnsupported},
		{name: "profiles without a layer", probe: func() MachineProbe { p := fullProbe(); p.LandlockABI = 0; return p }(),
			id: "sandbox.profiles", status: CapDegraded, fallback: "unconfined"},
		{name: "profiles on a mac with the microVM", probe: func() MachineProbe { p := fullProbe(); p.OS, p.LandlockABI = "darwin", 0; return p }(),
			settings: on, id: "sandbox.profiles", status: CapActive},
		{name: "structural without mergiraf degrades", probe: without(fullProbe(), "mergiraf"), id: "merge.structural", status: CapDegraded,
			tool: "mergiraf", fix: "barnraise tools install mergiraf", fallback: "line-based"},
		{name: "structural switched off", probe: fullProbe(), cfgs: []*ProjectConfig{cfgRTKOff}, id: "merge.structural", status: CapAvailable, fix: "structural: auto"},
		{name: "rerere off", probe: fullProbe(), cfgs: []*ProjectConfig{cfgRTKOff}, id: "merge.rerere", status: CapAvailable},
		{name: "rerere default on", probe: fullProbe(), id: "merge.rerere", status: CapActive, reason: "running"},
		{name: "rtk off", probe: fullProbe(), cfgs: []*ProjectConfig{cfgRTKOff}, id: "tokens.rtk", status: CapAvailable},
		{name: "rtk missing", probe: without(fullProbe(), "rtk"), id: "tokens.rtk", status: CapDegraded, tool: "rtk"},
		{name: "deps check off by default", probe: fullProbe(), id: "checks.dependencies", status: CapAvailable},
		{name: "deps check without scanner uses the API", probe: without(fullProbe(), "osv-scanner"), cfgs: []*ProjectConfig{deps},
			id: "checks.dependencies", status: CapDegraded, fallback: "OSV API", tool: "osv-scanner"},
		{name: "ast-grep check cannot run", probe: without(fullProbe(), "ast-grep"), cfgs: []*ProjectConfig{deps},
			id: "checks.ast-grep", status: CapMissing, tool: "ast-grep"},
		{name: "symbols fall back", probe: without(fullProbe(), "ast-grep"), id: "context.symbols", status: CapDegraded, fallback: "native extractor", tool: "ast-grep"},
		{name: "map default on", probe: fullProbe(), id: "context.map", status: CapActive},
		{name: "map off in this project", probe: fullProbe(), cfgs: []*ProjectConfig{cfgRTKOff}, id: "context.map", status: CapAvailable},
		{name: "old git", probe: func() MachineProbe {
			p := fullProbe()
			p.Tools["git"] = ToolProbe{Name: "git", Found: true, Version: "2.20.1"}
			return p
		}(), id: "worktrees.git", status: CapMissing, reason: "older than 2.25", fix: "git 2.25"},
		{name: "build cache without reflink", probe: func() MachineProbe { p := fullProbe(); p.Reflink = false; return p }(), cfgs: []*ProjectConfig{deps},
			id: "worktrees.build-cache", status: CapDegraded, fix: "Btrfs"},
		{name: "services without compose", probe: func() MachineProbe { p := fullProbe(); p.Compose = ""; return p }(), cfgs: []*ProjectConfig{deps},
			id: "worktrees.services", status: CapMissing, root: true},
		{name: "hooks set", probe: fullProbe(), cfgs: []*ProjectConfig{deps}, id: "worktrees.hooks", status: CapActive},
		{name: "containers without docker", probe: without(fullProbe(), "docker"), cfgs: []*ProjectConfig{deps}, id: "worktrees.containers", status: CapMissing, root: true},
		{name: "claude default provider not logged in", probe: func() MachineProbe { p := fullProbe(); p.Logins = nil; return p }(),
			id: "providers.claude", status: CapMissing, fix: "barnraise account add claude"},
		{name: "codex used by a role", probe: fullProbe(), cfgs: []*ProjectConfig{deps}, id: "providers.codex", status: CapActive},
		{name: "claude unused when roles list others", probe: fullProbe(), cfgs: []*ProjectConfig{deps}, id: "providers.claude", status: CapAvailable},
		{name: "gemini not installed", probe: without(fullProbe(), "gemini"), id: "providers.gemini", status: CapMissing, reason: "not installed", fix: "npm install"},
		{name: "otel off", probe: fullProbe(), id: "telemetry.otel", status: CapAvailable, fix: "plugin, otel"},
		{name: "network auto without tailscale", probe: without(fullProbe(), "tailscale"), id: "cluster.network", status: CapDegraded, fallback: "local network"},
		{name: "network lan", probe: without(fullProbe(), "tailscale"), settings: MachineSettings{Network: "lan"}, id: "cluster.network", status: CapActive},
		{name: "network tailscale missing", probe: without(fullProbe(), "tailscale"), settings: MachineSettings{Network: "tailscale"}, id: "cluster.network", status: CapMissing},
		{name: "network plugin unset", probe: fullProbe(), settings: MachineSettings{Network: "plugin"}, id: "cluster.network", status: CapMissing, fix: "network_plugin"},
		{name: "network plugin missing", probe: fullProbe(), settings: MachineSettings{Network: "plugin", NetworkPlugin: "mynet"}, id: "cluster.network", status: CapMissing, fix: "mynet"},
		{name: "gitleaks missing", probe: without(fullProbe(), "gitleaks"), id: "security.gitleaks", status: CapDegraded, fallback: "built-in", tool: "gitleaks"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			items := byID(EvaluateCapabilities(CapabilityInput{Probe: tc.probe, Settings: tc.settings, Projects: tc.cfgs}))
			it, ok := items[tc.id]
			if !ok {
				t.Fatalf("no item %s", tc.id)
			}
			if it.Status != tc.status {
				t.Fatalf("status %s, want %s (%+v)", it.Status, tc.status, it)
			}
			if !strings.Contains(it.Reason, tc.reason) {
				t.Errorf("reason %q lacks %q", it.Reason, tc.reason)
			}
			if !strings.Contains(strings.ToLower(it.Fix), strings.ToLower(tc.fix)) {
				t.Errorf("fix %q lacks %q", it.Fix, tc.fix)
			}
			if it.Tool != tc.tool {
				t.Errorf("tool %q, want %q", it.Tool, tc.tool)
			}
			if it.NeedsRoot != tc.root {
				t.Errorf("needs_root %v, want %v", it.NeedsRoot, tc.root)
			}
			if !strings.Contains(it.Fallback, tc.fallback) {
				t.Errorf("fallback %q lacks %q", it.Fallback, tc.fallback)
			}
			if it.Title == "" || it.Group == "" || it.Kind == "" || it.Reason == "" {
				t.Errorf("incomplete item %+v", it)
			}
		})
	}
}

func TestDynamicCapabilities(t *testing.T) {
	cfg := &ProjectConfig{
		Plugins: map[string]PluginDef{
			"jira": {Command: "jira-plugin"}, "otel": {Command: "barnraise", Args: []string{"plugin", "otel"}}, "empty": {},
		},
		ACPProviders: map[string]ACPProviderDef{"goose": {Command: "goose"}},
	}
	p := without(fullProbe())
	p.Tools["goose"] = ToolProbe{Name: "goose", Found: true}
	items := byID(EvaluateCapabilities(CapabilityInput{Probe: p, Projects: []*ProjectConfig{cfg, cfg}}))
	if it := items["plugins.jira"]; it.Status != CapMissing || !strings.Contains(it.Fix, "jira-plugin") {
		t.Fatalf("plugin %+v", it)
	}
	if _, ok := items["plugins.otel"]; ok {
		t.Fatal("the otel plugin has its own entry")
	}
	if _, ok := items["plugins.empty"]; ok {
		t.Fatal("a plugin without a command is the configuration's error, not a capability")
	}
	if items["telemetry.otel"].Status != CapActive {
		t.Fatalf("otel %+v", items["telemetry.otel"])
	}
	if it := items["providers.acp.goose"]; it.Status != CapActive {
		t.Fatalf("acp %+v", it)
	}
	progs := CapabilityPrograms([]*ProjectConfig{cfg, nil}, MachineSettings{NetworkPlugin: "mynet", MicroVMRunner: "smolvm"})
	for _, want := range []string{"git", "jira-plugin", "goose", "mynet"} {
		if !contains(progs, want) {
			t.Errorf("programs %v lack %s", progs, want)
		}
	}
	if n := strings.Count(strings.Join(progs, " "), "smolvm"); n != 1 {
		t.Errorf("smolvm listed %d times", n)
	}
}

func TestEvaluateOrderAndAnyProject(t *testing.T) {
	off := &ProjectConfig{Merge: MergeDef{Structural: "off"}}
	auto := &ProjectConfig{Merge: MergeDef{Structural: "auto"}}
	items := EvaluateCapabilities(CapabilityInput{Probe: fullProbe(), Projects: []*ProjectConfig{off, auto}})
	if byID(items)["merge.structural"].Status != CapActive {
		t.Fatal("one project that turns it on is enough")
	}
	for i := 1; i < len(items); i++ {
		gi, gj := groupIndex(items[i-1].Group), groupIndex(items[i].Group)
		if gi > gj || (gi == gj && items[i-1].ID > items[i].ID) {
			t.Fatalf("not sorted at %d: %s %s", i, items[i-1].ID, items[i].ID)
		}
	}
}

func groupIndex(g string) int {
	for i, x := range CapGroups {
		if x == g {
			return i
		}
	}
	return -1
}

func TestReportCountsToolsAndAttention(t *testing.T) {
	p := without(fullProbe(), "mergiraf", "rtk")
	p.Tools["osv-scanner"] = ToolProbe{Name: "osv-scanner", Found: true, Managed: true, Path: "/data/tools/bin/osv-scanner"}
	r := BuildCapabilityReport("m1", CapabilityInput{Probe: p})
	if r.Machine != "m1" || r.Platform != "linux/amd64" || !r.ProbedAt.Equal(p.ProbedAt) {
		t.Fatalf("header %+v", r)
	}
	total := 0
	for _, s := range CapStatuses {
		total += r.Counts[s]
	}
	if total != len(r.Items) || r.Counts[CapDegraded] < 2 {
		t.Fatalf("counts %v for %d items", r.Counts, len(r.Items))
	}
	att := r.Attention()
	if len(att) == 0 {
		t.Fatal("degraded items that are on need attention")
	}
	for _, it := range att {
		if !it.On {
			t.Fatalf("%s is off", it.ID)
		}
	}
	tools := map[string]CapabilityTool{}
	for _, tl := range r.Tools {
		tools[tl.ID] = tl
	}
	if len(tools) != len(ToolRecipes()) {
		t.Fatalf("tools %v", r.Tools)
	}
	if tl := tools["osv-scanner"]; !tl.Managed || tl.Installed != "unknown" || !tl.Available || tl.Pinned != "2.7.1" {
		t.Fatalf("osv %+v", tl)
	}
	if tl := tools["mergiraf"]; tl.Installed != "" || tl.Path != "" {
		t.Fatalf("mergiraf %+v", tl)
	}
	// smolvm has no darwin/amd64 build.
	p.OS, p.Arch = "darwin", "amd64"
	for _, tl := range CapabilityTools(p) {
		if tl.ID == "smolvm" && tl.Available {
			t.Fatal("smolvm ships no build for darwin/amd64")
		}
	}
	items := byID(EvaluateCapabilities(CapabilityInput{Probe: without(p, "smolvm")}))
	if it := items["sandbox.microvm"]; it.Tool != "" || !strings.Contains(it.Fix, "smol-machines") {
		t.Fatalf("no recipe for this platform: the plain fix is shown, %+v", it)
	}
}

func TestVersions(t *testing.T) {
	cases := []struct {
		out, want string
	}{
		{"git version 2.43.0", "2.43.0"}, {"ast-grep 0.51.3\n", "0.51.3"}, {"Docker version 27.1.1, build 6312585", "27.1.1"},
		{"1.2", "1.2"}, {"no version", ""},
	}
	for _, c := range cases {
		if got := ParseToolVersion(c.out); got != c.want {
			t.Errorf("ParseToolVersion(%q) = %q", c.out, got)
		}
	}
	at := []struct {
		have, min string
		ok        bool
	}{
		{"2.43.0", "2.25", true}, {"2.25", "2.25", true}, {"2.25.0", "2.25", true}, {"2.20.1", "2.25", false},
		{"10.0", "9.9.9", true}, {"1.2", "1.2.1", false}, {"garbage", "1.0", true}, {"1.0", "", true},
	}
	for _, c := range at {
		if got := VersionAtLeast(c.have, c.min); got != c.ok {
			t.Errorf("VersionAtLeast(%q, %q) = %v", c.have, c.min, got)
		}
	}
	if !strings.Contains(strings.Join(ProbedToolArgs("tailscale"), " "), "version") || ProbedToolArgs("other")[0] != "--version" {
		t.Fatal("version args")
	}
	if sentence("") != "" || sentence("x.") != "x." || sentence(" y ") != "y." {
		t.Fatal("sentence")
	}
}

func TestToolRecipes(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range ToolRecipes() {
		if seen[r.ID] || r.Version == "" || r.Bin == "" || r.Home == "" || r.License == "" {
			t.Fatalf("recipe %+v", r)
		}
		seen[r.ID] = true
		for _, a := range r.Assets {
			if len(a.SHA256) != 64 || !strings.HasPrefix(a.URL, "https://") || !strings.Contains(a.URL, r.Version) {
				t.Fatalf("%s asset %+v", r.ID, a)
			}
			switch a.Format {
			case FormatTarGz, FormatZip, FormatRaw:
			default:
				t.Fatalf("%s format %q", r.ID, a.Format)
			}
		}
		if _, ok := r.Asset("linux", "amd64"); !ok {
			t.Fatalf("%s has no linux/amd64 build", r.ID)
		}
	}
	if _, ok := LookupToolRecipe("nope"); ok {
		t.Fatal("unknown recipe found")
	}
	if r, _ := LookupToolRecipe("smolvm"); r.BinPath() != "smolvm" || r.GuestTree == "" {
		t.Fatal("smolvm recipe")
	}
	if len(ToolIDs()) != len(ToolRecipes()) {
		t.Fatal("ids")
	}
}

func TestUnknownRequirement(t *testing.T) {
	if ok, why, _ := checkRequirement(Requirement{Kind: "nope"}, MachineProbe{}, MachineSettings{}); ok || why == "" {
		t.Fatal("an unknown requirement never holds")
	}
	if machineSetting(MachineSettings{}, "nope") != "" {
		t.Fatal("unknown setting")
	}
	if (MachineProbe{}).Tool("x").Found {
		t.Fatal("tool")
	}
}
