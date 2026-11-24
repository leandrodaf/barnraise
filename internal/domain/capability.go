package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Capabilities (ADR-0046): what Barnraise does on one machine, what is
// switched off, what runs on a fallback and why, and the exact fix. The
// catalog (capability_catalog.go) declares every feature with its
// requirements; EvaluateCapabilities turns a probe of the machine and the
// projects' configuration into one status per feature. Nothing here runs
// a program: the probe is an outbound adapter.

// Capability statuses.
const (
	// CapActive: switched on and running as designed.
	CapActive = "active"
	// CapAvailable: everything it needs is here, but it is switched off.
	CapAvailable = "available"
	// CapDegraded: switched on and running on a fallback.
	CapDegraded = "degraded"
	// CapMissing: it cannot run on this machine.
	CapMissing = "missing"
	// CapUnsupported: not for this operating system.
	CapUnsupported = "unsupported"
)

// CapStatuses are the statuses, in the order reports count them.
var CapStatuses = []string{CapActive, CapAvailable, CapDegraded, CapMissing, CapUnsupported}

// Capability groups.
const (
	CapGroupSecurity  = "security"
	CapGroupIsolation = "isolation"
	CapGroupMerge     = "merge"
	CapGroupChecks    = "checks"
	CapGroupContext   = "context"
	CapGroupTokens    = "tokens"
	CapGroupWorktrees = "worktrees"
	CapGroupProviders = "providers"
	CapGroupTelemetry = "telemetry"
	CapGroupCluster   = "cluster"
	CapGroupPlugins   = "plugins"
)

// CapGroups are the groups in display order.
var CapGroups = []string{CapGroupSecurity, CapGroupIsolation, CapGroupMerge, CapGroupChecks, CapGroupContext,
	CapGroupTokens, CapGroupWorktrees, CapGroupProviders, CapGroupTelemetry, CapGroupCluster, CapGroupPlugins}

// Capability kinds.
const (
	CapNative   = "native"   // part of Barnraise
	CapOptional = "optional" // uses an external tool or OS feature when present
	CapPlugin   = "plugin"   // a plugin the owner configures
)

// Requirement kinds.
const (
	ReqTool     = "tool"     // a program on the PATH, with an optional minimum version
	ReqLandlock = "landlock" // the kernel's Landlock ABI (Min is the ABI number)
	ReqBwrap    = "bwrap"    // bubblewrap can start a sandbox here
	ReqKVM      = "kvm"      // /dev/kvm exists and this user may open it
	ReqReflink  = "reflink"  // the data folder's filesystem clones files (copy-on-write)
	ReqEgressCA = "egress-ca"
	ReqLogin    = "login"    // a provider account is logged in on this machine
	ReqCompose  = "compose"  // docker compose v2 answers
	ReqSetting  = "setting"  // a machine setting (daemon.yaml) has a value
	ReqConfiner = "confiner" // Landlock or a working microVM confines commands
)

// Requirement is one thing a capability needs.
type Requirement struct {
	Kind string
	Name string
	// Min is the lowest version of a tool, or the lowest Landlock ABI.
	Min string
	// OS limits the requirement to these systems (empty: all).
	OS []string
	// Fix is the exact command or configuration that meets it.
	Fix string
	// Tool is the recipe `barnraise tools install` uses to meet it.
	Tool string
	// Root: the fix needs root; Barnraise shows it and never runs it.
	Root bool
	// Fallback, when set, is what runs while this requirement is missing:
	// the capability is degraded rather than missing.
	Fallback string
	// Auto: Barnraise provides it itself once the capability is switched
	// on (the egress CA is created when the proxy starts), or it is set
	// together with the switch: it is checked only once the capability is on.
	Auto bool
}

// Capability is one feature of the catalog.
type Capability struct {
	ID    string
	Title string
	Group string
	Kind  string
	// OS limits the capability to these systems (empty: every system).
	OS       []string
	Requires []Requirement
	// Fallback is what happens while a requirement without its own
	// fallback is missing; empty means the capability simply cannot run.
	Fallback string
	// On reports whether a project (nil: a new project's defaults) or the
	// machine turns it on; nil means always on.
	On func(cfg *ProjectConfig, m MachineSettings) bool
	// Enable is how to switch it on.
	Enable string
}

// ToolProbe is what the probe found about one program.
type ToolProbe struct {
	Name    string `json:"name"`
	Found   bool   `json:"found"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
	// Managed: the program is one Barnraise installed (tools/bin).
	Managed bool   `json:"managed,omitempty"`
	Err     string `json:"err,omitempty"`
}

// MachineProbe is what a machine has, as an outbound adapter found it.
type MachineProbe struct {
	OS       string               `json:"os"`
	Arch     string               `json:"arch"`
	ProbedAt time.Time            `json:"probed_at"`
	Tools    map[string]ToolProbe `json:"tools,omitempty"`
	// LandlockABI is 0 when the kernel has none; LandlockReason says why.
	LandlockABI    int    `json:"landlock_abi"`
	LandlockReason string `json:"landlock_reason,omitempty"`
	BwrapUsable    bool   `json:"bwrap_usable"`
	BwrapReason    string `json:"bwrap_reason,omitempty"`
	KVMPresent     bool   `json:"kvm_present"`
	KVMAccess      bool   `json:"kvm_access"`
	// Reflink: the data folder (where worktrees live) clones files.
	Reflink       bool   `json:"reflink"`
	ReflinkReason string `json:"reflink_reason,omitempty"`
	// Compose is the version of `docker compose` ("" when it does not answer).
	Compose  string          `json:"compose,omitempty"`
	EgressCA bool            `json:"egress_ca"`
	Logins   map[string]bool `json:"logins,omitempty"`
}

// Tool returns the probe of a program.
func (p MachineProbe) Tool(name string) ToolProbe {
	if t, ok := p.Tools[name]; ok {
		return t
	}
	return ToolProbe{Name: name}
}

// Platform is os/arch.
func (p MachineProbe) Platform() string { return p.OS + "/" + p.Arch }

// MachineSettings are the machine's own switches (daemon.yaml and the
// daemon's flags).
type MachineSettings struct {
	// Egress is the egress proxy's address ("" when it does not run).
	Egress string `json:"egress,omitempty"`
	// IsolationBackend is daemon.yaml's isolation.backend ("" or microvm).
	IsolationBackend string `json:"isolation_backend,omitempty"`
	MicroVMImage     string `json:"microvm_image,omitempty"`
	// MicroVMRunner is the runner program (default smolvm).
	MicroVMRunner string `json:"microvm_runner,omitempty"`
	// Network is daemon.yaml's network ("" = auto).
	Network string `json:"network,omitempty"`
	// NetworkPlugin is the program of `network: plugin`.
	NetworkPlugin string `json:"network_plugin,omitempty"`
}

// CapabilityInput is everything the evaluation reads.
type CapabilityInput struct {
	Probe    MachineProbe
	Settings MachineSettings
	// Projects are the projects evaluated; none means a new project's defaults.
	Projects []*ProjectConfig
	// Installable reports whether a tool recipe ships a build for this
	// machine (nil: use the built-in recipes).
	Installable func(tool string) bool
}

// CapabilityItem is the status of one capability on one machine.
type CapabilityItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Group    string `json:"group"`
	Kind     string `json:"kind"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
	Fallback string `json:"fallback,omitempty"`
	Fix      string `json:"fix,omitempty"`
	// Tool is the recipe that fixes it with `barnraise tools install`.
	Tool      string `json:"tool,omitempty"`
	Version   string `json:"version,omitempty"`
	NeedsRoot bool   `json:"needs_root,omitempty"`
	// On: a project or the machine switched it on (or it is always on).
	On bool `json:"on,omitempty"`
}

// Attention reports whether the owner should look at it: switched on
// and not running as designed.
func (c CapabilityItem) Attention() bool {
	return c.On && (c.Status == CapDegraded || c.Status == CapMissing)
}

// CapabilityTool is one installable tool on one machine.
type CapabilityTool struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Installed string `json:"installed,omitempty"`
	Pinned    string `json:"pinned"`
	Managed   bool   `json:"managed,omitempty"`
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
}

// CapabilityReport is one machine's answer.
type CapabilityReport struct {
	Machine  string           `json:"machine"`
	Platform string           `json:"platform"`
	ProbedAt time.Time        `json:"probed_at"`
	Items    []CapabilityItem `json:"items"`
	Tools    []CapabilityTool `json:"tools,omitempty"`
	Counts   map[string]int   `json:"counts"`
	Error    string           `json:"error,omitempty"`
}

// CountCapabilities counts items by status.
func CountCapabilities(items []CapabilityItem) map[string]int {
	out := map[string]int{}
	for _, it := range items {
		out[it.Status]++
	}
	return out
}

// Attention returns the items the owner should look at.
func (r CapabilityReport) Attention() []CapabilityItem {
	var out []CapabilityItem
	for _, it := range r.Items {
		if it.Attention() {
			out = append(out, it)
		}
	}
	return out
}

// BuildCapabilityReport evaluates and fills a whole report.
func BuildCapabilityReport(machine string, in CapabilityInput) CapabilityReport {
	items := EvaluateCapabilities(in)
	return CapabilityReport{Machine: machine, Platform: in.Probe.Platform(), ProbedAt: in.Probe.ProbedAt, Items: items,
		Tools: CapabilityTools(in.Probe), Counts: CountCapabilities(items)}
}

// CapabilityTools lists the installable tools and where each stands here.
func CapabilityTools(p MachineProbe) []CapabilityTool {
	var out []CapabilityTool
	for _, r := range ToolRecipes() {
		_, ok := r.Asset(p.OS, p.Arch)
		t := CapabilityTool{ID: r.ID, Title: r.Title, Pinned: r.Version, Available: ok}
		if tp := p.Tool(r.Bin); tp.Found {
			t.Installed, t.Managed, t.Path = def0(tp.Version, "unknown"), tp.Managed, tp.Path
		}
		out = append(out, t)
	}
	return out
}

// EvaluateCapabilities is the status of every capability of the catalog
// on the probed machine, sorted by group and id.
func EvaluateCapabilities(in CapabilityInput) []CapabilityItem {
	if in.Installable == nil {
		in.Installable = func(tool string) bool {
			r, ok := LookupToolRecipe(tool)
			if !ok {
				return false
			}
			_, ok = r.Asset(in.Probe.OS, in.Probe.Arch)
			return ok
		}
	}
	cfgs := in.Projects
	if len(cfgs) == 0 {
		cfgs = []*ProjectConfig{nil}
	}
	var out []CapabilityItem
	for _, c := range CapabilityCatalog(in.Projects, in.Settings) {
		out = append(out, evaluate(c, in, cfgs))
	}
	order := map[string]int{}
	for i, g := range CapGroups {
		order[g] = i
	}
	sort.SliceStable(out, func(i, j int) bool {
		if order[out[i].Group] != order[out[j].Group] {
			return order[out[i].Group] < order[out[j].Group]
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func evaluate(c Capability, in CapabilityInput, cfgs []*ProjectConfig) CapabilityItem {
	it := CapabilityItem{ID: c.ID, Title: c.Title, Group: c.Group, Kind: c.Kind}
	if len(c.OS) > 0 && !contains(c.OS, in.Probe.OS) {
		it.Status = CapUnsupported
		it.Reason = fmt.Sprintf("not for %s: it runs on %s only", def0(in.Probe.OS, "this system"), strings.Join(c.OS, " and "))
		return it
	}
	on := c.On == nil
	for _, cfg := range cfgs {
		if on {
			break
		}
		on = c.On(cfg, in.Settings)
	}
	it.On = on
	var hard, soft *Requirement
	var hardWhy, softWhy string
	for i := range c.Requires {
		r := c.Requires[i]
		if len(r.OS) > 0 && !contains(r.OS, in.Probe.OS) {
			continue
		}
		ok, why, ver := checkRequirement(r, in.Probe, in.Settings)
		if ver != "" && it.Version == "" {
			it.Version = ver
		}
		if ok || (r.Auto && !on) {
			continue
		}
		if r.Fallback == "" && hard == nil {
			hard, hardWhy = &c.Requires[i], why
		}
		if r.Fallback != "" && soft == nil {
			soft, softWhy = &c.Requires[i], why
		}
	}
	fixFrom := func(r *Requirement) {
		it.Fix, it.NeedsRoot = r.Fix, r.Root
		if r.Tool != "" && in.Installable(r.Tool) {
			it.Tool = r.Tool
			it.Fix, it.NeedsRoot = "barnraise tools install "+r.Tool, false
		}
	}
	switch {
	case hard != nil && on && c.Fallback != "":
		it.Status, it.Reason, it.Fallback = CapDegraded, sentence(hardWhy), c.Fallback
		fixFrom(hard)
	case hard != nil:
		it.Status, it.Reason, it.Fallback = CapMissing, sentence(hardWhy), c.Fallback
		if !on {
			it.Reason = sentence("Switched off, and it could not run here: " + hardWhy)
		}
		fixFrom(hard)
	case !on:
		it.Status, it.Reason, it.Fix = CapAvailable, "Everything it needs is here; it is switched off.", c.Enable
		if soft != nil {
			it.Reason = sentence("Switched off; once on it would run on a fallback: " + softWhy)
		}
	case soft != nil:
		it.Status, it.Reason, it.Fallback = CapDegraded, sentence(softWhy), soft.Fallback
		fixFrom(soft)
	default:
		it.Status, it.Reason = CapActive, "Switched on and running."
		if c.On == nil {
			it.Reason = "Always on."
		}
	}
	return it
}

// sentence ends a reason with a period. The first letter is left as it is:
// a reason often starts with a program's name (mergiraf, rtk).
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

// checkRequirement reports whether r holds, why not, and a version seen.
func checkRequirement(r Requirement, p MachineProbe, m MachineSettings) (bool, string, string) {
	switch r.Kind {
	case ReqTool:
		t := p.Tool(r.Name)
		if !t.Found {
			return false, r.Name + " is not installed (or not on the PATH)", ""
		}
		if r.Min != "" && t.Version != "" && !VersionAtLeast(t.Version, r.Min) {
			return false, fmt.Sprintf("%s %s is older than %s", r.Name, t.Version, r.Min), t.Version
		}
		return true, "", def0(t.Version, "installed")
	case ReqLandlock:
		min, _ := strconv.Atoi(def0(r.Min, "1"))
		switch {
		case p.LandlockABI <= 0:
			return false, def0(p.LandlockReason, "The kernel has no Landlock"), ""
		case p.LandlockABI < min:
			return false, fmt.Sprintf("The kernel's Landlock ABI is %d; %d or later is needed", p.LandlockABI, min), "ABI " + strconv.Itoa(p.LandlockABI)
		}
		return true, "", "ABI " + strconv.Itoa(p.LandlockABI)
	case ReqBwrap:
		if !p.BwrapUsable {
			return false, def0(p.BwrapReason, "bubblewrap cannot start a sandbox here"), ""
		}
		return true, "", ""
	case ReqKVM:
		switch {
		case !p.KVMPresent:
			return false, "/dev/kvm is missing: the machine has no hardware virtualization (or it is off in the firmware)", ""
		case !p.KVMAccess:
			return false, "/dev/kvm exists but this user cannot open it", ""
		}
		return true, "", ""
	case ReqReflink:
		if !p.Reflink {
			return false, def0(p.ReflinkReason, "The filesystem of the data folder cannot clone files"), ""
		}
		return true, "", ""
	case ReqEgressCA:
		if !p.EgressCA {
			return false, "The egress proxy has no local CA", ""
		}
		return true, "", ""
	case ReqLogin:
		if !p.Logins[r.Name] {
			return false, "No " + r.Name + " account is logged in on this machine", ""
		}
		return true, "", ""
	case ReqCompose:
		if p.Compose == "" {
			return false, "docker compose does not answer here", ""
		}
		return true, "", p.Compose
	case ReqConfiner:
		if p.LandlockABI > 0 || (m.IsolationBackend == "microvm" && p.Tool(def0(m.MicroVMRunner, "smolvm")).Found && (p.OS != "linux" || p.KVMAccess)) {
			return true, "", ""
		}
		return false, "Neither Landlock nor a microVM can confine commands here", ""
	case ReqSetting:
		if machineSetting(m, r.Name) == "" {
			return false, r.Name + " is not set in daemon.yaml", ""
		}
		return true, "", ""
	}
	return false, "Unknown requirement " + r.Kind, ""
}

func machineSetting(m MachineSettings, name string) string {
	switch name {
	case "isolation.microvm.image":
		return m.MicroVMImage
	case "network_plugin.command":
		return m.NetworkPlugin
	}
	return ""
}

var versionRe = regexp.MustCompile(`\d+(?:\.\d+)+`)

// ParseToolVersion finds the first dotted version in a program's
// `--version` output ("git version 2.43.0" -> "2.43.0").
func ParseToolVersion(out string) string {
	return versionRe.FindString(out)
}

// VersionAtLeast compares dotted versions numerically; a version that
// cannot be read is taken as recent enough.
func VersionAtLeast(have, min string) bool {
	h, m := versionParts(have), versionParts(min)
	if h == nil || m == nil {
		return true
	}
	for i := 0; i < len(m) || i < len(h); i++ {
		var a, b int
		if i < len(h) {
			a = h[i]
		}
		if i < len(m) {
			b = m[i]
		}
		if a != b {
			return a > b
		}
	}
	return true
}

func versionParts(v string) []int {
	v = ParseToolVersion(v)
	if v == "" {
		return nil
	}
	var out []int
	for _, s := range strings.Split(v, ".") {
		n, err := strconv.Atoi(s)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

// CapabilityPrograms are the programs the projects' configuration names
// (plugins, ACP agents): the probe looks them up with the catalog's tools.
func CapabilityPrograms(cfgs []*ProjectConfig, m MachineSettings) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, t := range ProbedTools {
		add(t.Name)
	}
	for _, cfg := range cfgs {
		if cfg == nil {
			continue
		}
		for _, name := range sortedKeys(cfg.Plugins) {
			add(cfg.Plugins[name].Command)
		}
		for _, name := range sortedKeys(cfg.ACPProviders) {
			add(cfg.ACPProviders[name].Command)
		}
	}
	add(m.NetworkPlugin)
	add(m.MicroVMRunner)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ProbedTool is a program the probe looks for, and how it reads its version.
type ProbedTool struct {
	Name string
	Args []string
}

// ProbedTools are the programs of the catalog.
var ProbedTools = []ProbedTool{
	{"git", []string{"--version"}}, {"docker", []string{"--version"}}, {"podman", []string{"--version"}},
	{"claude", []string{"--version"}}, {"codex", []string{"--version"}}, {"gemini", []string{"--version"}},
	{"mergiraf", []string{"--version"}}, {"rtk", []string{"--version"}}, {"osv-scanner", []string{"--version"}},
	{"ast-grep", []string{"--version"}}, {"smolvm", []string{"--version"}}, {"bwrap", []string{"--version"}},
	{"tailscale", []string{"version"}}, {"gitleaks", []string{"version"}}, {"godot", []string{"--version"}},
}

// ProbedToolArgs returns the version arguments of a program ("--version"
// for any program the catalog does not list).
func ProbedToolArgs(name string) []string {
	for _, t := range ProbedTools {
		if t.Name == name {
			return t.Args
		}
	}
	return []string{"--version"}
}
