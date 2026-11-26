package app

import (
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// The merge queue works without a session: it rebases, runs the checks on
// the combined result and moves main, for minutes, and nothing said so. It
// now shows as a worker of each item in the batch, like an agent, with
// what it does right now, wherever agents show (the queue board, the item's page,
// the agents list, every machine of the cluster).

// queueAgent names the merge queue's worker for one item on this machine.
func (s *Service) queueAgent(itemID string) string {
	return "merge-queue-" + s.d.Machine + "-" + itemID
}

// queueState says what the merge queue does now for the items of a batch.
// state "working" while it runs, "done" when the batch landed or not.
func (s *Service) queueState(p *Project, items []*domain.Item, state, detail string) {
	var ids []string
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	if len(ids) > 1 {
		detail += " (batch " + strings.Join(ids, " + ") + ")"
	}
	now := s.d.Clock.Now().UTC()
	for _, it := range items {
		info := &domain.SessionInfo{Agent: s.queueAgent(it.ID), Role: "merge-queue", State: state, Detail: truncate(detail, 200),
			At: now, Machine: s.d.Machine, ItemID: it.ID, Step: def(it.Step, "integrate")}
		s.emitEphemeral(p, domain.EvAgentSession, it.ID, info.Agent, domain.Payload{Session: info})
	}
}
