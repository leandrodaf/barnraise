package domain

import (
	"strings"
	"testing"
	"time"
)

func TestChooseOffload(t *testing.T) {
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	ttl := 2 * time.Minute
	m := func(name string, cpuPct, memPct float64) *Machine {
		return &Machine{Name: name, Platform: "linux/amd64", CPUs: 10, MemGB: 100, Load1: cpuPct / 10, FreeMemGB: 100 - memPct, LastSeen: now, Slots: 4}
	}
	busy := *m("here", 95, 40)
	for _, tc := range []struct {
		what  string
		self  Machine
		peers []*Machine
		want  string
	}{
		{"an idle machine keeps its checks", *m("here", 20, 30), []*Machine{m("far", 5, 5)}, ""},
		{"loaded, idle peer: move", busy, []*Machine{m("far", 10, 20)}, "far"},
		{"the least loaded peer wins", busy, []*Machine{m("b", 30, 20), m("a", 10, 60), m("c", 12, 15)}, "c"},
		{"ties go to the name", busy, []*Machine{m("z", 10, 10), m("k", 10, 10)}, "k"},
		{"a busy peer is no help", busy, []*Machine{m("far", 80, 20)}, ""},
		{"the gap must pay for the move", *m("here", 86, 10), []*Machine{m("far", 48, 68)}, ""},
		{"no peers", busy, nil, ""},
		{"memory pressure moves it too", *m("here", 10, 92), []*Machine{m("far", 20, 30)}, "far"},
		{"a peer short of memory is skipped", busy, []*Machine{func() *Machine { p := m("far", 5, 5); p.MemGB, p.FreeMemGB = 8, 4; return p }()}, ""},
	} {
		got, why := ChooseOffload(tc.self, tc.peers, now, ttl)
		if got != tc.want {
			t.Errorf("%s: %q, want %q (%s)", tc.what, got, tc.want, why)
		}
		if got != "" && (!strings.Contains(why, tc.self.Name) || !strings.Contains(why, got)) {
			t.Errorf("%s: the reason must name both machines: %q", tc.what, why)
		}
	}
	// Peers that cannot take it, whatever their load.
	for what, change := range map[string]func(*Machine){
		"gone silent":        func(p *Machine) { p.LastSeen = now.Add(-10 * time.Minute) },
		"late heartbeat":     func(p *Machine) { p.LastSeen = now.Add(-90 * time.Second) },
		"leaving":            func(p *Machine) { p.Leaving = true },
		"left":               func(p *Machine) { p.Left = true },
		"another platform":   func(p *Machine) { p.Platform = "darwin/arm64" },
		"no readings":        func(p *Machine) { p.Load1, p.FreeMemGB = 0, 0 },
		"every slot taken":   func(p *Machine) { p.Busy = p.Slots },
		"it is this machine": func(p *Machine) { p.Name = "here" },
	} {
		p := m("far", 5, 5)
		change(p)
		if got, _ := ChooseOffload(busy, []*Machine{p, nil}, now, ttl); got != "" {
			t.Errorf("%s: moved to %q", what, got)
		}
	}
	// A machine starved of memory moves to any idle peer, without the usual gap.
	starved := *m("here", 10, 30)
	starved.MemGB, starved.FreeMemGB = 4, 1.5
	if got, why := ChooseOffload(starved, []*Machine{m("far", 40, 50)}, now, ttl); got != "far" || !strings.Contains(why, "1.5 GB") {
		t.Errorf("starved: %q %q", got, why)
	}
	if loaded, _ := (&Machine{}).Loaded(); loaded {
		t.Error("a machine that reports nothing is not called loaded")
	}
}
