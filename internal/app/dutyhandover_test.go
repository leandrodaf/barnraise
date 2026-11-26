package app

import (
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// A machine left on an older binary does not keep the cluster's duties:
// the newer machine takes them at once, so the newer rules run them. A
// holder on the same build keeps its lease until it stops renewing.
func TestNewerMachineTakesDutiesFromAnOlderOne(t *testing.T) {
	e := newEnv(t)
	if e.arb == nil {
		t.Skip("no arbiter")
	}
	e.svc.d.Version, e.svc.d.BuildTime = "0.1.1-55-gbbbbbbb", time.Date(2026, 11, 25, 23, 0, 0, 0, time.UTC)
	set := func(version string) {
		e.p.mu.Lock()
		if e.p.state.Machines == nil {
			e.p.state.Machines = map[string]*domain.Machine{}
		}
		e.p.state.Machines["old"] = &domain.Machine{Name: "old", Version: version, BuildTime: time.Date(2026, 11, 25, 21, 0, 0, 0, time.UTC)}
		e.p.mu.Unlock()
	}
	set("0.1.1-55-gbbbbbbb")
	if _, err := e.arb.Claim(e.ctx, HousekeepingKey, "old"); err != nil {
		t.Fatal(err)
	}
	e.svc.d.BuildTime = time.Date(2026, 11, 25, 21, 0, 0, 0, time.UTC)
	if e.svc.holdDuty(e.ctx, e.p, HousekeepingKey) {
		t.Fatal("took the duty from a machine on the same build")
	}
	e.svc.d.BuildTime = time.Date(2026, 11, 25, 23, 0, 0, 0, time.UTC)
	set("0.1.1-53-gaaaaaaa")
	if !e.svc.holdDuty(e.ctx, e.p, HousekeepingKey) {
		t.Fatal("the newer machine did not take the duty from the older one")
	}
	if info, err := e.arb.Inspect(e.ctx, HousekeepingKey); err != nil || info.Holder != e.svc.d.Machine {
		t.Fatalf("holder %+v %v", info, err)
	}
}
