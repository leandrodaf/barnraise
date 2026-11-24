package main

import (
	"context"
	"time"

	gitx "github.com/leandrodaf/barnraise/internal/adapters/out/git"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// setupMerge prepares how the project's repository merges: recorded
// resolutions (rerere) shared by every worktree and, when installed and
// allowed, a structural merge driver. A failure is logged and the project
// keeps the normal merge.
func (d *daemon) setupMerge(cfg *domain.ProjectConfig, repo *gitx.Repo) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rep, err := repo.SetupMerge(ctx, gitx.MergeSetup{Rerere: cfg.Merge.Rerere, Structural: cfg.Merge.Structural})
	if err != nil {
		d.log.Warn("merge setup", "project", cfg.Name, "err", err)
		return
	}
	if rep.RerereNote != "" {
		d.log.Info("recorded conflict resolutions", "project", cfg.Name, "rerere", rep.Rerere, "note", rep.RerereNote)
	}
	if rep.Structural != "" {
		d.log.Info("structural merge driver in use", "project", cfg.Name, "driver", rep.Structural, "patterns", rep.Patterns)
	} else {
		d.log.Debug("no structural merge driver", "project", cfg.Name, "note", rep.Note)
	}
}
