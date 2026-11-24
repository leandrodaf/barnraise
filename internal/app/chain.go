package app

import (
	"context"
	"fmt"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// ReportChain records a broken hash chain of the local event log (ADR-0029)
// in every project and tells the owner. The daemon calls it when its check
// of the tail (at start and with each anchor) or of the anchor fails; an
// intact report is ignored. The warning itself is appended to the log, so it
// is chained after the break it reports.
func (s *Service) ReportChain(ctx context.Context, rep domain.ChainReport, where string) {
	if rep.OK() {
		return
	}
	note, extra := ChainProblem(rep)
	extra["where"] = where
	for _, name := range s.Projects() {
		p, err := s.Project(name)
		if err != nil {
			continue
		}
		if _, err := s.emit(ctx, p, domain.EvChainBroken, "", "barnraise", domain.Payload{Note: note, Extra: extra}); err != nil {
			s.d.Log.Warn("record the broken event chain", "project", name, "err", err)
		}
		go s.notifyOwner(context.WithoutCancel(ctx), p, "trust", "The event log was changed outside Barnraise", note+"\nRun `barnraise events verify` for the full report.")
	}
	s.d.Log.Warn("event chain broken", "note", note)
}

// ChainProblem describes what is wrong in a report, in one sentence plus
// the fields of the event.
func ChainProblem(rep domain.ChainReport) (string, map[string]any) {
	extra := map[string]any{"checked_from": rep.From, "checked_to": rep.To}
	var note string
	if b := rep.Break; b != nil {
		extra["seq"], extra["reason"] = b.Seq, b.Reason
		switch b.Reason {
		case "link":
			note = fmt.Sprintf("event %d does not follow the event stored before it: an event was removed or inserted there", b.Seq)
		case "missing":
			note = fmt.Sprintf("event %d has no hash: it was written outside Barnraise", b.Seq)
		default:
			note = fmt.Sprintf("event %d does not match its hash: it was edited after it was stored", b.Seq)
		}
	}
	if a := rep.Anchor; a != nil && !a.OK {
		extra["anchor_seq"], extra["anchor"] = a.Seq, a.Reason
		if note != "" {
			note += "; "
		}
		note += "the signed anchor does not hold: " + a.Reason
	}
	return note, extra
}
