package domain

import (
	"fmt"
	"sort"
	"time"
)

// A check normally runs where the item is. When that machine is loaded and
// another machine of the cluster is idle, the check goes there: the other
// machine checks the item's commit out on its own, runs the command and
// removes what it created. These numbers decide when that is worth it.
const (
	// A machine is loaded when its CPUs or its memory are this busy (%)…
	OffloadBusyPct = 85.0
	// …or when it has less than this much memory available (GB).
	OffloadMinFreeMemGB = 3.0
	// A machine may take work from another when it is at most this busy (%)…
	OffloadIdleCPUPct = 50.0
	OffloadIdleMemPct = 70.0
	// …and at least this much less busy than the machine that asks (points).
	OffloadMinGain = 25.0
)

// load is how busy a machine is, 0 to 100: the worse of CPU and memory.
// ok is false when the machine reports neither.
func (m *Machine) load() (pct float64, ok bool) {
	cpu, mem := m.CPUUsedPct(), m.MemUsedPct()
	if cpu == 0 && mem == 0 {
		return 0, false
	}
	return max(cpu, mem), true
}

// Loaded says whether a machine is too busy to take one more check, and why.
func (m *Machine) Loaded() (bool, string) {
	if cpu := m.CPUUsedPct(); cpu >= OffloadBusyPct {
		return true, fmt.Sprintf("%.0f%% of its CPUs busy", cpu)
	}
	if mem := m.MemUsedPct(); mem >= OffloadBusyPct {
		return true, fmt.Sprintf("%.0f%% of its memory used", mem)
	}
	if m.FreeMemGB > 0 && m.FreeMemGB < OffloadMinFreeMemGB {
		return true, fmt.Sprintf("%.1f GB of memory available", m.FreeMemGB)
	}
	return false, ""
}

// ChooseOffload picks the machine a check should run on instead of self, or
// "" to run it here. It moves only when self is loaded and a peer is clearly
// better off: alive, staying, on the same platform (a check's command and
// tools are the machine's), reporting its readings, idle, and far enough
// below self to pay for the checkout on the other side. Among several, the
// least loaded wins; ties go to the name, so every machine decides the same.
func ChooseOffload(self Machine, peers []*Machine, now time.Time, ttl time.Duration) (target, why string) {
	loaded, reason := self.Loaded()
	if !loaded {
		return "", ""
	}
	selfLoad, _ := self.load()
	type cand struct {
		name string
		load float64
	}
	var cands []cand
	for _, p := range peers {
		if p == nil || p.Name == "" || p.Name == self.Name || p.Left || p.Leaving {
			continue
		}
		if p.Health(now, ttl) != "green" {
			continue
		}
		if self.Platform != "" && p.Platform != self.Platform {
			continue
		}
		l, ok := p.load()
		if !ok {
			continue
		}
		if p.CPUUsedPct() > OffloadIdleCPUPct || p.MemUsedPct() > OffloadIdleMemPct || (p.FreeMemGB > 0 && p.FreeMemGB < OffloadMinFreeMemGB*2) {
			continue
		}
		if p.Slots > 0 && p.Busy >= p.Slots {
			continue // every slot there is taken by agent sessions
		}
		// A machine starved of memory gains from any idle peer; otherwise the gap must be real.
		if selfLoad-l < OffloadMinGain && !(self.FreeMemGB > 0 && self.FreeMemGB < OffloadMinFreeMemGB) {
			continue
		}
		cands = append(cands, cand{p.Name, l})
	}
	if len(cands) == 0 {
		return "", ""
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].load != cands[j].load {
			return cands[i].load < cands[j].load
		}
		return cands[i].name < cands[j].name
	})
	return cands[0].name, fmt.Sprintf("%s has %s; %s is at %.0f%%", self.Name, reason, cands[0].name, cands[0].load)
}
