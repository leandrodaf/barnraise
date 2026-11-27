package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// announceClaim tells the people who read the tracker that this item is
// being worked on, where and on which branch, once per claim: the status
// label alone reads as a tag, and someone could pick the same issue up by
// hand. It runs on its own; a tracker that fails is noted like any other.
func (s *Service) announceClaim(p *Project, it *domain.Item) {
	t := p.Ports().Tracker
	if t == nil || it == nil || it.TrackerRef == "" {
		return
	}
	text := claimNotice(p.Cfg().Language, it, s.d.Machine)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		err := t.Comment(ctx, it.TrackerRef, text)
		if err != nil {
			s.d.Log.Warn("tracker claim notice", "item", it.ID, "err", err)
		}
		s.noteTracker(p.Name, err)
	}()
}

// claimNotice is the comment, in the project's language for people.
func claimNotice(lang string, it *domain.Item, machine string) string {
	if strings.HasPrefix(strings.ToLower(lang), "pt") {
		return fmt.Sprintf("🔨 **Em andamento no Barnraise** como %s, na máquina `%s`, branch `%s`.\n\n"+
			"Enquanto esta issue tiver o label `%s`, os agentes estão trabalhando nela: não pegue este item à mão. "+
			"Para assumir, use `barnraise takeover %s` (os agentes param e o trabalho fica com você).",
			it.ID, machine, it.Branch, statusLabelOf(domain.StatusInProgress), it.ID)
	}
	return fmt.Sprintf("🔨 **In progress in Barnraise** as %s, on machine `%s`, branch `%s`.\n\n"+
		"While this issue has the `%s` label, agents are working on it: do not pick this item up by hand. "+
		"To take it over, run `barnraise takeover %s` (the agents stop and the work is yours).",
		it.ID, machine, it.Branch, statusLabelOf(domain.StatusInProgress), it.ID)
}

func statusLabelOf(st domain.Status) string { return "status:" + string(st) }
