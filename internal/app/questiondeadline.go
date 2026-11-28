package app

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// decideOverdueQuestions answers, with its recommended option, every open
// question older than questions.decide_after: an item never waits for the
// owner longer than the owner said it may. Q-124 held I-76 three hours
// because the autopilot had no account with room to think it over.
// A housekeeping duty: one machine of the cluster does it.
func (s *Service) decideOverdueQuestions(ctx context.Context, p *Project, now time.Time) {
	after := p.Cfg().Questions.DecideAfter
	if after <= 0 {
		return
	}
	snap := p.Snapshot()
	ids := make([]string, 0, len(snap.Questions))
	for id := range snap.Questions {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		q := snap.Questions[id]
		if q.Answer != "" || q.AskedAt.IsZero() || now.Sub(q.AskedAt) < after {
			continue
		}
		label := q.Recommended()
		if label == "" && len(q.Options) > 0 {
			label = q.Options[0].Label
		}
		if label == "" {
			continue
		}
		note := fmt.Sprintf("Decided by default: nobody answered in %s (questions.decide_after), so the recommended option was taken.", after)
		if _, err := s.Answer(ctx, p.Name, System(), id, label, note); err != nil {
			s.d.Log.Warn("decide an overdue question", "project", p.Name, "question", id, "err", err)
			continue
		}
		s.d.Log.Info("question decided by default", "project", p.Name, "question", id, "option", label, "after", after)
	}
}
