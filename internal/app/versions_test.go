package app

import (
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// A machine is outdated when a peer runs a later build; it names the peer.
func TestVersionsOfTheCluster(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	e.svc.d.Version, e.svc.d.BuildTime = "8cbf79a", day
	if v := e.svc.Versions(); v.Outdated || v.Self.Version != "8cbf79a" {
		t.Fatalf("alone: %+v", v)
	}
	e.live(domain.EvMachineUpdated, "m2", domain.Payload{MachineI: &domain.Machine{Name: "m2", Version: "9a64ec7", BuildTime: day.Add(time.Hour), LastSeen: e.clock.Now()}})
	e.live(domain.EvMachineUpdated, "m3", domain.Payload{MachineI: &domain.Machine{Name: "m3", Version: "7aaaaaa", BuildTime: day.Add(-time.Hour), LastSeen: e.clock.Now()}})
	v := e.svc.Versions()
	if !v.Outdated || v.NewestMachine != "m2" || v.Newest.Version != "9a64ec7" {
		t.Fatalf("behind m2: %+v", v)
	}
	// A machine that left does not count.
	e.live(domain.EvMachineLeft, "m2", domain.Payload{MachineI: &domain.Machine{Name: "m2", Version: "9a64ec7", BuildTime: day.Add(time.Hour), Left: true}})
	if v := e.svc.Versions(); v.Outdated {
		t.Fatalf("a machine that left: %+v", v)
	}
}
