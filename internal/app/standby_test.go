package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Standby is the machine's switch: turning it on stops the sessions running
// here without blaming the agent, and nothing starts while it is on — not by
// the loop, not by an agent asking for work — in a project that is not paused.
func TestStandbyStopsSessionsAndStartsNothing(t *testing.T) {
	e := newEnv(t)
	e.account("fake-1")
	it := e.ready("simple", "long", "x")
	started := make(chan struct{}, 4)
	e.prov.Handle(func(ctx context.Context, _ ports.SessionSpec, _ func(ports.ProviderEvent)) (ports.SessionResult, error) {
		started <- struct{}{}
		<-ctx.Done()
		return ports.SessionResult{}, ctx.Err()
	})
	r := e.svc.Runtime()
	if on, _ := r.Standby(); on {
		t.Fatal("a machine starts out of standby")
	}
	e.fill()
	<-started
	if stopped := r.SetStandby(true, "holidays"); stopped != 1 {
		t.Fatalf("stopped %d sessions, want the one running", stopped)
	}
	if on, why := r.Standby(); !on || why != "holidays" {
		t.Fatalf("standby = %v %q", on, why)
	}
	e.idle()
	if got := e.item(it.ID); got.Attempts != 0 || got.Status != domain.StatusInProgress {
		t.Fatalf("a session stopped by standby is not the agent's fault: %+v", got)
	}
	if e.p.Snapshot().Paused {
		t.Fatal("standby is the machine's switch: the project is not paused")
	}
	// The loop starts nothing, however often it runs.
	r.Tick(e.ctx)
	e.fill()
	if n := len(e.prov.Specs()); n != 1 {
		t.Fatalf("%d sessions opened, want only the one from before standby", n)
	}
	// Neither does an agent that asks for work on this machine.
	other := e.ready("simple", "next", "y")
	_, err := e.svc.NextWork(e.ctx, "demo", slot("implementer"))
	if !errors.Is(err, ErrStandby) || !errors.Is(err, ErrPaused) {
		t.Fatalf("NextWork in standby: %v, want ErrStandby (a kind of ErrPaused)", err)
	}
	if got := e.item(other.ID); got.Status != domain.StatusReady || got.Claim != nil {
		t.Fatalf("an item was claimed in standby: %+v", got)
	}
	// Switching it on again changes nothing and stops nothing.
	if stopped := r.SetStandby(true, "holidays"); stopped != 0 {
		t.Fatalf("stopped %d sessions the second time", stopped)
	}
	// Off: the note goes, and the same loop works again.
	r.SetStandby(false, "ignored")
	if on, why := r.Standby(); on || why != "" {
		t.Fatalf("standby after off = %v %q", on, why)
	}
	e.fill()
	<-started
	if n := len(e.prov.Specs()); n < 2 {
		t.Fatalf("%d sessions opened after standby off, want work to start again", n)
	}
	r.SetStandby(true, "") // leave nothing running for the cleanup
	e.idle()
}

// In standby the machine says so in its heartbeat and gives up the cluster's
// housekeeping, so a machine that works can take it; out of standby it takes
// the duty back when it is free.
func TestStandbyGivesUpHousekeeping(t *testing.T) {
	e := newEnv(t)
	r := e.svc.Runtime()
	r.MaxSessions = -1
	pass := func() {
		r.lastHK["demo"] = time.Time{} // as if a renewal period had passed
		r.housekeeping(e.ctx, e.p, r.machine())
	}
	pass()
	if e.backend.Holder(e.svc.dutyRef(e.p, HousekeepingKey)) != "m1" {
		t.Fatalf("housekeeping held by %q before standby", e.backend.Holder(e.svc.dutyRef(e.p, HousekeepingKey)))
	}
	r.SetStandby(true, "")
	pass()
	if h := e.backend.Holder(e.svc.dutyRef(e.p, HousekeepingKey)); h != "" {
		t.Fatalf("a machine in standby still holds the housekeeping (%q)", h)
	}
	eventually(t, "the heartbeat to say standby", func() bool {
		m := e.p.Snapshot().Machines["m1"]
		return m != nil && m.Standby
	})
	pass() // nothing to give up the second time
	r.SetStandby(false, "")
	pass()
	if e.backend.Holder(e.svc.dutyRef(e.p, HousekeepingKey)) != "m1" {
		t.Fatal("out of standby the machine takes the housekeeping again")
	}
	eventually(t, "the heartbeat to say it works", func() bool {
		m := e.p.Snapshot().Machines["m1"]
		return m != nil && !m.Standby
	})
}

// The merge queue pushes to main: it waits too, and integrates once the
// machine is out of standby.
func TestStandbyHoldsTheMergeQueue(t *testing.T) {
	e := newEnv(t, withGit(), withHost())
	it := e.toQueue("Cards screen", map[string]string{"app/cards.txt": "cards\n", "plan.md": "# plan\n"})
	main := e.repo.RemoteMain(t)
	e.svc.Runtime().SetStandby(true, "")
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 0 || err != nil {
		t.Fatalf("the queue ran in standby: %d %v", n, err)
	}
	e.wantStatus(it.ID, domain.StatusQueued, "integrate")
	if e.repo.RemoteMain(t) != main {
		t.Fatal("main moved while the machine was in standby")
	}
	e.svc.Runtime().SetStandby(false, "")
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 1 || err != nil {
		t.Fatalf("the queue after standby: %d %v", n, err)
	}
	e.wantStatus(it.ID, domain.StatusIntegrated, "integrate")
}

// A runtime that is stopping (an update swaps the binary meanwhile) runs no
// tick: no heartbeat, no duty, no session.
func TestStoppedRuntimeDoesNotTick(t *testing.T) {
	e := newEnv(t)
	e.account("fake-1")
	e.ready("simple", "after stop", "x")
	r := e.svc.Runtime()
	r.StopAll()
	r.Tick(e.ctx)
	e.fill()
	if n := len(e.prov.Specs()); n != 0 {
		t.Fatalf("%d sessions opened after StopAll", n)
	}
	if _, ok := e.p.leases[HousekeepingKey]; ok {
		t.Fatal("a stopping runtime took the housekeeping duty")
	}
}
