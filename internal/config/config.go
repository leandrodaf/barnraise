// Package config reads barnraise.yaml, merges it over the template and the
// built-in defaults, and compiles it into domain.ProjectConfig.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/templates"
)

// FileName is the project file.
const FileName = "barnraise.yaml"

// Dir is the project folder for roles, standards and the constitution.
const Dir = ".barnraise"

// Load reads root/barnraise.yaml and compiles it.
func Load(root string) (*domain.ProjectConfig, error) {
	data, err := os.ReadFile(filepath.Join(root, FileName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no %s in %s: run `barnraise init` first", FileName, root)
		}
		return nil, err
	}
	return Compile(root, data)
}

// Compile merges data over its template and the defaults.
func Compile(root string, data []byte) (*domain.ProjectConfig, error) {
	var user yaml.Node
	if err := yaml.Unmarshal(data, &user); err != nil {
		return nil, fmt.Errorf("%s is not valid YAML: %w", FileName, err)
	}
	userMap := docMap(&user)
	tmplName := "blank"
	if v := lookup(userMap, "template"); v != nil && v.Value != "" {
		tmplName = v.Value
	}
	tdata, err := TemplateConfig(tmplName)
	if err != nil {
		return nil, err
	}
	var tnode yaml.Node
	if err := yaml.Unmarshal(tdata, &tnode); err != nil {
		return nil, fmt.Errorf("template %s: %w", tmplName, err)
	}
	var base yaml.Node
	if err := yaml.Unmarshal(templates.Base(), &base); err != nil {
		return nil, fmt.Errorf("base defaults: %w", err)
	}
	merged := mergeNodes(docMap(&base), docMap(&tnode), []string{})
	if userMap != nil {
		merged = mergeNodes(merged, userMap, []string{})
	}
	var raw rawConfig
	if err := merged.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, friendlyYAML(err))
	}
	raw.flowsNode = lookup(merged, "flows")
	cfg, err := raw.compile(root, tmplName)
	if err != nil {
		return nil, err
	}
	if err := loadAreasFile(root, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// TemplateConfig returns template.yaml of an embedded or saved template.
func TemplateConfig(name string) ([]byte, error) {
	if data, err := templates.Config(name); err == nil {
		return data, nil
	}
	if dir, err := UserTemplatesDir(); err == nil {
		if data, err := os.ReadFile(filepath.Join(dir, name, "template.yaml")); err == nil {
			return data, nil
		}
	}
	return nil, fmt.Errorf("template %q does not exist; built-in templates are %s", name, strings.Join(templates.Names(), ", "))
}

// UserTemplatesDir is where `barnraise template save` writes.
func UserTemplatesDir() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "barnraise", "templates"), nil
}

func friendlyYAML(err error) error {
	msg := err.Error()
	msg = strings.ReplaceAll(msg, "yaml: unmarshal errors:\n", "")
	return errors.New(strings.TrimSpace(msg))
}

// docMap returns the top-level mapping of a YAML document.
func docMap(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		return n.Content[0]
	}
	if n.Kind == 0 {
		return nil
	}
	return n
}

func lookup(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// replaceWhole lists paths whose values replace instead of merging.
var replaceWhole = map[string]bool{"flows.*": true, "models.classes.*": true}

// mergeNodes deep-merges mapping b over a. Flows are replaced whole so a
// project that redefines a flow does not inherit stray steps.
func mergeNodes(a, b *yaml.Node, path []string) *yaml.Node {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if a.Kind != yaml.MappingNode || b.Kind != yaml.MappingNode {
		return b
	}
	out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	idx := map[string]int{}
	for i := 0; i+1 < len(a.Content); i += 2 {
		idx[a.Content[i].Value] = len(out.Content)
		out.Content = append(out.Content, a.Content[i], a.Content[i+1])
	}
	for i := 0; i+1 < len(b.Content); i += 2 {
		k := b.Content[i].Value
		p := append(append([]string(nil), path...), k)
		if j, ok := idx[k]; ok {
			if matchesReplace(p) {
				out.Content[j+1] = b.Content[i+1]
			} else {
				out.Content[j+1] = mergeNodes(out.Content[j+1], b.Content[i+1], p)
			}
			continue
		}
		idx[k] = len(out.Content)
		out.Content = append(out.Content, b.Content[i], b.Content[i+1])
	}
	return out
}

func matchesReplace(p []string) bool {
	for pat := range replaceWhole {
		parts := strings.Split(pat, ".")
		if len(parts) != len(p) {
			continue
		}
		ok := true
		for i := range parts {
			if parts[i] != "*" && parts[i] != p[i] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// rawConfig mirrors barnraise.yaml.
type rawConfig struct {
	Project    string            `yaml:"project"`
	Template   string            `yaml:"template"`
	Language   string            `yaml:"language"`
	Goal       goalField         `yaml:"goal"`
	Approves   string            `yaml:"approves"`
	MainBranch string            `yaml:"main_branch"`
	Tracker    domain.TrackerDef `yaml:"-"`
	TrackerRaw struct {
		Type    string            `yaml:"type"`
		Repo    string            `yaml:"repo"`
		Project yaml.Node         `yaml:"project"`
		URL     string            `yaml:"url"`
		Columns map[string]string `yaml:"columns"`
		Trusted []string          `yaml:"trusted"`
		Token   string            `yaml:"token"`
	} `yaml:"tracker"`
	CodeHost struct {
		Type   string `yaml:"type"`
		Repo   string `yaml:"repo"`
		URL    string `yaml:"url"`
		Remote string `yaml:"remote"`
		Token  string `yaml:"token"`
	} `yaml:"code_host"`
	Checks map[string]checkField `yaml:"checks"`
	Roles  map[string]struct {
		Prompt    string   `yaml:"prompt"`
		Class     string   `yaml:"class"`
		Providers []string `yaml:"providers"`
		Count     *int     `yaml:"count"`
		Tools     []string `yaml:"tools"`
		Needs     []string `yaml:"needs"`
		Skills    []string `yaml:"skills"`
		Reviewer  bool     `yaml:"reviewer"`
	} `yaml:"roles"`
	Kinds    map[string]string `yaml:"kinds"`
	Capacity map[string]string `yaml:"capacity"`
	Proposal struct {
		PerAgentPerDay int    `yaml:"per_agent_per_day"`
		Expire         string `yaml:"expire"`
		Duplicate      string `yaml:"duplicate"`
	} `yaml:"proposals"`
	Items struct {
		TargetSize string `yaml:"target_size"`
		Smaller    string `yaml:"smaller"`
		Tweaks     struct {
			GroupBy       string `yaml:"group_by"`
			BecomeItemAt  int    `yaml:"become_item_at"`
			AttachOnClaim *bool  `yaml:"attach_on_claim"`
		} `yaml:"tweaks"`
		SameAreaFindings string `yaml:"same_area_findings"`
	} `yaml:"items"`
	SelfImprovement struct {
		Budget   string `yaml:"budget"`
		Approves string `yaml:"approves"`
	} `yaml:"self_improvement"`
	Models struct {
		Classes  map[string][]string `yaml:"classes"`
		Ladders  map[string][]string `yaml:"ladders"`
		Families map[string]string   `yaml:"families"`
	} `yaml:"models"`
	Policy struct {
		DenyWrite    []string `yaml:"deny_write"`
		LockExisting []string `yaml:"lock_existing"`
		DenyRead     []string `yaml:"deny_read"`
		DenyCommands []string `yaml:"deny_commands"`
		AllowHosts   []string `yaml:"allow_hosts"`
	} `yaml:"policy"`
	Areas map[string]struct {
		Paths     []string `yaml:"paths"`
		Screens   []string `yaml:"screens"`
		Contracts []string `yaml:"contracts"`
		Standard  string   `yaml:"standard"`
	} `yaml:"areas"`
	Merge struct {
		Mode         string `yaml:"mode"`
		Batch        int    `yaml:"batch"`
		Changelog    string `yaml:"changelog"`
		Tag          bool   `yaml:"tag"`
		UseHostQueue bool   `yaml:"use_host_queue"`
	} `yaml:"merge"`
	Lease struct {
		Renew string `yaml:"renew"`
		TTL   string `yaml:"ttl"`
	} `yaml:"lease"`
	Budget struct {
		TriageWeek    string `yaml:"triage_week"`
		GoalWeek      string `yaml:"goal_week"`
		ContextTokens int    `yaml:"context_tokens"`
		CompactAt     string `yaml:"compact_at"`
	} `yaml:"budget"`
	Revalidation struct {
		StaleAfter string `yaml:"stale_after"`
	} `yaml:"revalidation"`
	Notifications []domain.NotifierDef `yaml:"notifications"`
	Webhooks      []struct {
		URL       string   `yaml:"url"`
		Events    []string `yaml:"events"`
		SecretRef string   `yaml:"secret_ref"`
	} `yaml:"webhooks"`
	Experiments []struct {
		Name   string            `yaml:"name"`
		Share  string            `yaml:"share"`
		Role   string            `yaml:"role"`
		Model  string            `yaml:"model"`
		Prompt string            `yaml:"prompt"`
		Env    map[string]string `yaml:"env"`
	} `yaml:"experiments"`
	Worktree struct {
		Include      []string `yaml:"include"`
		PortBase     int      `yaml:"port_base"`
		SafetyCommit string   `yaml:"safety_commit"`
	} `yaml:"worktree"`

	flowsNode *yaml.Node
}

// goalField accepts a string or a mapping.
type goalField struct {
	Name      string `yaml:"name"`
	DoneWhen  string `yaml:"done_when"`
	Inventory string `yaml:"inventory"`
	Budget    string `yaml:"budget"`
}

func (g *goalField) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		g.Name = n.Value
		return nil
	}
	type plain goalField
	return n.Decode((*plain)(g))
}

// checkField accepts a command string or a mapping.
type checkField struct {
	Run          string   `yaml:"run"`
	RunsOn       string   `yaml:"runs_on"`
	Workflow     string   `yaml:"workflow"`
	URL          string   `yaml:"url"`
	Scope        string   `yaml:"scope"`
	Timeout      string   `yaml:"timeout"`
	FlakyRetries *int     `yaml:"flaky_retries"`
	Areas        []string `yaml:"areas"`
}

func (c *checkField) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		c.Run = n.Value
		return nil
	}
	type plain checkField
	return n.Decode((*plain)(c))
}

func (r *rawConfig) compile(root, tmpl string) (*domain.ProjectConfig, error) {
	c := &domain.ProjectConfig{Root: root, Template: tmpl}
	c.Name = r.Project
	if c.Name == "" && root != "" {
		abs, _ := filepath.Abs(root)
		c.Name = domain.Slug(filepath.Base(abs), 40)
	}
	c.Language = def(r.Language, "en")
	c.Approves = def(r.Approves, "owner")
	c.MainBranch = def(r.MainBranch, "main")
	c.Goal = domain.Goal{Name: r.Goal.Name, DoneWhen: r.Goal.DoneWhen, Inventory: r.Goal.Inventory}
	if r.Goal.Budget != "" {
		p, err := domain.ParsePercent(r.Goal.Budget)
		if err != nil {
			return nil, fmt.Errorf("goal.budget: %w", err)
		}
		c.Goal.BudgetPct = p
	}
	tr := r.TrackerRaw
	c.Tracker = domain.TrackerDef{Type: def(tr.Type, "local"), Repo: tr.Repo, URL: tr.URL, Columns: tr.Columns, Trusted: tr.Trusted, Token: tr.Token}
	if tr.Project.Kind == yaml.ScalarNode {
		c.Tracker.Project = tr.Project.Value
	}
	ch := r.CodeHost
	c.CodeHost = domain.CodeHostDef{Type: ch.Type, Repo: ch.Repo, URL: ch.URL, Remote: def(ch.Remote, "origin"), Token: ch.Token}
	if c.CodeHost.Type == "" {
		switch c.Tracker.Type {
		case "github", "gitlab":
			c.CodeHost.Type = c.Tracker.Type
			if c.CodeHost.Repo == "" {
				c.CodeHost.Repo = c.Tracker.Repo
			}
		default:
			c.CodeHost.Type = "git"
		}
	}

	c.Checks = map[string]domain.CheckDef{}
	for name, cf := range r.Checks {
		d := domain.CheckDef{Name: name, Run: cf.Run, RunsOn: def(cf.RunsOn, "local"), Workflow: cf.Workflow, URL: cf.URL, Scope: def(cf.Scope, "touched"), Areas: cf.Areas, FlakyRetries: 3}
		if cf.FlakyRetries != nil {
			d.FlakyRetries = *cf.FlakyRetries
		}
		d.Timeout = 30 * time.Minute
		if cf.Timeout != "" {
			t, err := domain.ParseDuration(cf.Timeout)
			if err != nil {
				return nil, fmt.Errorf("checks.%s.timeout: %w", name, err)
			}
			d.Timeout = t
		}
		c.Checks[name] = d
	}

	c.Roles = map[string]domain.RoleDef{}
	for name, rr := range r.Roles {
		count := 1
		if rr.Count != nil {
			count = *rr.Count
		}
		prompt := rr.Prompt
		if prompt == "" {
			prompt = filepath.ToSlash(filepath.Join(Dir, "roles", name+".md"))
		}
		c.Roles[name] = domain.RoleDef{Name: name, Prompt: prompt, Class: rr.Class, Providers: rr.Providers, Count: count, Tools: rr.Tools, Needs: rr.Needs, Skills: rr.Skills, Reviewer: rr.Reviewer}
	}

	flows, err := compileFlows(r.flowsNode, c.Roles)
	if err != nil {
		return nil, err
	}
	c.Flows = flows
	c.KindFlows = r.Kinds

	c.Capacity = domain.DefaultCapacity
	for k, v := range r.Capacity {
		p, err := domain.ParsePercent(v)
		if err != nil {
			return nil, fmt.Errorf("capacity.%s: %w", k, err)
		}
		switch k {
		case "goal":
			c.Capacity.Goal = p
		case "bugs":
			c.Capacity.Bugs = p
		case "improvements":
			c.Capacity.Improvements = p
		default:
			return nil, fmt.Errorf("capacity.%s is not a lane; use goal, bugs and improvements", k)
		}
	}
	if b := r.SelfImprovement.Budget; b != "" {
		n, unit, ok := strings.Cut(b, "/")
		v, err := strconv.Atoi(strings.TrimSpace(n))
		if !ok || err != nil || strings.TrimSpace(unit) != "week" {
			return nil, fmt.Errorf("self_improvement.budget %q: use <n>/week", b)
		}
		c.Capacity.SelfPerWeek = v
	}
	c.SelfApproves = def(r.SelfImprovement.Approves, "owner")

	c.Proposals = domain.DefaultProposalRules
	if r.Proposal.PerAgentPerDay > 0 {
		c.Proposals.PerAgentPerDay = r.Proposal.PerAgentPerDay
	}
	if r.Proposal.Expire != "" {
		d, err := domain.ParseDuration(r.Proposal.Expire)
		if err != nil {
			return nil, fmt.Errorf("proposals.expire: %w", err)
		}
		c.Proposals.Expire = d
	}
	c.Proposals.Duplicate = def(r.Proposal.Duplicate, "comment")

	c.Tweaks = domain.DefaultTweakRules
	if r.Items.Tweaks.BecomeItemAt > 0 {
		c.Tweaks.BecomeItemAt = r.Items.Tweaks.BecomeItemAt
	}
	if r.Items.Tweaks.AttachOnClaim != nil {
		c.Tweaks.AttachOnClaim = *r.Items.Tweaks.AttachOnClaim
	}
	c.TargetSize = def(r.Items.TargetSize, "2h-6h")
	if _, _, err := domain.ParseSizeRange(c.TargetSize); err != nil {
		return nil, fmt.Errorf("items.target_size: %w", err)
	}
	c.SmallerBecomes = def(r.Items.Smaller, "tweak")
	c.SameAreaFinding = def(r.Items.SameAreaFindings, "rework")

	c.Models = domain.DefaultModelRouter()
	for class, list := range r.Models.Classes {
		var choices []domain.ModelChoice
		for _, s := range list {
			mc, err := ParseModelChoice(s)
			if err != nil {
				return nil, fmt.Errorf("models.classes.%s: %w", class, err)
			}
			choices = append(choices, mc)
		}
		c.Models.Classes[class] = choices
	}
	for p, l := range r.Models.Ladders {
		c.Models.Ladders[p] = l
	}
	for p, f := range r.Models.Families {
		c.Models.Families[p] = f
	}

	c.Policy = domain.DefaultPolicy().Merge(domain.Policy{
		DenyWrite: r.Policy.DenyWrite, LockExisting: r.Policy.LockExisting, DenyRead: r.Policy.DenyRead,
		DenyCommands: r.Policy.DenyCommands, AllowHosts: r.Policy.AllowHosts, MainBranch: c.MainBranch,
	})

	c.Areas = map[string]domain.AreaDef{}
	for name, a := range r.Areas {
		c.Areas[domain.AreaName(name)] = domain.AreaDef{Name: domain.AreaName(name), Paths: a.Paths, Screens: a.Screens, Contracts: a.Contracts, Standard: a.Standard}
	}

	c.Merge = domain.MergeDef{Mode: def(r.Merge.Mode, "squash"), Batch: r.Merge.Batch, Changelog: r.Merge.Changelog, Tag: r.Merge.Tag, UseHostQueue: r.Merge.UseHostQueue}
	if c.Merge.Batch <= 0 {
		c.Merge.Batch = 4
	}
	if c.Merge.Mode != "squash" && c.Merge.Mode != "steps" {
		return nil, fmt.Errorf("merge.mode must be squash or steps")
	}

	c.Lease = domain.LeaseDef{Renew: 30 * time.Second, TTL: 2 * time.Minute}
	if r.Lease.Renew != "" {
		d, err := domain.ParseDuration(r.Lease.Renew)
		if err != nil {
			return nil, fmt.Errorf("lease.renew: %w", err)
		}
		c.Lease.Renew = d
	}
	if r.Lease.TTL != "" {
		d, err := domain.ParseDuration(r.Lease.TTL)
		if err != nil {
			return nil, fmt.Errorf("lease.ttl: %w", err)
		}
		c.Lease.TTL = d
	}

	c.Budget = domain.BudgetDef{TriageWeekPct: 2, ContextTokens: 6000, CompactAtPct: 60}
	if v := r.Budget.TriageWeek; v != "" {
		p, err := domain.ParsePercent(v)
		if err != nil {
			return nil, fmt.Errorf("budget.triage_week: %w", err)
		}
		c.Budget.TriageWeekPct = float64(p)
	}
	if v := r.Budget.GoalWeek; v != "" {
		p, err := domain.ParsePercent(v)
		if err != nil {
			return nil, fmt.Errorf("budget.goal_week: %w", err)
		}
		c.Budget.GoalWeekPct = float64(p)
	}
	if r.Budget.ContextTokens > 0 {
		c.Budget.ContextTokens = r.Budget.ContextTokens
	}
	if v := r.Budget.CompactAt; v != "" {
		p, err := domain.ParsePercent(v)
		if err != nil {
			return nil, fmt.Errorf("budget.compact_at: %w", err)
		}
		c.Budget.CompactAtPct = float64(p)
	}
	c.StaleAfter = 14 * 24 * time.Hour
	if v := r.Revalidation.StaleAfter; v != "" {
		d, err := domain.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("revalidation.stale_after: %w", err)
		}
		c.StaleAfter = d
	}
	c.Notifications = r.Notifications
	for _, w := range r.Webhooks {
		c.Webhooks = append(c.Webhooks, domain.WebhookDef{URL: w.URL, Events: w.Events, SecretRef: w.SecretRef})
	}
	for _, e := range r.Experiments {
		share := 10
		if e.Share != "" {
			p, err := domain.ParsePercent(e.Share)
			if err != nil {
				return nil, fmt.Errorf("experiments.%s.share: %w", e.Name, err)
			}
			share = p
		}
		c.Experiments = append(c.Experiments, domain.ExperimentDef{Name: e.Name, Share: share, Role: e.Role, Model: e.Model, Prompt: e.Prompt, Env: e.Env})
	}
	c.EnvFiles = r.Worktree.Include
	c.PortBase = r.Worktree.PortBase
	if c.PortBase == 0 {
		c.PortBase = 4100
	}
	c.SafetyCommit = 15 * time.Minute
	if v := r.Worktree.SafetyCommit; v != "" {
		d, err := domain.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("worktree.safety_commit: %w", err)
		}
		c.SafetyCommit = d
	}
	return c, nil
}

// ParseModelChoice parses "claude/opus@high".
func ParseModelChoice(s string) (domain.ModelChoice, error) {
	prov, rest, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok || prov == "" || rest == "" {
		return domain.ModelChoice{}, fmt.Errorf("model %q: use provider/model or provider/model@effort", s)
	}
	model, effort, _ := strings.Cut(rest, "@")
	return domain.ModelChoice{Provider: prov, Model: model, Effort: effort}, nil
}

// reviewerRoles return reviews by default.
var reviewerRoles = map[string]bool{"reviewer": true, "critic": true, "designer": true, "analyst": true, "editor": true}

// compileFlows keeps the step order of the YAML mapping.
func compileFlows(n *yaml.Node, roles map[string]domain.RoleDef) (map[string]*domain.Flow, error) {
	out := map[string]*domain.Flow{}
	if n == nil {
		return out, nil
	}
	if n.Kind != yaml.MappingNode {
		return nil, errors.New("flows must be a mapping of flow names")
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		name := n.Content[i].Value
		fn := n.Content[i+1]
		f := &domain.Flow{Name: name, Rework: map[string]string{}, Limit: domain.DefaultReworkLimit}
		steps := lookup(fn, "steps")
		if steps == nil || steps.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("flow %s: steps must be a mapping in order", name)
		}
		for j := 0; j+1 < len(steps.Content); j += 2 {
			sname := steps.Content[j].Value
			var sf struct {
				Type        string `yaml:"type"`
				Role        string `yaml:"role"`
				Model       string `yaml:"model"`
				ModelFamily string `yaml:"model_family"`
				Class       string `yaml:"class"`
				Output      string `yaml:"output"`
				Approve     string `yaml:"approve"`
				Run         string `yaml:"run"`
				Scope       string `yaml:"scope"`
				Require     string `yaml:"require"`
				Review      *bool  `yaml:"review"`
			}
			if err := steps.Content[j+1].Decode(&sf); err != nil {
				return nil, fmt.Errorf("flow %s, step %s: %w", name, sname, friendlyYAML(err))
			}
			st := domain.StepDef{Name: sname, Role: sf.Role, Model: sf.Model, ModelFamily: sf.ModelFamily, Class: sf.Class, Output: sf.Output, Run: sf.Run, Scope: sf.Scope, Require: sf.Require}
			switch {
			case sf.Type != "":
				st.Type = domain.StepType(sf.Type)
			case sf.Run == "merge_queue":
				st.Type = domain.StepMergeQueue
			case strings.HasPrefix(sf.Run, "checks.") || sf.Run == "checks":
				st.Type = domain.StepCheck
			case sf.Run != "":
				st.Type = domain.StepScript
			case sf.Role != "":
				st.Type = domain.StepAgent
			case sf.Approve != "":
				st.Type = domain.StepApproval
				st.Approver = sf.Approve
			default:
				return nil, fmt.Errorf("flow %s, step %s: say what runs it (role, run or approve)", name, sname)
			}
			if st.Type == domain.StepAgent {
				rd := roles[st.Role]
				st.Review = rd.Reviewer || reviewerRoles[st.Role]
				if sf.Review != nil {
					st.Review = *sf.Review
				}
				if st.Output == "plan.md" || st.Output == "failing_test" {
					st.Review = false
				}
			}
			if st.Type == domain.StepApproval && st.Approver == "" {
				st.Approver = sf.Approve
			}
			f.Steps = append(f.Steps, st)
			// An agent step with approve: gets its own approval step right after it.
			if st.Type == domain.StepAgent && sf.Approve != "" {
				ap := domain.StepDef{Name: sname + "-approval", Type: domain.StepApproval, Approver: sf.Approve}
				f.Steps = append(f.Steps, ap)
				f.Rework[ap.Name+".reject"] = sname
				f.Rework[ap.Name+".rework"] = sname
			}
		}
		if rw := lookup(fn, "rework"); rw != nil && rw.Kind == yaml.MappingNode {
			for j := 0; j+1 < len(rw.Content); j += 2 {
				k := rw.Content[j].Value
				v := rw.Content[j+1]
				if k == "limit" {
					var lim struct {
						SameModel *int   `yaml:"same_model"`
						Escalate  *int   `yaml:"escalate"`
						Then      string `yaml:"then"`
					}
					if err := v.Decode(&lim); err != nil {
						return nil, fmt.Errorf("flow %s, rework.limit: %w", name, err)
					}
					if lim.SameModel != nil {
						f.Limit.SameModel = *lim.SameModel
					}
					if lim.Escalate != nil {
						f.Limit.Escalate = *lim.Escalate
					}
					if lim.Then != "" {
						f.Limit.Then = lim.Then
					}
					continue
				}
				f.Rework[k] = v.Value
				// "x.reject" and "x.rework" are the same for approval steps.
			}
		}
		out[name] = f
	}
	return out, nil
}

// loadAreasFile merges .barnraise/areas.yaml.
func loadAreasFile(root string, c *domain.ProjectConfig) error {
	data, err := os.ReadFile(filepath.Join(root, Dir, "areas.yaml"))
	if err != nil {
		return nil
	}
	var m map[string]struct {
		Paths     []string `yaml:"paths"`
		Screens   []string `yaml:"screens"`
		Contracts []string `yaml:"contracts"`
		Standard  string   `yaml:"standard"`
	}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("%s/areas.yaml: %w", Dir, err)
	}
	for name, a := range m {
		n := domain.AreaName(name)
		if _, ok := c.Areas[n]; ok {
			continue // barnraise.yaml wins
		}
		c.Areas[n] = domain.AreaDef{Name: n, Paths: a.Paths, Screens: a.Screens, Contracts: a.Contracts, Standard: a.Standard}
	}
	return nil
}

func def(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}

// Check compiles and validates, returning plain sentences.
func Check(root string) ([]string, error) {
	cfg, err := Load(root)
	if err != nil {
		return nil, err
	}
	var problems []string
	for _, e := range cfg.Validate() {
		problems = append(problems, e.Error())
	}
	for name, r := range cfg.Roles {
		p := r.Prompt
		if strings.Contains(p, "\n") {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			if _, err := templates.Role(cfg.Template, name); err != nil {
				problems = append(problems, fmt.Sprintf("role %s points to %s, which does not exist", name, p))
			}
		}
	}
	sort.Strings(problems)
	return problems, nil
}

// Explain renders the compiled config with every default filled in.
func Explain(cfg *domain.ProjectConfig) (string, error) {
	type flowOut struct {
		Steps  []domain.StepDef   `yaml:"steps"`
		Rework map[string]string  `yaml:"rework,omitempty"`
		Limit  domain.ReworkLimit `yaml:"limit"`
	}
	flows := map[string]flowOut{}
	for n, f := range cfg.Flows {
		flows[n] = flowOut{Steps: f.Steps, Rework: f.Rework, Limit: f.Limit}
	}
	view := map[string]any{
		"project": cfg.Name, "template": cfg.Template, "language": cfg.Language, "goal": cfg.Goal,
		"approves": cfg.Approves, "main_branch": cfg.MainBranch, "tracker": cfg.Tracker, "code_host": cfg.CodeHost,
		"checks": cfg.Checks, "roles": cfg.Roles, "flows": flows, "kinds": cfg.KindFlows, "capacity": cfg.Capacity,
		"proposals": map[string]any{"per_agent_per_day": cfg.Proposals.PerAgentPerDay, "expire": cfg.Proposals.Expire.String(), "duplicate": cfg.Proposals.Duplicate},
		"items":     map[string]any{"target_size": cfg.TargetSize, "smaller": cfg.SmallerBecomes, "tweaks": cfg.Tweaks, "same_area_findings": cfg.SameAreaFinding},
		"models":    cfg.Models, "policy": cfg.Policy, "areas": cfg.Areas, "merge": cfg.Merge,
		"lease":  map[string]string{"renew": cfg.Lease.Renew.String(), "ttl": cfg.Lease.TTL.String()},
		"budget": cfg.Budget, "revalidation": map[string]string{"stale_after": cfg.StaleAfter.String()},
		"notifications": cfg.Notifications, "webhooks": cfg.Webhooks, "experiments": cfg.Experiments,
		"worktree": map[string]any{"include": cfg.EnvFiles, "port_base": cfg.PortBase, "safety_commit": cfg.SafetyCommit.String()},
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(view); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// RolePrompt returns the role prompt text from the project or the template.
func RolePrompt(cfg *domain.ProjectConfig, role string) (string, error) {
	r, ok := cfg.Roles[role]
	if !ok {
		return "", fmt.Errorf("role %s is not declared", role)
	}
	if strings.Contains(r.Prompt, "\n") {
		return r.Prompt, nil
	}
	if data, err := os.ReadFile(filepath.Join(cfg.Root, r.Prompt)); err == nil {
		return string(data), nil
	}
	if data, err := templates.Role(cfg.Template, role); err == nil {
		return string(data), nil
	}
	return "", fmt.Errorf("role %s: prompt %s not found in the project or template %s", role, r.Prompt, cfg.Template)
}
