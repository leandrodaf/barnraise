package domain

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Sandbox profiles: what a confined command may touch, by the kind of
// command it is. Each is a fixed, built-in set; barnraise.yaml picks one per
// check and for the worktree setup, and may add paths and ports
// (`sandbox:`), never remove what a profile denies.
const (
	// SandboxSetup installs dependencies: network to the registries, writes
	// to the worktree and the package stores.
	SandboxSetup = "setup"
	// SandboxBuild compiles: like setup, plus the built-in command deny rules.
	SandboxBuild = "build"
	// SandboxCheck runs tests: no network beyond loopback services (through
	// the egress proxy when the daemon runs one).
	SandboxCheck = "check"
	// SandboxAgent is an agent session confined by Barnraise itself (when
	// the provider's own sandbox cannot start on the machine).
	SandboxAgent = "agent"
	// SandboxOpen confines the filesystem only; the network stays open.
	SandboxOpen = "open"
	// SandboxNone runs the command unconfined, as before profiles existed.
	SandboxNone = "none"
)

// SandboxNet is how much network a profile allows.
type SandboxNet string

const (
	// NetOpen does not restrict the network.
	NetOpen SandboxNet = "open"
	// NetRegistries allows outbound TCP to the web ports (80, 443), the
	// egress proxy and loopback services.
	NetRegistries SandboxNet = "registries"
	// NetLocal allows outbound TCP only to the egress proxy and to loopback
	// services: the ports listed in `sandbox.ports` and the ephemeral range
	// a test server binds to.
	NetLocal SandboxNet = "local"
)

// SandboxProfile is one built-in profile.
type SandboxProfile struct {
	Name         string     `json:"name"`
	Network      SandboxNet `json:"network"`
	Confined     bool       `json:"confined"`
	DenyCommands []string   `json:"deny_commands,omitempty"`
	About        string     `json:"about"`
}

// Built-in command deny rules of the profiles (regular expressions over the
// normalized command text). They are matched before the command starts.
var (
	// denyDestroy is refused in every confined profile: wiping the
	// filesystem, the home or a device, and fork bombs.
	denyDestroy = []string{
		`\brm\s+(-\S*\s+)*-[a-zA-Z]*[rR][a-zA-Z]*\s+(-\S*\s+)*(/|~|\$HOME|\$\{HOME\})/?\*?(\s|$)`,
		`\brm\s+(-\S*\s+)*-[a-zA-Z]*[rR][a-zA-Z]*\s+(-\S*\s+)*\.\.(/\.\.)*/?(\s|$)`,
		`\bmkfs(\.\w+)?\b`,
		`\bdd\b.*\bof=/dev/(sd|nvme|hd|vd|mmcblk)`,
		`:\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}`,
		`\bchmod\s+(-R\s+)?0?777\s+/(\s|$)`,
	}
	// denyFetchRun is refused where nothing should download and run code.
	denyFetchRun = []string{
		`(curl|wget)\b[^|]*\|\s*(sudo\s+)?(ba|z|da)?sh\b`,
		`\b(ba|z)?sh\s+<\(\s*(curl|wget)`,
		`\bsudo\b`,
	}
	// denyOutbound is refused in checks: they verify, they do not publish.
	denyOutbound = []string{
		`\bgit\s+push\b`,
		`\b(ssh|scp|sftp|rsync)\b[^|;&]*\s[^\s@]+@\S+`,
		`\bnc\b.*\s-e\s`,
	}
)

var sandboxProfiles = map[string]SandboxProfile{
	SandboxSetup: {Name: SandboxSetup, Network: NetRegistries, Confined: true, DenyCommands: denyDestroy,
		About: "install dependencies: registries reachable, writes to the worktree and the package stores"},
	SandboxBuild: {Name: SandboxBuild, Network: NetRegistries, Confined: true, DenyCommands: concat(denyDestroy, denyFetchRun),
		About: "compile: registries reachable, no download-and-run, writes to the worktree and the package stores"},
	SandboxCheck: {Name: SandboxCheck, Network: NetLocal, Confined: true, DenyCommands: concat(denyDestroy, denyFetchRun, denyOutbound),
		About: "test: loopback services and the egress proxy only, writes to the worktree and the package stores"},
	SandboxAgent: {Name: SandboxAgent, Network: NetLocal, Confined: true, DenyCommands: concat(denyDestroy, denyFetchRun),
		About: "agent session: the provider's API through the egress proxy, writes to the worktree, its .git and the account"},
	SandboxOpen: {Name: SandboxOpen, Network: NetOpen, Confined: true, DenyCommands: denyDestroy,
		About: "filesystem confined, network open"},
	SandboxNone: {Name: SandboxNone, Network: NetOpen, Confined: false,
		About: "not confined"},
}

func concat(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// SandboxProfileNames lists the built-in profiles in order.
func SandboxProfileNames() []string {
	names := make([]string, 0, len(sandboxProfiles))
	for n := range sandboxProfiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// LookupSandboxProfile returns a built-in profile.
func LookupSandboxProfile(name string) (SandboxProfile, error) {
	p, ok := sandboxProfiles[name]
	if !ok {
		return SandboxProfile{}, fmt.Errorf("sandbox profile %q does not exist (known: %s)", name, strings.Join(SandboxProfileNames(), ", "))
	}
	p.DenyCommands = append([]string(nil), p.DenyCommands...)
	return p, nil
}

// AgentDenyCommands are the agent profile's command rules, added to the
// policy the hook applies in every session.
func AgentDenyCommands() []string {
	return append([]string(nil), sandboxProfiles[SandboxAgent].DenyCommands...)
}

// SandboxDef is `sandbox:` in barnraise.yaml (plus `worktree.sandbox`): the
// profile of the worktree setup and what every confined command of the
// project may reach on top of its profile.
type SandboxDef struct {
	// Setup is the profile of worktree.setup (default setup).
	Setup string `json:"setup,omitempty"`
	// Read and Write are more paths (absolute or ~/) readable or writable.
	Read  []string `json:"read,omitempty"`
	Write []string `json:"write,omitempty"`
	// Ports are more TCP ports a confined command may connect to (a local
	// database on 5432, an emulator on 5554).
	Ports []int `json:"ports,omitempty"`
	// DenyCommands are the project's own command rules (policy.deny_commands
	// and sandbox.deny_commands), added to every confined profile.
	DenyCommands []string `json:"deny_commands,omitempty"`
}

// CheckSandbox is the profile of a check: its own, otherwise check.
func CheckSandbox(d CheckDef) string { return def1(d.Sandbox, SandboxCheck) }

// SetupSandbox is the profile of the worktree setup.
func (s SandboxDef) SetupSandbox() string { return def1(s.Setup, SandboxSetup) }

func def1(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}

// Validate reports mistakes in the project's sandbox keys.
func (s SandboxDef) Validate() []error {
	var errs []error
	if s.Setup != "" {
		if _, err := LookupSandboxProfile(s.Setup); err != nil {
			errs = append(errs, fmt.Errorf("worktree.sandbox: %w", err))
		}
	}
	for _, p := range append(append([]string(nil), s.Read...), s.Write...) {
		if !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "~/") {
			errs = append(errs, fmt.Errorf("sandbox: path %q must be absolute or start with ~/", p))
		}
	}
	for _, p := range s.Ports {
		if p < 1 || p > 65535 {
			errs = append(errs, fmt.Errorf("sandbox.ports: %d is not a TCP port", p))
		}
	}
	for _, rx := range s.DenyCommands {
		if _, err := regexp.Compile(rx); err != nil {
			errs = append(errs, fmt.Errorf("sandbox.deny_commands: %q: %w", rx, err))
		}
	}
	return errs
}

// PortRange is an inclusive range of TCP ports.
type PortRange struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// Confinement is what one confined command may do, with every path
// resolved. The OS layer (Landlock, a microVM) applies it; the command
// rules are matched before the command starts.
type Confinement struct {
	Profile string `json:"profile"`
	// Work are the paths the command works in (worktree, its .git, the
	// agent's account): writable, and kept even inside a denied folder.
	Work []string `json:"work"`
	// ReadWrite are the package stores and scratch folders.
	ReadWrite []string `json:"read_write,omitempty"`
	// ReadOnly are the system, the toolchains and the dependencies.
	ReadOnly []string `json:"read_only,omitempty"`
	// Tools are the folders of the tools Barnraise installed (ADR-0045):
	// read-only, and kept even inside a denied folder (they live in the data
	// folder, which is denied).
	Tools []string `json:"tools,omitempty"`
	// DenyRead are folders and files never granted, even when a parent is.
	DenyRead []string `json:"deny_read,omitempty"`
	// Network: open, registries or local.
	Network SandboxNet `json:"network"`
	// Connect are the TCP ports outbound connections may use.
	Connect []int `json:"connect,omitempty"`
	// Ephemeral adds the machine's ephemeral port range to Connect (loopback
	// test servers bind there).
	Ephemeral bool `json:"ephemeral,omitempty"`
	// DenyCommands are matched against Command before it starts.
	DenyCommands []string `json:"deny_commands,omitempty"`
	// Command is the text matched against DenyCommands ("" = the arguments).
	Command string `json:"command,omitempty"`
	// Hosts are the hosts a layer that filters by name (a microVM's
	// allow-list) lets through; Landlock filters by port only.
	Hosts []string `json:"hosts,omitempty"`
	// Env are the names of the variables the command needs; a layer that
	// does not inherit the environment (a microVM) passes them by name.
	Env []string `json:"env,omitempty"`
}

// ConfineInput is what BuildConfinement needs about the command.
type ConfineInput struct {
	Profile  string
	Home     string
	Work     []string // worktree first
	TmpDir   string   // the machine's temporary folder (default /tmp)
	Proxy    int      // the egress proxy's port, 0 when none runs
	DenyRead []string // the project's deny_read globs
	Project  SandboxDef
	Command  string
	Hosts    []string
	Env      []string
	Tools    []string // folders of the tools Barnraise installed, read-only
}

// systemRead is readable by every confined command: the system, its
// configuration and the runtime folders resolvers and toolchains use.
var systemRead = []string{"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32", "/etc", "/opt", "/nix", "/snap",
	"/run", "/var/lib", "/var/cache", "/proc", "/sys", "/usr/local", "/home/linuxbrew", "/opt/homebrew"}

// homeRead are toolchains and their settings in the home, read only.
var homeRead = []string{"~/.local/bin", "~/.local/lib", "~/.local/share/claude", "~/.local/share/pnpm", "~/.local/share/mise",
	"~/.nvm", "~/.volta", "~/.bun", "~/.deno", "~/.pyenv", "~/.rbenv", "~/.rustup", "~/.sdkman", "~/.asdf", "~/.jdks",
	"~/.dotnet", "~/.ghcup", "~/.nix-profile", "~/.npm-global", "~/sdk", "~/flutter", "~/Android", "~/Library/Android",
	"~/.gitconfig", "~/.config/git", "~/.config/go", "~/.terraform.d/plugins"}

// homeStores are package stores and caches: writable, shared by worktrees.
var homeStores = []string{"~/.cache", "~/go", "~/.npm", "~/.pnpm-store", "~/.yarn", "~/.gradle", "~/.m2", "~/.cargo",
	"~/.ivy2", "~/.sbt", "~/.pub-cache", "~/.nuget", "~/.bundle", "~/.gem", "~/.android", "~/.konan", "~/.expo"}

// alwaysDenied are credentials inside the stores above.
var alwaysDenied = []string{"~/.cargo/credentials", "~/.cargo/credentials.toml", "~/.gradle/gradle.properties",
	"~/.m2/settings.xml", "~/.m2/settings-security.xml", "~/.config/barnraise", "~/.local/share/barnraise", "~/.cache/barnraise"}

// BuildConfinement resolves a profile for one command. Network "local"
// without an egress proxy falls back to "registries": without the proxy
// nothing tells a registry apart from any other host on port 443, and a
// check that downloads its modules would fail where it used to pass.
func BuildConfinement(in ConfineInput) (Confinement, error) {
	prof, err := LookupSandboxProfile(def1(in.Profile, SandboxCheck))
	if err != nil {
		return Confinement{}, err
	}
	expand := func(p string) string {
		if in.Home != "" && (p == "~" || strings.HasPrefix(p, "~/")) {
			return path.Join(in.Home, p[1:])
		}
		return path.Clean(p)
	}
	c := Confinement{Profile: prof.Name, Network: prof.Network, Command: in.Command, Hosts: in.Hosts, Env: in.Env}
	for _, t := range in.Tools {
		if t != "" {
			c.Tools = appendUnique(c.Tools, path.Clean(t))
		}
	}
	for _, w := range in.Work {
		if w != "" {
			c.Work = appendUnique(c.Work, path.Clean(w))
		}
	}
	tmp := def1(in.TmpDir, "/tmp")
	c.ReadWrite = appendUnique(c.ReadWrite, path.Clean(tmp), "/tmp", "/var/tmp", "/dev")
	for _, s := range homeStores {
		c.ReadWrite = appendUnique(c.ReadWrite, expand(s))
	}
	for _, s := range in.Project.Write {
		c.ReadWrite = appendUnique(c.ReadWrite, expand(s))
	}
	c.ReadOnly = appendUnique(c.ReadOnly, systemRead...)
	for _, s := range homeRead {
		c.ReadOnly = appendUnique(c.ReadOnly, expand(s))
	}
	for _, s := range in.Project.Read {
		c.ReadOnly = appendUnique(c.ReadOnly, expand(s))
	}
	for _, d := range alwaysDenied {
		c.DenyRead = appendUnique(c.DenyRead, expand(d))
	}
	for _, g := range in.DenyRead {
		if root := GlobRoot(g); root != "" {
			c.DenyRead = appendUnique(c.DenyRead, expand(root))
		}
	}
	if c.Network == NetLocal && in.Proxy == 0 {
		c.Network = NetRegistries
	}
	switch c.Network {
	case NetRegistries:
		c.Connect = []int{80, 443}
		c.Ephemeral = true
	case NetLocal:
		c.Ephemeral = true
	}
	if c.Network != NetOpen {
		if in.Proxy > 0 {
			c.Connect = appendUniqueInt(c.Connect, in.Proxy)
		}
		c.Connect = appendUniqueInt(c.Connect, in.Project.Ports...)
		sort.Ints(c.Connect)
	}
	c.DenyCommands = append(prof.DenyCommands, in.Project.DenyCommands...)
	return c, nil
}

// GlobRoot is the literal folder or file a deny_read glob starts with, when
// it is absolute or under ~ ("~/.ssh/**" is "~/.ssh"); "" for globs that
// match anywhere ("**/*.jks"), which only the hook can apply.
func GlobRoot(g string) string {
	if !strings.HasPrefix(g, "/") && !strings.HasPrefix(g, "~") {
		return ""
	}
	i := strings.IndexAny(g, "*?[{")
	if i < 0 {
		return strings.TrimSuffix(g, "/")
	}
	root := g[:i]
	if !strings.HasSuffix(root, "/") {
		// "~/.env*": the folder that holds the match is the root.
		root = path.Dir(root)
	}
	root = strings.TrimSuffix(root, "/")
	if root == "" || root == "~" {
		return "" // the whole home or filesystem: too wide to deny as a folder
	}
	return root
}

// DeniedCommand returns the rule a command breaks, or "".
func (c Confinement) DeniedCommand(command string) string {
	cmd := normalizeCommand(command)
	for _, rx := range c.DenyCommands {
		if re := compiled(rx); re != nil && re.MatchString(cmd) {
			return rx
		}
	}
	return ""
}

func appendUnique(list []string, vals ...string) []string {
	for _, v := range vals {
		if !Contains(list, v) {
			list = append(list, v)
		}
	}
	return list
}

func appendUniqueInt(list []int, vals ...int) []int {
	for _, v := range vals {
		found := false
		for _, x := range list {
			found = found || x == v
		}
		if !found {
			list = append(list, v)
		}
	}
	return list
}

// SandboxSupport is what a machine offers to confine commands.
type SandboxSupport struct {
	Backend   string `json:"backend"`           // landlock, microvm
	Available bool   `json:"available"`         // can confine here
	ABI       int    `json:"abi,omitempty"`     // Landlock ABI version
	Network   bool   `json:"network,omitempty"` // TCP connect can be restricted
	Reason    string `json:"reason,omitempty"`  // why not, or what is missing
}

// String is a one-line summary for status output.
func (s SandboxSupport) String() string {
	if !s.Available {
		if s.Reason == "" {
			return s.Backend + " unavailable"
		}
		return s.Backend + " unavailable (" + s.Reason + ")"
	}
	out := s.Backend
	if s.ABI > 0 {
		out += fmt.Sprintf(" ABI v%d", s.ABI)
	}
	if s.Backend == "landlock" {
		if s.Network {
			out += ", filesystem and TCP"
		} else {
			out += ", filesystem only (network needs ABI v4)"
		}
	}
	if s.Reason != "" {
		out += " (" + s.Reason + ")"
	}
	return out
}

// validateSandbox checks the sandbox keys and the profile of every check.
func (c *ProjectConfig) validateSandbox() []error {
	errs := c.Sandbox.Validate()
	names := make([]string, 0, len(c.Checks))
	for n := range c.Checks {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if p := c.Checks[n].Sandbox; p != "" {
			if _, err := LookupSandboxProfile(p); err != nil {
				errs = append(errs, fmt.Errorf("checks.%s.sandbox: %w", n, err))
			}
		}
	}
	return errs
}
