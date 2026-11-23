package domain

import (
	"fmt"
	"sort"
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
	RunsOn       string        `json:"runs_on"`            // local, github-actions, machine:<label>, webhook
	Workflow     string        `json:"workflow,omitempty"` // github-actions workflow file
	URL          string        `json:"url,omitempty"`      // webhook
	Scope        string        `json:"scope,omitempty"`    // touched (default) or all
	Timeout      time.Duration `json:"timeout"`
	FlakyRetries int           `json:"flaky_retries"` // up to 3 runs on branch and main
	Criteria     bool          `json:"criteria,omitempty"`
	Areas        []string      `json:"areas,omitempty"` // only for these areas
}

// AreaDef maps an area to folders, screens, contracts and standards.
type AreaDef struct {
	Name      string   `json:"name"`
	Paths     []string `json:"paths"`
	Screens   []string `json:"screens,omitempty"`
	Contracts []string `json:"contracts,omitempty"`
	Standard  string   `json:"standard,omitempty"` // .barnraise/standards/<area>.md
}

// NotifierDef configures an owner notification channel.
type NotifierDef struct {
	Type   string            `json:"type"` // webhook, telegram, slack, ntfy, email, desktop
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

// TrackerDef configures where people see items.
type TrackerDef struct {
	Type    string            `json:"type"` // local, github, gitlab, jira, linear
	Repo    string            `json:"repo,omitempty"`
	Project string            `json:"project,omitempty"`
	URL     string            `json:"url,omitempty"`
	Columns map[string]string `json:"columns,omitempty"` // barnraise status -> tracker column
	Trusted []string          `json:"trusted,omitempty"` // authors or labels that create items directly
	Token   string            `json:"token,omitempty"`   // keychain reference
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
	Started time.Time         `json:"started,omitempty"`
}

// ProjectConfig is barnraise.yaml compiled with template defaults.
type ProjectConfig struct {
	Name            string                 `json:"name"`
	Root            string                 `json:"root"`
	Template        string                 `json:"template"`
	Language        string                 `json:"language"`
	Goal            Goal                   `json:"goal"`
	Approves        string                 `json:"approves"`
	MainBranch      string                 `json:"main_branch"`
	Tracker         TrackerDef             `json:"tracker"`
	CodeHost        CodeHostDef            `json:"code_host"`
	Roles           map[string]RoleDef     `json:"roles"`
	Flows           map[string]*Flow       `json:"flows"`
	Checks          map[string]CheckDef    `json:"checks"`
	Areas           map[string]AreaDef     `json:"areas"`
	Capacity        Capacity               `json:"capacity"`
	Proposals       ProposalRules          `json:"proposals"`
	Tweaks          TweakRules             `json:"tweaks"`
	TargetSize      string                 `json:"target_size"`
	SmallerBecomes  string                 `json:"smaller_becomes"`
	SameAreaFinding string                 `json:"same_area_findings"`
	SelfApproves    string                 `json:"self_approves"`
	Models          ModelRouter            `json:"models"`
	Policy          Policy                 `json:"policy"`
	Merge           MergeDef               `json:"merge"`
	Lease           LeaseDef               `json:"lease"`
	Budget          BudgetDef              `json:"budget"`
	StaleAfter      time.Duration          `json:"stale_after"`
	Notifications   []NotifierDef          `json:"notifications,omitempty"`
	Webhooks        []WebhookDef           `json:"webhooks,omitempty"`
	Experiments     []ExperimentDef        `json:"experiments,omitempty"`
	KindFlows       map[string]string      `json:"kind_flows,omitempty"` // item kind -> flow
	SafetyCommit    time.Duration          `json:"safety_commit"`
	EnvFiles        []string               `json:"env_files,omitempty"` // copied into worktrees (.worktreeinclude)
	PortBase        int                    `json:"port_base"`
	Standards       map[string]string      `json:"standards,omitempty"`
	Extra           map[string]interface{} `json:"extra,omitempty"`
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
				if name != "all" {
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
		default:
			errs = append(errs, fmt.Errorf("check %s: runs_on %q must be local, github-actions, machine:<label> or webhook", n, ch.RunsOn))
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
		errs = append(errs, fmt.Errorf("tracker type %q is not supported; use local, github, gitlab, jira or linear", c.Tracker.Type))
	}
	if c.Lease.TTL > 0 && c.Lease.Renew >= c.Lease.TTL {
		errs = append(errs, fmt.Errorf("lease renew (%s) must be shorter than the ttl (%s)", c.Lease.Renew, c.Lease.TTL))
	}
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
