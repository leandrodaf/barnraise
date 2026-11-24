package app

import (
	"context"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// rebaseItem rebases the item's worktree in dir onto onto. When git
// resolved conflicts from resolutions it recorded earlier (rerere), it says
// so in the item's timeline and returns the files: the merge queue then runs
// every local check, never trusting a resolution it did not see checked.
func (s *Service) rebaseItem(ctx context.Context, p *Project, it *domain.Item, dir, onto string) ([]string, error) {
	g := p.Ports().Git
	rr, ok := g.(ports.ReusingRebaser)
	if !ok {
		return nil, g.Rebase(ctx, dir, onto)
	}
	out, err := rr.RebaseReusing(ctx, dir, onto)
	if err != nil || len(out.Reapplied) == 0 {
		return nil, err
	}
	files := make([]any, 0, len(out.Reapplied))
	for _, f := range out.Reapplied {
		files = append(files, f)
	}
	_, _ = s.emit(ctx, p, domain.EvConflictsReapplied, it.ID, "merge-queue", domain.Payload{
		Note:  "conflicts with main reapplied from a previous resolution: " + strings.Join(out.Reapplied, ", "),
		Extra: map[string]any{"files": files, "onto": onto},
	})
	return out.Reapplied, nil
}

// reappliedHint is added to a failed check's reason when the tree carried
// resolutions git reapplied, so the agent knows where to look.
func reappliedHint(files []string) string {
	if len(files) == 0 {
		return ""
	}
	return "; the conflicts in " + strings.Join(files, ", ") +
		" were resolved from a resolution recorded earlier (git rerere): if that resolution is wrong, rebase on main, run `git rerere forget <file>`, resolve again and continue"
}
