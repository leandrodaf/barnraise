package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// dependenciesField is the `dependencies:` key: `off` (or false), or a
// mapping that tunes the built-in dependencies check.
type dependenciesField struct {
	Check   *bool    `yaml:"check"`
	FailAt  string   `yaml:"fail_at"`
	Unknown string   `yaml:"unknown"`
	Ignore  []string `yaml:"ignore"`
	Scanner string   `yaml:"scanner"`
	API     string   `yaml:"api"`
}

func (d *dependenciesField) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		switch strings.ToLower(strings.TrimSpace(n.Value)) {
		case "off", "false", "no":
			off := false
			d.Check = &off
			return nil
		case "on", "true", "yes", "":
			return nil
		}
		return fmt.Errorf("dependencies is %q; use off, or a mapping (fail_at, ignore, scanner)", n.Value)
	}
	type plain dependenciesField
	return n.Decode((*plain)(d))
}

func (d dependenciesField) policy() domain.DependencyPolicy {
	return domain.DependencyPolicy{
		Off: d.Check != nil && !*d.Check, FailAt: strings.ToLower(strings.TrimSpace(d.FailAt)),
		Unknown: strings.ToLower(strings.TrimSpace(d.Unknown)), Ignore: d.Ignore,
		Scanner: strings.ToLower(strings.TrimSpace(d.Scanner)), API: strings.TrimRight(strings.TrimSpace(d.API), "/"),
	}
}

// lookPath finds a program (replaced in tests).
var lookPath = exec.LookPath

// builtinCheckProblems reports an ast-grep check on a machine without
// ast-grep, or without the rules it would run.
func builtinCheckProblems(root string, cfg *domain.ProjectConfig) []string {
	var out []string
	for name, ch := range cfg.Checks {
		if ch.RunsOn != domain.RunsOnASTGrep {
			continue
		}
		if _, err := lookPath("ast-grep"); err != nil {
			out = append(out, fmt.Sprintf("check %s runs ast-grep, which is not on the PATH of this machine", name))
		}
		if namesConfig(ch.Run) {
			continue // the check names its own rules file
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(ch.Dir), "sgconfig.yml")); err != nil {
			out = append(out, fmt.Sprintf("check %s runs ast-grep, but %s has no sgconfig.yml with its rules", name, filepath.ToSlash(filepath.Join(".", ch.Dir))))
		}
	}
	return out
}

func namesConfig(args string) bool {
	for _, f := range strings.Fields(args) {
		if f == "-c" || f == "--config" || strings.HasPrefix(f, "--config=") {
			return true
		}
	}
	return false
}
