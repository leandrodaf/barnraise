package app

import (
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// An approved item that has not landed for an hour shows on every screen,
// whatever keeps it, and so does the error a machine's queue reports.
func TestQueueAlerts(t *testing.T) {
	now := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	snap := &domain.State{Items: map[string]*domain.Item{
		"I-57": {ID: "I-57", Status: domain.StatusQueued, Step: "integrate", StepStartedAt: now.Add(-7 * time.Hour), Claim: &domain.Claim{Machine: "dma"}},
		"I-62": {ID: "I-62", Status: domain.StatusQueued, Step: "integrate", StepStartedAt: now.Add(-10 * time.Minute)},
	}}
	machines := []MachineView{
		{Machine: domain.Machine{Name: "dma", QueueErr: "the code host refuses this machine's token the checks"}, Health: "green"},
		{Machine: domain.Machine{Name: "gone", QueueErr: "old"}, Health: "red"},
	}
	got := strings.Join(queueAlerts(snap, machines, now), "\n")
	for _, want := range []string{"merge queue on dma cannot land: the code host refuses", "I-57 is approved and has waited 7h0m0s to land on dma"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "I-62") || strings.Contains(got, "gone") {
		t.Errorf("alerts for a fresh item or a machine that is out:\n%s", got)
	}
}
