package app

import (
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// While the merge queue integrates an item, the item says what the queue
// does (a check on the combined result, moving main) and on which machine;
// once the batch is over, it no longer shows a worker.
func TestQueuedItemSaysWhatTheMergeQueueDoes(t *testing.T) {
	e := newEnv(t)
	it := e.ready("simple", "queued", "x")
	e.setStatus(it.ID, domain.StatusQueued)
	cur := e.item(it.ID)
	e.svc.queueState(e.p, []*domain.Item{cur}, "working", "running check main on the combined result")
	snap := e.p.Snapshot()
	var sessions []*domain.SessionInfo
	for _, si := range snap.Sessions {
		if si.ItemID == it.ID {
			sessions = append(sessions, si)
		}
	}
	io := &ItemOverview{Sessions: sessions}
	act, detail := itemActivity(snap, snap.Items[it.ID], io, func(string) bool { return true })
	if act != ActQueued || !strings.Contains(detail, "running check main") || !strings.Contains(detail, e.svc.d.Machine) {
		t.Fatalf("activity %s %q", act, detail)
	}
	if sig := queueSignal(io); sig.Kind != "queue_check" || sig.Arg != "main" {
		t.Fatalf("signal %+v", sig)
	}
	e.svc.queueState(e.p, []*domain.Item{cur}, "done", "")
	if si := e.p.Snapshot().Sessions[e.svc.queueAgent(it.ID)]; si == nil || si.Live() {
		t.Fatalf("worker after the batch: %+v", si)
	}
}
