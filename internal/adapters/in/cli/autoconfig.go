package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/leandrodaf/barnraise/internal/config"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// AutoOptions are the few answers AutoConfig takes from the owner; the
// rest comes from the repository itself.
type AutoOptions struct {
	Name     string // project name; empty takes the folder's
	Language string // pt-BR, en…; empty takes the machine's
}

// AutoConfig writes barnraise.yaml (and .barnraise/, and one file per part
// of a repository with several) from what the repository holds: the
// answers `barnraise init --yes` takes, with no terminal. A repository that
// already has a barnraise.yaml is left as it is; wrote says which happened.
func AutoConfig(root string, o AutoOptions) (wrote bool, err error) {
	if _, err := os.Stat(filepath.Join(root, config.FileName)); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	d := Detect(root)
	a := InitAnswers{Project: o.Name, Template: def(d.Template, "blank"), Check: d.Check, Approves: "owner", Language: def(o.Language, detectLang()), Tracker: "local"}
	if d.Host == "github" && d.Remote != "" {
		a.Tracker, a.Repo = "github", d.Remote
	}
	parts := DetectParts(root)
	if len(parts) < 2 {
		parts = nil
	} else {
		a.Template, a.Check = "blank", ""
	}
	yml := renderYAML(a)
	cfg, err := config.Compile(root, []byte(yml))
	if err == nil {
		if errs := cfg.Validate(); len(errs) > 0 {
			err = errors.Join(errs...)
		}
	}
	if err != nil {
		return false, fmt.Errorf("the barnraise.yaml written from %s is not valid: %w", root, err)
	}
	for _, p := range parts {
		file := filepath.Join(root, filepath.FromSlash(p.Dir), config.FileName)
		if _, err := os.Stat(file); err == nil {
			continue
		}
		if err := os.WriteFile(file, []byte(renderPart(p)), 0o644); err != nil {
			return false, err
		}
	}
	if err := os.WriteFile(filepath.Join(root, config.FileName), []byte(yml), 0o644); err != nil {
		return false, err
	}
	return true, scaffold(root, a.Template)
}

// Readiness is what stops agents of a project on this machine, one plain
// sentence each, as `barnraise up` checks it.
func Readiness(ctx context.Context, cfg *domain.ProjectConfig) []string {
	return defaultReadiness().problems(ctx, cfg)
}
