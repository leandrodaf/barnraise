package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// ChangeSize is what an item changes on its branch, as items.max_change
// counts it.
type ChangeSize struct {
	Files int `json:"files"`
	Lines int `json:"lines"`
}

// changeSize measures the item's branch from where it left main: the files
// and the added and removed lines items.max_change counts (its exclusions,
// the item's own files and the repository map left out; a binary file
// counts as a file with no lines).
func (s *Service) changeSize(ctx context.Context, p *Project, itemID string) (ChangeSize, error) {
	var out ChangeSize
	g := p.Ports().Git
	if g == nil {
		return out, nil
	}
	main, err := g.MainHead(ctx)
	if err != nil {
		return out, err
	}
	head, err := g.Head(ctx, s.WorktreeDir(p.Name, itemID))
	if err != nil {
		return out, err
	}
	if base, err := g.MergeBase(ctx, main, head); err == nil {
		main = base
	}
	diff, err := g.Diff(ctx, main, head, 0)
	if err != nil {
		return out, err
	}
	return measureDiff(diff, p.Cfg().MaxChange), nil
}

// measureDiff counts a unified diff's files and changed lines.
func measureDiff(diff string, m domain.MaxChange) ChangeSize {
	var out ChangeSize
	counting := false
	for line := range strings.SplitSeq(diff, "\n") {
		if rest, ok := strings.CutPrefix(line, "diff --git a/"); ok {
			path := rest
			if i := strings.Index(rest, " b/"); i >= 0 {
				path = rest[:i]
			}
			counting = m.Counts(path) && !domain.Contains(ItemFiles, path) && path != domain.RepoMapFile
			if counting {
				out.Files++
			}
			continue
		}
		if !counting || strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			out.Lines++
		}
	}
	return out
}

// overMaxChange says why a size passes items.max_change, "" when it fits.
func overMaxChange(size ChangeSize, m domain.MaxChange) string {
	var over []string
	if m.Files > 0 && size.Files > m.Files {
		over = append(over, fmt.Sprintf("%d files (the limit is %d)", size.Files, m.Files))
	}
	if m.Lines > 0 && size.Lines > m.Lines {
		over = append(over, fmt.Sprintf("%d changed lines (the limit is %d)", size.Lines, m.Lines))
	}
	return strings.Join(over, " and ")
}

// excludedNote names what items.max_change does not count, for a message.
func excludedNote(m domain.MaxChange) string {
	if len(m.Exclude) == 0 {
		return ""
	}
	return ". Not counted: " + strings.Join(m.Exclude, ", ")
}

// maxChangeNote tells the agent the limit before it plans or works.
func maxChangeNote(m domain.MaxChange) string {
	if !m.On() {
		return ""
	}
	var lim []string
	if m.Files > 0 {
		lim = append(lim, fmt.Sprintf("%d files", m.Files))
	}
	if m.Lines > 0 {
		lim = append(lim, fmt.Sprintf("%d changed lines", m.Lines))
	}
	return "Size limit (items.max_change): one item changes at most " + strings.Join(lim, " and ") + excludedNote(m) +
		". When the work is larger, the plan splits it: this item does a part that meets its criteria on its own, and the rest is proposed as new items (propose). A delivery past the limit is refused.\n"
}
