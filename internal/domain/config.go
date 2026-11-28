package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// RoleDef is a function an agent plays.
type RoleDef struct {
	Name       string   `json:"name"`
	Prompt     string   `json:"prompt"`               // path of .barnraise/roles/<role>.md, or inline text
	Class      string   `json:"class,omitempty"`      // default model class
	Providers  []string `json:"providers,omitempty"`  // allowed providers, in order
	Count      int      `json:"count"`                // agents per machine
	Tools      []string `json:"tools,omitempty"`      // MCP tools it sees (defaults by role)
	Needs      []string `json:"needs,omitempty"`      // machine labels: emulators, gpu
	Skills     []string `json:"skills,omitempty"`     // procedures loaded on demand
	Reviewer   bool     `json:"reviewer,omitempty"`   // returns reviews
	Standalone bool     `json:"standalone,omitempty"` // runs without an item (analyst triage)
}

// CheckDef declares a verification and where it runs.
type CheckDef struct {
	Name         string        `json:"name"`
	Run          string        `json:"run,omitempty"`      // command for local and machine checks
	RunsOn       string        `json:"runs_on"`            // local, github-actions, machine:<label>, webhook, plugin:<name>
	Workflow     string        `json:"workflow,omitempty"` // github-actions workflow file
	URL          string        `json:"url,omitempty"`      // webhook
	Scope        string        `json:"scope,omitempty"`    // touched (default) or all
	Timeout      time.Duration `json:"timeout"`
	FlakyRetries int           `json:"flaky_retries"` // up to 3 runs on branch and main
	Criteria     bool          `json:"criteria,omitempty"`
	Areas        []string      `json:"areas,omitempty"`      // only for these areas
	Paths        []string      `json:"paths,omitempty"`      // …or when a touched file matches one of these globs
	Dir          string        `json:"dir,omitempty"`        // folder the command runs in, relative to the repository root
	NoOffload    bool          `json:"no_offload,omitempty"` // never moved to an idle machine (it needs something only this machine has)
	Covers       []string      `json:"covers,omitempty"`     // checks this one makes redundant when both apply
	Order        int           `json:"order,omitempty"`      // lower runs first (cheap before expensive); ties by name
	Sandbox      string        `json:"sandbox,omitempty"`    // sandbox profile of a local run (default check)
}

// ContainerDef makes every agent session of the project run inside a
// container, so agents work without permission prompts and still reach
// only what is mounted: the item's worktree, the repository's .git, the
// agent's own account and the daemon's socket.
type ContainerDef struct {
	Image   string   `json:"image,omitempty"`   // has the AI CLI and the project's toolchain
	Engine  string   `json:"engine,omitempty"`  // docker (default) or podman
	Network string   `json:"network,omitempty"` // engine network: bridge (default), host, none, or a named one
	Mounts  []string `json:"mounts,omitempty"`  // extra host paths (caches, SDKs); "path:ro" is read-only
	GPU     string   `json:"gpu,omitempty"`     // auto (default: whatever the machine has), all, none
}

// SelectionDef tunes which checks run for a change, beyond areas and paths.
type SelectionDef struct {
	AllWhen  []string `json:"all_when,omitempty"`  // globs: a change to one of these runs every check
	NoneWhen []string `json:"none_when,omitempty"` // globs: a change to these alone needs no check
}

// SchedulingDef tunes how items are spread over the agents.
type SchedulingDef struct {
	// SharedFiles are globs of files several items may change at the same
	// time (a central scene or script every item touches): they do not keep
	// two items apart, and the merge queue settles what they both changed.
	SharedFiles []string `json:"shared_files,omitempty"`
}

// Exclusive drops the shared files from what an item is predicted to touch:
// what is left keeps two items from running at the same time.
func (s SchedulingDef) Exclusive(files []string) []string {
	if len(s.SharedFiles) == 0 {
		return files
	}
	var out []string
	for _, f := range files {
		shared := false
		for _, g := range s.SharedFiles {
			if f == g || MatchGlob(g, f) {
				shared = true
				break
			}
		}
		if !shared {
			out = append(out, f)
		}
	}
	return out
}

// CheckDecision is the verdict of the selection for one check.
type CheckDecision struct {
	Name string `json:"name"`
	Run  bool   `json:"run"`
	Why  string `json:"why"` // why it runs, or why it is skipped
}

// SelectChecks decides which of the named checks run for a change, in the
// order they should run. The rules, in order:
//
//  1. Files matching selection.none_when are left out; a change made only of
//     them runs just the checks with scope all.
//  2. If a remaining file matches selection.all_when, or belongs to no area
//     and to no check's paths, every check runs: never skip on unknown ground.
//  3. Otherwise a check runs when the change touches one of its areas or
//     paths (checks with neither, or with scope all, always run).
//  4. A check that another running check covers is skipped.
//
// Nothing known about the change (no files, no areas) runs everything.
func (c *ProjectConfig) SelectChecks(names, touched, touchedAreas []string) []CheckDecision {
	var relevant []string
	for _, f := range touched {
		if !matchAny(c.Selection.NoneWhen, f) {
			relevant = append(relevant, f)
		}
	}
	onlyQuiet := len(touched) > 0 && len(relevant) == 0
	full, fullWhy := false, ""
	for _, f := range relevant {
		if matchAny(c.Selection.AllWhen, f) {
			full, fullWhy = true, f+" is in selection.all_when"
			break
		}
		if c.AreaOfFile(f) == "" && !c.claimedByCheck(f) {
			full, fullWhy = true, f+" belongs to no area: every check runs"
			break
		}
	}
	out := make([]CheckDecision, 0, len(names))
	running := map[string]bool{}
	for _, n := range names {
		d, ok := c.Checks[n]
		if !ok {
			continue
		}
		dec := CheckDecision{Name: n}
		switch {
		case d.Scope == "all":
			dec.Run, dec.Why = true, "scope all"
		case onlyQuiet:
			dec.Why = "skipped: the change only touches files that need no check (selection.none_when)"
		case full:
			dec.Run, dec.Why = true, fullWhy
		default:
			areas := touchedAreas
			if len(relevant) > 0 {
				areas = nil // the files say what was touched; the item's area is only a fallback
			}
			dec.Run, dec.Why = d.Applies(relevant, areas, c.AreaOfFile)
			if dec.Run {
				dec.Why = "the change touches it"
			}
		}
		running[n] = dec.Run
		out = append(out, dec)
	}
	for i := range out {
		if !out[i].Run {
			continue
		}
		for _, by := range names {
			if by != out[i].Name && running[by] && Contains(c.Checks[by].Covers, out[i].Name) && !Contains(c.Checks[out[i].Name].Covers, by) {
				out[i].Run, out[i].Why = false, "skipped: covered by "+by
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := c.Checks[out[i].Name], c.Checks[out[j].Name]
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (c *ProjectConfig) claimedByCheck(f string) bool {
	for _, d := range c.Checks {
		if d.RunsOn == RunsOnDependencies {
			continue // it rides along; a manifest outside every area still runs every check
		}
		if matchAny(d.Paths, f) {
			return true
		}
	}
	return false
}

func matchAny(globs []string, f string) bool {
	for _, g := range globs {
		if MatchGlob(g, f) {
			return true
		}
	}
	return false
}

// Applies decides whether a check must run for a change, and says why not
// when it is skipped. The rule, in order:
//
//   - scope all: always;
//   - neither areas nor paths: always (a check of the whole repository);
//   - nothing known about the change: always (never skip blind);
//   - a touched file in one of its areas, or matching one of its paths: yes;
//   - otherwise it is skipped.
//
// touchedAreas are areas already known to be touched (for example the
// item's own area); areaOf resolves a file to its area.
func (d CheckDef) Applies(touched, touchedAreas []string, areaOf func(string) string) (bool, string) {
	if d.Scope == "all" || (len(d.Areas) == 0 && len(d.Paths) == 0) {
		return true, ""
	}
	if len(touched) == 0 && len(touchedAreas) == 0 {
		return true, ""
	}
	hit := map[string]bool{}
	for _, a := range touchedAreas {
		hit[AreaName(a)] = true
	}
	for _, f := range touched {
		for _, g := range d.Paths {
			if MatchGlob(g, f) {
				return true, ""
			}
		}
		if areaOf != nil {
			if a := areaOf(f); a != "" {
				hit[AreaName(a)] = true
			}
		}
	}
	for _, a := range d.Areas {
		if hit[AreaName(a)] {
			return true, ""
		}
	}
	scope := append(append([]string{}, d.Areas...), d.Paths...)
	return false, "skipped: the change touches nothing in " + strings.Join(scope, ", ")
}

// AreaDef maps an area to folders, screens, contracts and standards.
type AreaDef struct {
	Name      string   `json:"name"`
	Paths     []string `json:"paths"`
	Screens   []string `json:"screens,omitempty"`
	Contracts []string `json:"contracts,omitempty"`
	Standard  string   `json:"standard,omitempty"` // .barnraise/standards/<area>.md
	// Set when the area comes from its own barnraise.yaml in a folder of the
	// repository (a backend, a web app, a mobile app living side by side).
	// KindFlows gives the items of this area their own flow per kind (a
	// mobile screen goes through emulators and a design review; a backend
	// endpoint does not).
	KindFlows map[string]string `json:"kinds,omitempty"`
	Dir       string            `json:"dir,omitempty"`   // folder, relative to the repository root
	About     string            `json:"about,omitempty"` // one line: what this part is
	Stack     string            `json:"stack,omitempty"` // go, react, react-native…
}

// NotifierDef configures an owner notification channel.
type NotifierDef struct {
	Type   string            `json:"type"` // webhook, telegram, slack, ntfy, email, desktop, plugin:<name>
	Target string            `json:"target"`
	Secret string            `json:"secret,omitempty"` // keychain reference
	Extra  map[string]string `json:"extra,omitempty"`
}

// WebhookDef is an outgoing, HMAC-signed webhook.
type WebhookDef struct {
	URL       string   `json:"url"`
	Events    []string `json:"events,omitempty"` // empty: all; supports prefix "item.*"
	SecretRef string   `json:"secret_ref,omitempty"`
}

// MCPServerDef is an MCP server the owner lets agents use besides
// Barnraise's own. Nothing else is loaded into a session: the servers of
// the account's own configuration stay out.
type MCPServerDef struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"` // values may be secret references (keychain:…, env:…)
	URL     string            `json:"url,omitempty"`
	Type    string            `json:"type,omitempty"` // http or sse, with url
	// Headers of a remote server; values may be secret references.
	Headers map[string]string `json:"headers,omitempty"`
	Roles   []string          `json:"roles,omitempty"` // empty: every role
}

// MCPFor returns the extra MCP servers a role may use.
func (c *ProjectConfig) MCPFor(role string) map[string]MCPServerDef {
	out := map[string]MCPServerDef{}
	for name, d := range c.MCP {
		if len(d.Roles) == 0 || Contains(d.Roles, role) {
			out[name] = d
		}
	}
	return out
}

func (c *ProjectConfig) validateMCP() []error {
	var errs []error
	for name, d := range c.MCP {
		switch {
		case name == "barnraise":
			errs = append(errs, fmt.Errorf("mcp: the name barnraise is taken by Barnraise's own server"))
		case (d.Command == "") == (d.URL == ""):
			errs = append(errs, fmt.Errorf("mcp.%s needs either `command` (a local server) or `url` (a remote one)", name))
		case d.Type != "" && d.Type != "http" && d.Type != "sse":
			errs = append(errs, fmt.Errorf("mcp.%s: type %q is not http or sse", name, d.Type))
		}
		for _, r := range d.Roles {
			if _, ok := c.Roles[r]; !ok {
				errs = append(errs, fmt.Errorf("mcp.%s is for role %q, which does not exist", name, r))
			}
		}
	}
	return errs
}

// APIProviderDef is a provider reached with an API key instead of a
// subscription login: an endpoint that speaks the OpenAI Responses API,
// such as OpenRouter. Its sessions run the Codex CLI against the endpoint,
// so they read, edit and run commands like any other session.
type APIProviderDef struct {
	BaseURL string `json:"base_url" yaml:"base_url"`
}

// DefaultAPIProviders are the key-based providers known without
// configuration. `providers:` in barnraise.yaml adds others or changes these.
func DefaultAPIProviders() map[string]APIProviderDef {
	return map[string]APIProviderDef{"openrouter": {BaseURL: "https://openrouter.ai/api/v1"}}
}

// APIKeySecret is the keychain key that holds the API key of an account of
// a key-based provider (`barnraise account add openrouter` stores it).
func APIKeySecret(accountID string) string { return "account/" + accountID + "/api-key" }

// CLIProviders are the providers with a CLI of their own and a login; their
// names cannot be taken by a key-based provider.
var CLIProviders = []string{"claude", "codex", "gemini", "fake"}

var reAPIProviderName = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

func (c *ProjectConfig) validateAPIProviders() []error {
	var errs []error
	for name, d := range c.APIProviders {
		if !reAPIProviderName.MatchString(name) {
			errs = append(errs, fmt.Errorf("providers.%s: a provider name is lower-case letters and digits, starting with a letter", name))
			continue
		}
		if Contains(CLIProviders, name) {
			errs = append(errs, fmt.Errorf("providers: the name %s is taken by the provider with its own CLI", name))
			continue
		}
		if d.BaseURL == "" {
			errs = append(errs, fmt.Errorf("providers.%s needs a base_url (an endpoint that speaks the OpenAI Responses API, e.g. https://openrouter.ai/api/v1)", name))
		}
	}
	return errs
}

// TrackerDef configures where people see items.
type TrackerDef struct {
	Type    string            `json:"type"` // local, github, gitlab, jira, linear, plugin:<name>
	Repo    string            `json:"repo,omitempty"`
	Project string            `json:"project,omitempty"`
	URL     string            `json:"url,omitempty"`
	Columns map[string]string `json:"columns,omitempty"` // barnraise status -> tracker column
	Trusted []string          `json:"trusted,omitempty"` // authors or labels that create items directly
	Token   string            `json:"token,omitempty"`   // keychain reference
	// Reconcile turns on the periodic repair of the tracker: every live item
	// gets its issue and every issue shows its item's status, whichever
	// machine made the change and whether its own update reached the tracker.
	Reconcile bool `json:"reconcile,omitempty"`
}

// CodeHostDef configures the git server.
type CodeHostDef struct {
	Type   string `json:"type"` // github, gitlab, gitea, git
	Repo   string `json:"repo,omitempty"`
	URL    string `json:"url,omitempty"`
	Remote string `json:"remote"`
	Token  string `json:"token,omitempty"`
}

// MergeDef configures the merge queue.
type MergeDef struct {
	Mode         string `json:"mode"`  // squash or steps
	Batch        int    `json:"batch"` // items per batch, with bisection on failure
	Changelog    string `json:"changelog,omitempty"`
	Tag          bool   `json:"tag,omitempty"`
	UseHostQueue bool   `json:"use_host_queue,omitempty"`
	// HostChecks is what the merge queue does with the code host's own CI
	// on the item's pull request: "wait" (the default) lands the item only
	// after every check of the host passed on its commit, "ignore" lands
	// it on Barnraise's own checks alone.
	HostChecks string `json:"host_checks,omitempty"`
	// Rerere keeps git's record of conflict resolutions (rerere) on for the
	// project, so a conflict resolved once is reapplied the next time.
	Rerere bool `json:"rerere"`
	// Structural is when to merge with a structural merge driver
	// (mergiraf): "auto" when it is installed, "off" never.
	Structural string `json:"structural,omitempty"`
}

// LeaseDef sets claim renewals.
type LeaseDef struct {
	Renew time.Duration `json:"renew"`
	TTL   time.Duration `json:"ttl"`
}

// BudgetDef caps spending.
type BudgetDef struct {
	TriageWeekPct float64 `json:"triage_week_pct"`
	GoalWeekPct   float64 `json:"goal_week_pct"`
	ContextTokens int     `json:"context_tokens"`
	CompactAtPct  float64 `json:"compact_at_pct"`
}

// ExperimentDef routes a share of items to a change before the whole fleet.
type ExperimentDef struct {
	Name    string            `json:"name"`
	Share   int               `json:"share"` // percent of items, default 10
	Role    string            `json:"role,omitempty"`
	Model   string            `json:"model,omitempty"`
	Prompt  string            `json:"prompt,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Started time.Time         `json:"started,omitzero"`
}

// ProjectConfig is barnraise.yaml compiled with template defaults.
type ProjectConfig struct {
	Name            string                  `json:"name"`
	Root            string                  `json:"root"`
	Template        string                  `json:"template"`
	Language        string                  `json:"language"`
	Goal            Goal                    `json:"goal"`
	Approves        string                  `json:"approves"`
	MainBranch      string                  `json:"main_branch"`
	Tracker         TrackerDef              `json:"tracker"`
	CodeHost        CodeHostDef             `json:"code_host"`
	Roles           map[string]RoleDef      `json:"roles"`
	Flows           map[string]*Flow        `json:"flows"`
	Checks          map[string]CheckDef     `json:"checks"`
	Areas           map[string]AreaDef      `json:"areas"`
	Capacity        Capacity                `json:"capacity"`
	Proposals       ProposalRules           `json:"proposals"`
	Tweaks          TweakRules              `json:"tweaks"`
	TargetSize      string                  `json:"target_size"`
	SmallerBecomes  string                  `json:"smaller_becomes"`
	SameAreaFinding string                  `json:"same_area_findings"`
	SelfApproves    string                  `json:"self_approves"`
	Models          ModelRouter             `json:"models"`
	Policy          Policy                  `json:"policy"`
	Egress          EgressDef               `json:"egress"`
	Merge           MergeDef                `json:"merge"`
	Review          ReviewDef               `json:"review,omitzero"`
	Questions       QuestionsDef            `json:"questions,omitzero"`
	Selection       SelectionDef            `json:"selection,omitzero"`
	Scheduling      SchedulingDef           `json:"scheduling,omitzero"`
	Dependencies    DependencyPolicy        `json:"dependencies,omitzero"`
	Container       ContainerDef            `json:"container,omitzero"`
	Lease           LeaseDef                `json:"lease"`
	Budget          BudgetDef               `json:"budget"`
	Knowledge       KnowledgeDef            `json:"knowledge"`
	StaleAfter      time.Duration           `json:"stale_after"`
	Notifications   []NotifierDef           `json:"notifications,omitempty"`
	Webhooks        []WebhookDef            `json:"webhooks,omitempty"`
	MCP             map[string]MCPServerDef `json:"mcp,omitempty"`
	Plugins         map[string]PluginDef    `json:"plugins,omitempty"`
	Experiments     []ExperimentDef         `json:"experiments,omitempty"`
	KindFlows       map[string]string       `json:"kind_flows,omitempty"` // item kind -> flow
	SafetyCommit    time.Duration           `json:"safety_commit"`
	EnvFiles        []string                `json:"env_files,omitempty"` // copied into worktrees (.worktreeinclude)
	// WorktreeSetup is a command run once in each new worktree before any
	// work or check happens there (install dependencies, generate code).
	WorktreeSetup        string        `json:"worktree_setup,omitempty"`
	WorktreeSetupTimeout time.Duration `json:"worktree_setup_timeout,omitempty"`
	// WorktreeSeed are directories copied from the project root into a
	// worktree before its setup (dependencies, caches): copy-on-write where
	// the filesystem has it, so the setup finds most of its work done.
	WorktreeSeed []string `json:"worktree_seed,omitempty"`
	// WorktreeStrip are rebuildable directories removed from the worktree of
	// an item that waits (waiting_owner, blocked) longer than
	// WorktreeStripAfter; the setup runs again when the item moves.
	WorktreeStrip      []string      `json:"worktree_strip,omitempty"`
	WorktreeStripAfter time.Duration `json:"worktree_strip_after,omitempty"`
	// WorktreeStores are directories shared by every worktree of the project
	// (a package manager's store, a build cache): store name -> the
	// environment variable that receives its path.
	WorktreeStores map[string]string `json:"worktree_stores,omitempty"`
	PortBase       int               `json:"port_base"`
	Standards      map[string]string `json:"standards,omitempty"`
	Extra          map[string]any    `json:"extra,omitempty"`
	// APIProviders are the providers reached with an API key (OpenRouter
	// and other endpoints), by provider name.
	APIProviders map[string]APIProviderDef `json:"api_providers,omitempty"`
	// ACPProviders are agent programs that speak the Agent Client Protocol
	// (`providers.<name>.type: acp`), by provider name.
	ACPProviders map[string]ACPProviderDef `json:"acp_providers,omitempty"`
	// Outfilter configures the output filter and the full outputs it keeps.
	Outfilter OutfilterDef `json:"outfilter,omitzero"`
	// Terse decides which steps ask the agent for short answers.
	Terse TerseDef `json:"terse,omitzero"`
	// StaleReads warns an agent when main changes files it read or changed.
	StaleReads StaleReadsDef `json:"stale_reads"`
	// Lessons are distilled from resolved rework and given to later items.
	Lessons LessonsDef `json:"lessons"`
	// Documents are the texts Barnraise writes for people (pull request,
	// plan, item, issue) and when plans draw diagrams (documents.go).
	Documents DocumentsDef `json:"documents"`
	// Sandbox is how confined commands of the project are (profiles, extra paths and ports).
	Sandbox SandboxDef `json:"sandbox,omitzero"`
	// Worktree are the per-item isolation, build caches and phase hooks of
	// item worktrees (worktree_opts.go).
	Worktree WorktreeOpts `json:"worktree"`
}

// FlowFor returns the flow of an item kind.
func (c *ProjectConfig) FlowFor(kind string) (*Flow, error) {
	name := kind
	if m, ok := c.KindFlows[kind]; ok {
		name = m
	}
	if f, ok := c.Flows[name]; ok {
		return f, nil
	}
	if f, ok := c.Flows["feature"]; ok && kind == "" {
		return f, nil
	}
	return nil, fmt.Errorf("no flow for item kind %q (known: %v)", kind, c.FlowNames())
}

// FlowOf returns the flow of an item: the one its area chose for its kind,
// otherwise the project's flow for that kind.
func (c *ProjectConfig) FlowOf(kind, area string) (*Flow, error) {
	if a, ok := c.Areas[AreaName(area)]; ok && len(a.KindFlows) > 0 {
		k := kind
		if k == "" {
			k = "feature"
		}
		if name, ok := a.KindFlows[k]; ok {
			if f, ok := c.Flows[name]; ok {
				return f, nil
			}
		}
	}
	return c.FlowFor(kind)
}

// FlowNames lists flows.
func (c *ProjectConfig) FlowNames() []string {
	var out []string
	for n := range c.Flows {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// AreaGlobs returns area -> path globs.
func (c *ProjectConfig) AreaGlobs() map[string][]string {
	m := map[string][]string{}
	for n, a := range c.Areas {
		m[n] = a.Paths
	}
	return m
}

// AreaOfFile resolves a file to its area.
func (c *ProjectConfig) AreaOfFile(f string) string { return AreaOf(c.AreaGlobs(), f) }

// Validate checks the compiled config. Messages are plain sentences.
func (c *ProjectConfig) Validate() []error {
	var errs []error
	if c.Name == "" {
		errs = append(errs, fmt.Errorf("the project has no name"))
	}
	if err := c.Capacity.Validate(); err != nil {
		errs = append(errs, err)
	}
	errs = append(errs, c.validateMCP()...)
	errs = append(errs, c.validateAPIProviders()...)
	errs = append(errs, c.validateACPProviders()...)
	errs = append(errs, c.validatePlugins()...)
	errs = append(errs, c.validateSandbox()...)
	if ct := c.Container; ct.Image == "" && (ct.Engine != "" || ct.Network != "" || len(ct.Mounts) > 0) {
		errs = append(errs, fmt.Errorf("container needs an image (one with the AI CLI and the project's tools)"))
	} else if ct.Engine != "" && ct.Engine != "docker" && ct.Engine != "podman" {
		errs = append(errs, fmt.Errorf("container.engine %q is not docker or podman", ct.Engine))
	} else if ct.GPU != "" && ct.GPU != "auto" && ct.GPU != "all" && ct.GPU != "none" {
		errs = append(errs, fmt.Errorf("container.gpu %q is not auto, all or none", ct.GPU))
	}
	for _, m := range c.Container.Mounts {
		if why := ForbiddenMount(m, ""); why != "" {
			errs = append(errs, fmt.Errorf("container.mounts: %s is never mounted into an agent's container: %s", m, why))
		}
	}
	for an, a := range c.Areas {
		for kind, flow := range a.KindFlows {
			if _, ok := c.Flows[flow]; !ok {
				errs = append(errs, fmt.Errorf("area %s sends %s items to flow %q, which is not declared in flows (known: %v)", an, kind, flow, c.FlowNames()))
			}
		}
	}
	for name, d := range c.Checks {
		// A misspelled area would make the check never run: say it now.
		for _, a := range d.Areas {
			if _, ok := c.Areas[AreaName(a)]; !ok {
				errs = append(errs, fmt.Errorf("check %s is for area %q, which is not declared in areas", name, a))
			}
		}
		for _, o := range d.Covers {
			if _, ok := c.Checks[o]; !ok {
				errs = append(errs, fmt.Errorf("check %s covers %q, which is not declared in checks", name, o))
			}
		}
	}
	if len(c.Flows) == 0 {
		errs = append(errs, fmt.Errorf("no flows are defined; pick a template or declare flows"))
	}
	for _, f := range c.Flows {
		if err := f.Validate(); err != nil {
			errs = append(errs, err)
			continue
		}
		for _, s := range f.Steps {
			if s.Type == StepAgent {
				if _, ok := c.Roles[s.Role]; !ok {
					errs = append(errs, fmt.Errorf("flow %s, step %s uses role %q, which is not declared in roles", f.Name, s.Name, s.Role))
				}
			}
			if s.Type == StepApproval && s.Approver != "owner" {
				if _, ok := c.Roles[s.Approver]; !ok {
					errs = append(errs, fmt.Errorf("flow %s, step %s is approved by %q, which is neither owner nor a declared role", f.Name, s.Name, s.Approver))
				}
			}
			if s.Type == StepCheck {
				name := trimChecks(s.Run)
				if name != "all" && name != "checks" {
					if _, ok := c.Checks[name]; !ok {
						errs = append(errs, fmt.Errorf("flow %s, step %s runs check %q, which is not declared in checks", f.Name, s.Name, name))
					}
				}
			}
		}
	}
	for n, ch := range c.Checks {
		switch {
		case ch.RunsOn == "local" || len(ch.RunsOn) > 8 && ch.RunsOn[:8] == "machine:":
			if ch.Run == "" {
				errs = append(errs, fmt.Errorf("check %s runs on %s but has no command", n, ch.RunsOn))
			}
		case ch.RunsOn == "github-actions":
			if ch.Workflow == "" {
				errs = append(errs, fmt.Errorf("check %s runs on GitHub Actions but names no workflow", n))
			}
		case ch.RunsOn == "webhook":
			if ch.URL == "" {
				errs = append(errs, fmt.Errorf("check %s runs on a webhook but has no url", n))
			}
		case strings.HasPrefix(ch.RunsOn, "plugin:"):
			// validatePlugins checks the name
		case isBuiltinCheck(ch.RunsOn):
		default:
			errs = append(errs, fmt.Errorf("check %s: runs_on %q must be local, github-actions, machine:<label>, webhook, plugin:<name>, %s or %s", n, ch.RunsOn, RunsOnDependencies, RunsOnASTGrep))
		}
	}
	switch c.Approves {
	case "owner", "analyst":
	default:
		errs = append(errs, fmt.Errorf("approves must be owner or analyst, not %q", c.Approves))
	}
	switch c.Tracker.Type {
	case "local", "github", "gitlab", "jira", "linear":
	default:
		if !strings.HasPrefix(c.Tracker.Type, "plugin:") {
			errs = append(errs, fmt.Errorf("tracker type %q is not supported; use local, github, gitlab, jira, linear or plugin:<name>", c.Tracker.Type))
		}
	}
	if c.Lease.TTL > 0 && c.Lease.Renew >= c.Lease.TTL {
		errs = append(errs, fmt.Errorf("lease renew (%s) must be shorter than the ttl (%s)", c.Lease.Renew, c.Lease.TTL))
	}
	errs = append(errs, c.Dependencies.Validate()...)
	return errs
}

// TrimChecks turns "checks.main" into "main".
func TrimChecks(s string) string { return trimChecks(s) }

func trimChecks(s string) string {
	if len(s) > 7 && s[:7] == "checks." {
		return s[7:]
	}
	return s
}

// GitTokenSecret is the keychain key of the token for one repository at a
// host (github.com, alchemistfy/one-more-bastion), "" without both.
func GitTokenSecret(host, path string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	path = strings.Trim(strings.TrimSuffix(strings.TrimSpace(path), ".git"), "/")
	if host == "" || path == "" {
		return ""
	}
	return "git/" + host + "/" + path
}

// ReviewDef is what a review may send back.
type ReviewDef struct {
	// SendBack: "all" (any finding of the item's area sends it back) or
	// "blockers" (only a blocker, or criteria that fail, sends it back;
	// the other findings become one follow-up proposal and the item goes on).
	SendBack string `json:"send_back,omitempty"`
}

// Review send-back modes.
const (
	SendBackAll      = "all"
	SendBackBlockers = "blockers"
)

// QuestionsDef is how long a question to the owner waits.
type QuestionsDef struct {
	// DecideAfter: an open question takes its recommended option after this
	// long with no answer (0: it waits for the owner or the autopilot).
	DecideAfter time.Duration `json:"decide_after,omitempty"`
}
