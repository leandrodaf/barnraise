package app

import (
	"testing"

	"github.com/leandrodaf/barnraise/internal/testkit"
)

// Two clusters that are not paired share one repository: each runs its own
// duties. Two machines of one cluster share them: one of them runs each.
func TestDutiesAreHeldPerCluster(t *testing.T) {
	backend := testkit.NewArbiterBackend()
	mine := newEnv(t, withMachine("leandro"), withBackend(backend))
	mine.ready("feature", "this cluster's first item", "")
	other := newEnv(t, withMachine("apolo"), withBackend(backend))
	other.ready("feature", "the other cluster's first item", "")
	if !other.svc.holdHousekeeping(other.ctx, other.p) {
		t.Fatal("the other cluster holds its housekeeping")
	}
	if !mine.svc.holdHousekeeping(mine.ctx, mine.p) {
		t.Fatal("an unpaired cluster on the same repository took this one's housekeeping")
	}
	// A second machine of this cluster: it sees the first one in the
	// replicated state and leaves the duty to it.
	dma := newEnv(t, withMachine("dma"), withBackend(backend))
	dma.sync(mine)
	mine.svc.releaseLease(mine.ctx, mine.p, HousekeepingKey)
	if !mine.svc.holdHousekeeping(mine.ctx, mine.p) {
		t.Fatal("leandro holds the duty again")
	}
	if dma.svc.holdHousekeeping(dma.ctx, dma.p) {
		t.Fatal("two machines of one cluster both hold its housekeeping")
	}
	if got, want := dma.svc.dutyRef(dma.p, HousekeepingKey), mine.svc.dutyRef(mine.p, HousekeepingKey); got != want {
		t.Fatalf("one cluster, two names for its duty: %s and %s", got, want)
	}
}

// In a real repository the shared lease of an older version exists already
// (lease/housekeeping): the cluster's own lease must not be named under it,
// since git cannot have a ref below another ref.
func TestClusterDutyBesideTheSharedLeaseInGit(t *testing.T) {
	e := newEnv(t, withGit())
	if _, err := e.arb.Claim(e.ctx, HousekeepingKey, "apolo"); err != nil {
		t.Fatal(err)
	}
	e.ready("feature", "first", "")
	if !e.svc.holdHousekeeping(e.ctx, e.p) {
		t.Fatalf("the cluster's housekeeping (%s) could not be claimed beside %s", e.svc.dutyRef(e.p, HousekeepingKey), HousekeepingKey)
	}
}
