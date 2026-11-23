package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ModelChoice is a provider, model and effort for a step.
type ModelChoice struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort,omitempty"`
}

func (m ModelChoice) String() string {
	s := m.Provider + "/" + m.Model
	if m.Effort != "" {
		s += "@" + m.Effort
	}
	return s
}

// ModelRouter picks provider, model and effort by item class, with fallbacks
// and escalation along each provider's ladder.
type ModelRouter struct {
	Classes  map[string][]ModelChoice `json:"classes"`  // simple, normal, complex, native
	Ladders  map[string][]string      `json:"ladders"`  // provider -> models from cheap to strong
	Families map[string]string        `json:"families"` // provider -> model family
}

// DefaultModelRouter mirrors today's routing: native and complex on Fable,
// normal on Opus, simple on Sonnet, Codex as fallback and other family.
func DefaultModelRouter() ModelRouter {
	return ModelRouter{
		Classes: map[string][]ModelChoice{
			"simple":  {{Provider: "claude", Model: "sonnet", Effort: "low"}, {Provider: "codex", Model: "gpt-5.5-mini"}},
			"normal":  {{Provider: "claude", Model: "opus", Effort: "medium"}, {Provider: "claude", Model: "sonnet", Effort: "high"}, {Provider: "codex", Model: "gpt-5.5"}},
			"complex": {{Provider: "claude", Model: "fable", Effort: "high"}, {Provider: "claude", Model: "opus", Effort: "high"}, {Provider: "codex", Model: "gpt-5.5", Effort: "high"}},
			"native":  {{Provider: "claude", Model: "fable", Effort: "high"}, {Provider: "claude", Model: "opus", Effort: "high"}},
			"cheap":   {{Provider: "claude", Model: "haiku"}, {Provider: "claude", Model: "sonnet", Effort: "low"}, {Provider: "codex", Model: "gpt-5.5-mini"}},
		},
		Ladders: map[string][]string{
			"claude": {"haiku", "sonnet", "opus", "fable"},
			"codex":  {"gpt-5.5-mini", "gpt-5.5"},
			"gemini": {"gemini-flash", "gemini-pro"},
			"fake":   {"fake-small", "fake-large"},
		},
		Families: map[string]string{"claude": "anthropic", "codex": "openai", "gemini": "google", "openrouter": "mixed", "fake": "fake"},
	}
}

// Family of a provider.
func (r ModelRouter) Family(provider string) string {
	if f, ok := r.Families[provider]; ok {
		return f
	}
	return provider
}

// Escalate moves a model up its provider's ladder by tier levels.
func (r ModelRouter) Escalate(c ModelChoice, tier int) ModelChoice {
	if tier <= 0 {
		return c
	}
	ladder := r.Ladders[c.Provider]
	idx := -1
	for i, m := range ladder {
		if m == c.Model {
			idx = i
		}
	}
	if idx < 0 {
		return c
	}
	idx += tier
	if idx >= len(ladder) {
		idx = len(ladder) - 1
	}
	c.Model = ladder[idx]
	if c.Effort == "low" || c.Effort == "medium" {
		c.Effort = "high"
	}
	return c
}

// RouteRequest describes what a step needs.
type RouteRequest struct {
	Class       string
	FixedModel  string            // step model: haiku
	Tier        int               // escalation
	AvoidFamily string            // different_from rule
	AvoidModel  string            // fallback when only one family exists
	Usable      func(string) bool // provider has an account with capacity
}

// Route returns the candidates in order of preference.
func (r ModelRouter) Route(req RouteRequest) ([]ModelChoice, error) {
	class := req.Class
	if class == "" || class == "auto" {
		class = "normal"
	}
	var base []ModelChoice
	if req.FixedModel != "" {
		for prov, ladder := range r.Ladders {
			for _, m := range ladder {
				if m == req.FixedModel {
					base = append(base, ModelChoice{Provider: prov, Model: m})
				}
			}
		}
		if len(base) == 0 {
			if p, m, ok := strings.Cut(req.FixedModel, "/"); ok {
				base = append(base, ModelChoice{Provider: p, Model: m})
			}
		}
		base = append(base, r.Classes["cheap"]...)
	} else {
		var ok bool
		base, ok = r.Classes[class]
		if !ok {
			return nil, fmt.Errorf("unknown model class %q", class)
		}
	}
	var preferred, fallback []ModelChoice
	seen := map[string]bool{}
	for _, c := range base {
		c = r.Escalate(c, req.Tier)
		if seen[c.String()] {
			continue
		}
		seen[c.String()] = true
		if req.Usable != nil && !req.Usable(c.Provider) {
			continue
		}
		if req.AvoidFamily != "" && r.Family(c.Provider) == req.AvoidFamily {
			if c.Model != req.AvoidModel {
				fallback = append(fallback, c)
			}
			continue
		}
		preferred = append(preferred, c)
	}
	out := append(preferred, fallback...)
	if len(out) == 0 {
		return nil, fmt.Errorf("no provider with free capacity for class %s", class)
	}
	return out, nil
}

// AccountStatus of a login.
type AccountStatus string

const (
	AccountOK      AccountStatus = "ok"
	AccountExpired AccountStatus = "expired" // login expired: relogin
	AccountLimited AccountStatus = "limited"
	AccountOff     AccountStatus = "off"
)

// Usage is what the status line or the provider reports.
type Usage struct {
	FiveHourPct    float64   `json:"five_hour_pct"`
	WeeklyPct      float64   `json:"weekly_pct"`
	WeeklyTopPct   float64   `json:"weekly_top_pct,omitempty"` // weekly window of the top model (Fable)
	FiveHourResets time.Time `json:"five_hour_resets,omitempty"`
	WeeklyResets   time.Time `json:"weekly_resets,omitempty"`
	ContextPct     float64   `json:"context_pct,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
	Source         string    `json:"source,omitempty"` // statusline, estimate
}

// Account is a login at a provider.
type Account struct {
	ID         string        `json:"id"` // claude-1
	Provider   string        `json:"provider"`
	Plan       string        `json:"plan,omitempty"`
	ConfigDir  string        `json:"config_dir,omitempty"`
	LinuxUser  string        `json:"linux_user,omitempty"`
	Machine    string        `json:"machine,omitempty"`
	CeilingPct float64       `json:"ceiling_pct"` // never pass this weekly share (default 100)
	Status     AccountStatus `json:"status"`
	Usage      Usage         `json:"usage"`
	Projects   []string      `json:"projects,omitempty"` // empty: all
	AddedAt    time.Time     `json:"added_at"`
}

// Available reports whether the account can take a step now.
func (a *Account) Available(now time.Time) bool {
	if a.Status == AccountExpired || a.Status == AccountOff {
		return false
	}
	u := a.Usage
	if u.FiveHourPct >= 100 && (u.FiveHourResets.IsZero() || now.Before(u.FiveHourResets)) {
		return false
	}
	ceiling := a.CeilingPct
	if ceiling <= 0 {
		ceiling = 100
	}
	if u.WeeklyPct >= ceiling && (u.WeeklyResets.IsZero() || now.Before(u.WeeklyResets)) {
		return false
	}
	return true
}

// Load is the higher of the two windows, used to rotate accounts.
func (a *Account) Load() float64 {
	l := a.Usage.FiveHourPct
	if a.Usage.WeeklyPct > l {
		l = a.Usage.WeeklyPct
	}
	return l
}

// PickAccount returns the least loaded available account of a provider.
func PickAccount(accounts []*Account, provider, machine, project string, now time.Time) *Account {
	var cands []*Account
	for _, a := range accounts {
		if a.Provider != provider || !a.Available(now) {
			continue
		}
		if machine != "" && a.Machine != "" && a.Machine != machine {
			continue
		}
		if len(a.Projects) > 0 && !contains(a.Projects, project) {
			continue
		}
		cands = append(cands, a)
	}
	if len(cands) == 0 {
		return nil
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].Load() < cands[j].Load() })
	return cands[0]
}

// PredictExhaustion estimates when a window hits 100% given two readings.
func PredictExhaustion(prev, cur Usage) time.Time {
	dt := cur.UpdatedAt.Sub(prev.UpdatedAt)
	dp := cur.FiveHourPct - prev.FiveHourPct
	if dt <= 0 || dp <= 0 {
		return time.Time{}
	}
	left := 100 - cur.FiveHourPct
	return cur.UpdatedAt.Add(time.Duration(left / dp * float64(dt)))
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// Contains is exported for other layers.
func Contains(s []string, v string) bool { return contains(s, v) }
