package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// itemHead is the head commit of an item's worktree ("" when there is none).
func (s *Service) itemHead(ctx context.Context, p *Project, itemID string) string {
	g := p.Ports().Git
	if g == nil {
		return ""
	}
	dir := s.WorktreeDir(p.Name, itemID)
	if _, err := os.Stat(dir); err != nil {
		return ""
	}
	h, err := g.Head(ctx, dir)
	if err != nil {
		return ""
	}
	return h
}

// sendsBack reports whether an outcome may send the work back (and so
// whether the item's head is worth remembering as the start of the fix).
func sendsBack(o domain.Outcome) bool {
	switch o {
	case domain.OutcomePass, domain.OutcomeDone, domain.OutcomeApprove, domain.OutcomeNeedsContext, domain.OutcomeBlocked:
		return false
	}
	return true
}

// resolveRounds marks the rework rounds an item just got past: those the
// step that passed had sent back, or all of them when the item is done.
// It returns copies of the rounds resolved now. Callers hold p.mu and
// pass the item being mutated.
func resolveRounds(it *domain.Item, step string, all bool) []domain.ReworkRecord {
	var out []domain.ReworkRecord
	for _, list := range []*[]domain.ReworkRecord{&it.Rework, &it.PastRework} {
		for i := range *list {
			r := &(*list)[i]
			if r.Resolved || (!all && r.From != step) {
				continue
			}
			r.Resolved = true
			out = append(out, *r)
		}
	}
	return out
}

// pastRework keeps the rounds still to be learned from when the rework
// budget is reset.
func pastRework(it *domain.Item) {
	for _, r := range it.Rework {
		if !r.Resolved {
			it.PastRework = append(it.PastRework, r)
		}
	}
}

// learnAsync distills the lessons of the rounds an item got past. The
// item's head is read now, while its worktree certainly exists.
func (s *Service) learnAsync(ctx context.Context, p *Project, it *domain.Item, rounds []domain.ReworkRecord) {
	if !p.Cfg().Lessons.Enabled || len(rounds) == 0 || it == nil {
		return
	}
	head := s.itemHead(ctx, p, it.ID)
	it = it.Clone()
	s.stale.passes.Add(1)
	go func() {
		defer s.stale.passes.Done()
		bg, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		s.learn(bg, p, it, rounds, head)
	}()
}

// learn turns each resolved round into a lesson, or merges it into a
// similar lesson already known. It returns the lessons written.
func (s *Service) learn(ctx context.Context, p *Project, it *domain.Item, rounds []domain.ReworkRecord, head string) []*domain.Lesson {
	scope := s.itemScope(ctx, p, it, head)
	checks := sortedKeys(p.Cfg().Checks)
	var out []*domain.Lesson
	for _, rr := range rounds {
		fix := scope
		if g := p.Ports().Git; g != nil && rr.Commit != "" && head != "" && rr.Commit != head {
			if changed, err := g.ChangedFiles(ctx, rr.Commit, head); err == nil {
				fix = intersect(changed, scope)
			}
		}
		l := domain.DistillLesson(it, rr, fix, domain.ChecksNamed(rr.Reason, checks), s.d.Clock.Now().UTC())
		if w := s.keepLesson(ctx, p, l); w != nil {
			out = append(out, w)
		}
	}
	return out
}

// itemScope is every file the item changes: its deliveries, its branch and,
// once it landed, its commit on main. Barnraise's own files are left out.
func (s *Service) itemScope(ctx context.Context, p *Project, it *domain.Item, head string) []string {
	files := append([]string(nil), it.FilesTouched...)
	if g := p.Ports().Git; g != nil && head != "" {
		if main, err := g.MainHead(ctx); err == nil && main != "" && main != head {
			if f, err := s.itemFiles(ctx, p, main, head); err == nil {
				files = append(files, f...)
			}
		}
		if it.MergeCommit != "" {
			if f, err := g.ChangedFiles(ctx, it.MergeCommit+"^", it.MergeCommit); err == nil {
				files = append(files, f...)
			}
		}
	}
	var out []string
	for _, f := range domain.Dedupe(files) {
		if !domain.Contains(ItemFiles, f) && f != p.Cfg().Merge.Changelog {
			out = append(out, f)
		}
	}
	return out
}

func intersect(a, b []string) []string {
	var out []string
	for _, x := range a {
		if domain.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

// keepLesson stores a new lesson, or merges it into the most similar one
// known. A round already learned (replayed) is ignored.
func (s *Service) keepLesson(ctx context.Context, p *Project, l *domain.Lesson) *domain.Lesson {
	p.mu.Lock()
	defer p.mu.Unlock()
	src := l.Source()
	for _, cur := range p.state.LessonList() {
		if domain.Contains(cur.Sources, src) {
			return nil
		}
	}
	for _, cur := range p.state.LessonList() {
		if !cur.Similar(l) {
			continue
		}
		next := cur.Clone()
		next.Absorb(l, s.d.Clock.Now().UTC())
		if _, err := s.emitLocked(ctx, p, domain.EvLessonMerged, next.ID, "barnraise", domain.Payload{Lesson: next, Cause: src}); err != nil {
			s.d.Log.Warn("merge a lesson", "lesson", next.ID, "err", err)
			return nil
		}
		return next
	}
	if _, err := s.emitLocked(ctx, p, domain.EvLessonLearned, l.ID, "barnraise", domain.Payload{Lesson: l, Cause: src}); err != nil {
		s.d.Log.Warn("learn a lesson", "lesson", l.ID, "err", err)
		return nil
	}
	return l
}

// lessonsPrompt is the prompt part with the lessons that concern an item:
// those about the files it is anchored to, touched or read, and its areas.
func (s *Service) lessonsPrompt(ctx context.Context, p *Project, it *domain.Item) string {
	cfg := p.Cfg().Lessons
	if !cfg.Enabled || cfg.Max <= 0 {
		return ""
	}
	snap := p.Snapshot()
	if len(snap.Lessons) == 0 {
		return ""
	}
	files := append(append([]string(nil), it.Anchors.Files...), it.FilesTouched...)
	if rl := s.readLog(); rl != nil {
		noted, _ := rl.TouchedFiles(ctx, p.Name, it.ID)
		for _, f := range noted {
			files = append(files, f.Path)
		}
	}
	areas := append([]string{it.Area}, it.Anchors.Areas...)
	picked := domain.RelevantLessons(snap.LessonList(), domain.Dedupe(files), areas, cfg.Max)
	if len(picked) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Lessons from earlier rework in these files (do not repeat them)\n")
	for _, l := range picked {
		fmt.Fprintf(&b, "- [%s] %s\n", l.ID, truncate(l.Text, 400))
	}
	return b.String()
}

// LessonFilter narrows the lesson list.
type LessonFilter struct {
	File string
	Area string
}

// ListLessons lists the project's lessons, the most taught first.
func (s *Service) ListLessons(project string, a Actor, f LessonFilter) ([]*domain.Lesson, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeItemsRead, p.Name); err != nil {
		return nil, err
	}
	all := p.Snapshot().LessonList()
	if f.File == "" && f.Area == "" {
		return all, nil
	}
	var files, areas []string
	if f.File != "" {
		files = []string{f.File}
	}
	if f.Area != "" {
		areas = []string{f.Area}
	}
	return domain.RelevantLessons(all, files, areas, len(all)), nil
}

// DeleteLesson removes a lesson: later prompts no longer get it.
func (s *Service) DeleteLesson(ctx context.Context, project string, a Actor, id string) (*domain.Lesson, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeApprove, p.Name); err != nil {
		return nil, err
	}
	if !domain.IsLessonID(id) {
		return nil, fmt.Errorf("%w: %q is not a lesson id (L-…)", ErrInvalid, id)
	}
	id = domain.NormalizeLessonID(id)
	p.mu.Lock()
	defer p.mu.Unlock()
	cur, ok := p.state.Lessons[id]
	if !ok {
		return nil, fmt.Errorf("%w: lesson %s", ErrNotFound, id)
	}
	gone := cur.Clone()
	if _, err := s.emitLocked(ctx, p, domain.EvLessonDeleted, id, actorName(a), domain.Payload{Lesson: gone}); err != nil {
		return nil, err
	}
	return gone, nil
}
