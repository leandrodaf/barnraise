package app

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// Machine readings are live state: the event log never stores them. What a
// chart needs is the recent past, so each daemon keeps a short window of
// samples per machine in memory: a ring, a few kilobytes per machine, gone
// on restart and rebuilt within minutes.

const (
	// sampleEvery is the least time between two samples of one machine
	// (a machine sends one heartbeat per project; one sample is enough).
	sampleEvery = 10 * time.Second
	// samplesKept bounds the window: 720 samples are 6 hours at the usual
	// 30-second heartbeat.
	samplesKept = 720
)

// MachineSample is one reading of a machine, as percentages ready to plot.
type MachineSample struct {
	At       time.Time `json:"at"`
	CPUPct   float64   `json:"cpu_pct"`
	MemPct   float64   `json:"mem_pct"`
	TempC    float64   `json:"temp_c"`
	GPUPct   float64   `json:"gpu_pct"`    // the busiest GPU
	GPUTempC float64   `json:"gpu_temp_c"` // the hottest GPU
	Busy     int       `json:"busy"`
	Slots    int       `json:"slots"`
}

type telemetry struct {
	mu   sync.Mutex
	ring map[string][]MachineSample // machine -> samples, oldest first
}

func (t *telemetry) add(m *domain.Machine, now time.Time) {
	if m == nil || m.Name == "" {
		return
	}
	s := MachineSample{At: now.UTC(), CPUPct: round1(m.CPUUsedPct()), MemPct: round1(m.MemUsedPct()), TempC: m.TempC, Busy: m.Busy, Slots: m.Slots}
	for _, g := range m.GPUs {
		s.GPUPct = max(s.GPUPct, g.UtilPct)
		s.GPUTempC = max(s.GPUTempC, g.TempC)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ring == nil {
		t.ring = map[string][]MachineSample{}
	}
	r := t.ring[m.Name]
	if n := len(r); n > 0 && now.Sub(r[n-1].At) < sampleEvery {
		return
	}
	r = append(r, s)
	if len(r) > samplesKept {
		r = append(r[:0:0], r[len(r)-samplesKept:]...)
	}
	t.ring[m.Name] = r
}

func (t *telemetry) get(machine string, since time.Time) map[string][]MachineSample {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := map[string][]MachineSample{}
	for name, r := range t.ring {
		if machine != "" && name != machine {
			continue
		}
		i := sort.Search(len(r), func(i int) bool { return !r[i].At.Before(since) })
		out[name] = append([]MachineSample(nil), r[i:]...)
	}
	return out
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }

// MachineHistory returns the recent readings of one machine (or of every
// machine when machine is empty) over the last window (0 = everything kept).
func (s *Service) MachineHistory(_ context.Context, project string, a Actor, machine string, window time.Duration) (map[string][]MachineSample, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeItemsRead, p.Name); err != nil {
		return nil, err
	}
	var since time.Time
	if window > 0 {
		since = s.d.Clock.Now().Add(-window)
	}
	out := s.telemetry.get(machine, since)
	if machine != "" && len(out) == 0 {
		return nil, fmt.Errorf("%w: no readings of machine %s yet", ErrNotFound, machine)
	}
	return out, nil
}
