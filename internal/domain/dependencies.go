package domain

import (
	"fmt"
	"strings"
	"time"
)

// Built-in check kinds: checks Barnraise runs itself, without a command.
const (
	// RunsOnDependencies looks for known vulnerabilities in the
	// dependencies a change adds or updates (ADR-0034).
	RunsOnDependencies = "builtin:dependencies"
	// RunsOnASTGrep runs `ast-grep scan` with the project's structural
	// rules (sgconfig.yml), when ast-grep is installed.
	RunsOnASTGrep = "builtin:ast-grep"
	// DependencyCheck is the name of the check Barnraise adds by itself.
	DependencyCheck = "dependencies"
)

// BuiltinChecks are the runs_on values of the built-in check kinds.
var BuiltinChecks = []string{RunsOnDependencies, RunsOnASTGrep}

// DependencyManifests are the files that declare or pin dependencies. A
// change to one of them runs the dependencies check.
var DependencyManifests = []string{
	"**/go.mod", "**/go.sum",
	"**/package.json", "**/package-lock.json", "**/npm-shrinkwrap.json", "**/pnpm-lock.yaml", "**/yarn.lock",
	"**/requirements*.txt", "**/Pipfile.lock", "**/poetry.lock", "**/uv.lock", "**/pyproject.toml",
	"**/Cargo.toml", "**/Cargo.lock",
	"**/Gemfile", "**/Gemfile.lock",
	"**/build.gradle", "**/build.gradle.kts", "**/gradle.lockfile", "**/pom.xml",
}

// IsDependencyManifest reports whether a repository path declares or pins
// dependencies.
func IsDependencyManifest(file string) bool { return matchAny(DependencyManifests, file) }

// Severities, lowest first. "unknown" is an advisory that gives none.
var Severities = []string{"low", "medium", "high", "critical"}

// SeverityRank orders severities: 0 is none or unknown, 1 low … 4
// critical. "moderate" (GitHub's word) is medium.
func SeverityRank(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "low":
		return 1
	case "medium", "moderate":
		return 2
	case "high":
		return 3
	case "critical":
		return 4
	}
	return 0
}

// SeverityOfScore maps a CVSS base score to its qualitative rating.
func SeverityOfScore(score float64) string {
	switch {
	case score >= 9:
		return "critical"
	case score >= 7:
		return "high"
	case score >= 4:
		return "medium"
	case score > 0:
		return "low"
	}
	return ""
}

// DependencyPolicy is the `dependencies:` key of barnraise.yaml: the
// built-in check of the dependencies a change adds or updates.
type DependencyPolicy struct {
	// Off disables the automatic check.
	Off bool `json:"off,omitempty"`
	// FailAt is the least severity that fails the check (default high).
	FailAt string `json:"fail_at,omitempty"`
	// Unknown is the severity given to an advisory that states none
	// (default high: an unrated advisory is not a harmless one).
	Unknown string `json:"unknown,omitempty"`
	// Ignore lists advisory ids (or their aliases) the owner accepted.
	Ignore []string `json:"ignore,omitempty"`
	// Scanner is auto (osv-scanner when installed, else the OSV API),
	// osv-scanner or api.
	Scanner string `json:"scanner,omitempty"`
	// API is the base URL of an OSV-compatible service (default
	// https://api.osv.dev).
	API string `json:"api,omitempty"`
}

// Fails reports whether an advisory of this severity fails the check.
func (p DependencyPolicy) Fails(severity string) bool {
	r := SeverityRank(severity)
	if r == 0 {
		r = SeverityRank(def0(p.Unknown, "high"))
	}
	return r >= SeverityRank(def0(p.FailAt, "high"))
}

// Ignored reports whether the owner accepted any of these advisory ids.
func (p DependencyPolicy) Ignored(ids ...string) bool {
	for _, id := range ids {
		for _, ig := range p.Ignore {
			if strings.EqualFold(strings.TrimSpace(ig), id) {
				return true
			}
		}
	}
	return false
}

// Validate checks the values of the policy.
func (p DependencyPolicy) Validate() []error {
	var errs []error
	for key, v := range map[string]string{"fail_at": p.FailAt, "unknown": p.Unknown} {
		if v != "" && SeverityRank(v) == 0 {
			errs = append(errs, fmt.Errorf("dependencies.%s is %q; use low, medium, high or critical", key, v))
		}
	}
	switch p.Scanner {
	case "", "auto", "osv-scanner", "api":
	default:
		errs = append(errs, fmt.Errorf("dependencies.scanner is %q; use auto, osv-scanner or api", p.Scanner))
	}
	return errs
}

func def0(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}

// AddDependencyCheck adds the built-in dependencies check, unless the
// policy turns it off or the project already has a check of that name
// (the project's own wins).
func (c *ProjectConfig) AddDependencyCheck() {
	if c.Dependencies.Off {
		return
	}
	if _, ok := c.Checks[DependencyCheck]; ok {
		return
	}
	if c.Checks == nil {
		c.Checks = map[string]CheckDef{}
	}
	c.Checks[DependencyCheck] = CheckDef{
		Name: DependencyCheck, RunsOn: RunsOnDependencies, Scope: "touched",
		Paths: append([]string(nil), DependencyManifests...), Timeout: 10 * time.Minute,
	}
}

// AutoChecks adds the built-in dependencies check to the checks a step
// runs when the step is the last check step of a flow: once per item,
// right before integration, whatever check the flow names there.
func (c *ProjectConfig) AutoChecks(st StepDef, names []string) []string {
	d, ok := c.Checks[DependencyCheck]
	if !ok || d.RunsOn != RunsOnDependencies || Contains(names, DependencyCheck) || !c.lastCheckStep(st) {
		return names
	}
	return append(append([]string(nil), names...), DependencyCheck)
}

// lastCheckStep reports whether st is the last check step of a flow.
func (c *ProjectConfig) lastCheckStep(st StepDef) bool {
	if st.Type != StepCheck {
		return false
	}
	for _, f := range c.Flows {
		var last *StepDef
		for i := range f.Steps {
			if f.Steps[i].Type == StepCheck {
				last = &f.Steps[i]
			}
		}
		if last != nil && last.Name == st.Name && last.Run == st.Run {
			return true
		}
	}
	return false
}

// isBuiltinCheck reports whether runs_on names a built-in check kind.
func isBuiltinCheck(runsOn string) bool { return Contains(BuiltinChecks, runsOn) }
