package app

import (
	"fmt"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// queueSlow is how long an approved item may wait to land before the
// owner sees it on screen. A pass rebases, checks and pushes in minutes;
// an item still queued after this is stuck, whatever the cause.
const queueSlow = time.Hour

// queueAlerts tells the owner, from any machine, what keeps items from
// landing: the error a machine's merge queue reports, and every item that
// has waited at integrate for longer than queueSlow. Before, a machine
// whose queue could not land waited silently for hours.
func queueAlerts(snap *domain.State, machines []MachineView, now time.Time) []string {
	var out []string
	for _, m := range machines {
		if m.QueueErr != "" && m.Health != "red" && !m.Left {
			out = append(out, fmt.Sprintf("merge queue on %s cannot land: %s", m.Name, m.QueueErr))
		}
		if m.Waiting != "" && m.Health != "red" && !m.Left && (m.WaitingUntil.IsZero() || m.WaitingUntil.After(now)) {
			msg := fmt.Sprintf("work on %s waits for capacity: %s", m.Name, m.Waiting)
			if !m.WaitingUntil.IsZero() {
				msg += fmt.Sprintf(" (the first frees up at %s)", m.WaitingUntil.Local().Format("Mon 15:04"))
			}
			out = append(out, msg)
		}
	}
	for _, it := range snap.ItemList() {
		if it.Status != domain.StatusQueued || it.StepStartedAt.IsZero() {
			continue
		}
		wait := now.Sub(it.StepStartedAt)
		if wait < queueSlow {
			continue
		}
		where := ""
		if it.Claim != nil {
			where = " on " + it.Claim.Machine
		}
		out = append(out, fmt.Sprintf("%s is approved and has waited %s to land%s", it.ID, wait.Round(time.Minute), where))
	}
	return out
}
