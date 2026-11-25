package app

import (
	"context"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// machineDistrust lists what refuses every session of the project on this
// machine: a changed hook binary, or a project root (barnraise.yaml,
// .barnraise/) changed outside Barnraise. Each problem is recorded and told
// to the owner once. An error reading them counts as no problem here: the
// session's own check still refuses it.
func (s *Service) machineDistrust(ctx context.Context, p *Project) []TrustProblem {
	probs, err := s.provenanceProblems(ctx, p, "", nil)
	if err != nil {
		return nil
	}
	for _, pr := range probs {
		s.trustMismatch(ctx, p, "", pr.Scope, pr.Changes, pr.Fix)
	}
	return probs
}

// standDown is what a machine whose sessions are all refused does: it gives
// the items it holds back to the queue, so a machine that can run them takes
// them, and leaves the duties that start sessions (housekeeping, which
// refines drafts, and the autopilot) to another machine. Without it the
// machine kept claiming ready items and the analyst's drafts, failed to
// start every one of them and held them for as long as nobody re-trusted
// its files. Items with a session still running here keep it: the session
// started before the change and ends on its own.
func (s *Service) standDown(ctx context.Context, p *Project, busy map[string]bool, why string) []string {
	s.releaseLease(ctx, p, HousekeepingKey)
	s.releaseLease(ctx, p, AutopilotKey)
	var returned []string
	for _, it := range p.Snapshot().ItemList() {
		if it.Claim == nil || it.Claim.Machine != s.d.Machine || it.Status != domain.StatusInProgress || busy[it.ID] {
			continue
		}
		p.mu.Lock()
		_, running := p.running[it.ID]
		p.mu.Unlock()
		if running {
			continue
		}
		_, _ = s.commitStep(ctx, p, it.ID, it.ID+": machine "+s.d.Machine+" cannot start sessions, work saved")
		_, err := s.mutateItem(ctx, p, it.ID, domain.EvItemReleased, "barnraise", func(x *domain.Item) error {
			if x.Claim == nil || x.Claim.Machine != s.d.Machine || x.Status != domain.StatusInProgress {
				return ErrConflict // it moved meanwhile: nothing to give back
			}
			x.Claim = nil
			x.Status = domain.StatusReady
			return nil
		}, domain.Payload{Note: "machine " + s.d.Machine + " cannot start sessions (" + why + "); the item continues from its last step on another machine"})
		if err != nil {
			continue
		}
		s.releaseLease(ctx, p, it.ID)
		returned = append(returned, it.ID)
	}
	if len(returned) > 0 {
		s.d.Log.Warn("items given back: this machine cannot start sessions", "project", p.Name, "items", returned, "why", why)
	}
	return returned
}
