package app

import (
	"errors"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func TestMachineReadingsAreKeptAsAShortWindowInMemory(t *testing.T) {
	e := newEnv(t)
	base := e.clock.Now()
	m := func(name string, load float64) *domain.Machine {
		return &domain.Machine{Name: name, CPUs: 8, MemGB: 32, FreeMemGB: 8, Load1: load, TempC: 51, Slots: 4, Busy: 1,
			GPUs: []domain.GPUStat{{Name: "a", UtilPct: 10, TempC: 40}, {Name: "b", UtilPct: 70, TempC: 66}}}
	}
	// One heartbeat per project arrives together: one sample is kept.
	e.svc.telemetry.add(m("m1", 2), base)
	e.svc.telemetry.add(m("m1", 2), base.Add(time.Second))
	e.svc.telemetry.add(m("m1", 4), base.Add(30*time.Second))
	e.svc.telemetry.add(m("m2", 8), base.Add(30*time.Second))
	e.svc.telemetry.add(nil, base)
	e.clock.Advance(time.Minute)

	all, err := e.svc.MachineHistory(e.ctx, "demo", owner(), "", 0)
	if err != nil || len(all) != 2 || len(all["m1"]) != 2 || len(all["m2"]) != 1 {
		t.Fatalf("history %+v %v", all, err)
	}
	s := all["m1"][1]
	if s.CPUPct != 50 || s.MemPct != 75 || s.TempC != 51 || s.GPUPct != 70 || s.GPUTempC != 66 || s.Busy != 1 || s.Slots != 4 {
		t.Fatalf("sample %+v", s)
	}
	recent, _ := e.svc.MachineHistory(e.ctx, "demo", owner(), "m1", 45*time.Second)
	if len(recent["m1"]) != 1 {
		t.Fatalf("window: %+v", recent)
	}
	if _, err := e.svc.MachineHistory(e.ctx, "demo", owner(), "ghost", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown machine: %v", err)
	}
	if _, err := e.svc.MachineHistory(e.ctx, "demo", Actor{}, "", 0); !errors.Is(err, ErrForbidden) {
		t.Fatalf("without scope: %v", err)
	}
	// The window is bounded: old samples leave as new ones arrive.
	for i := 0; i < samplesKept+50; i++ {
		e.svc.telemetry.add(m("m3", 1), base.Add(time.Duration(i)*sampleEvery))
	}
	if got := len(e.svc.telemetry.get("m3", time.Time{})["m3"]); got != samplesKept {
		t.Fatalf("kept %d samples, want %d", got, samplesKept)
	}
	// Heartbeats feed it: this machine's own, and the ones mirrored from others.
	e.svc.emitEphemeral(e.p, domain.EvMachineUpdated, "self", "barnraise", domain.Payload{MachineI: m("self", 1)})
	peer := domain.NewEvent("hb-peer", domain.EvMachineUpdated, "demo", "far", "far", "barnraise", e.clock.Now(), domain.Payload{MachineI: m("far", 3)})
	if err := e.svc.Ingest(e.ctx, peer); err != nil {
		t.Fatal(err)
	}
	now, _ := e.svc.MachineHistory(e.ctx, "demo", owner(), "", 0)
	if len(now["self"]) != 1 || len(now["far"]) != 1 {
		t.Fatalf("heartbeats must be sampled: %v", now)
	}
	if evs, _ := e.store.Load(e.ctx, "demo", 0); len(evs) != 0 {
		for _, ev := range evs {
			if ev.Type == domain.EvMachineUpdated {
				t.Fatal("readings are never stored in the event log")
			}
		}
	}
}
