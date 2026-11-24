package domain

import (
	"math"
	"testing"
	"time"
)

func TestAccountOutlook(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	near := func(got, want float64) bool { return math.Abs(got-want) < 1 }

	// Two hours into a five-hour window with 60% used: 40% at the pace mark,
	// ahead of it, so it runs out (40% left at 30%/h = 1h20) before the reset in 3h.
	a := &Account{Status: AccountOK, Usage: Usage{FiveHourPct: 60, FiveHourResets: now.Add(3 * time.Hour), WeeklyPct: 10, WeeklyResets: now.Add(6 * 24 * time.Hour)}}
	o := a.OutlookAt(now)
	if o.Limited {
		t.Fatal("an account at 60% is not limited")
	}
	if !near(o.FiveHour.PacePct, 40) || !near(o.FiveHour.ResetsIn, 3*3600) || !near(o.FiveHour.RunsOutIn, 80*60) {
		t.Fatalf("five-hour window %+v", o.FiveHour)
	}
	// One day into the week with 10% used: behind the pace, it lasts.
	if o.Weekly.RunsOutIn != 0 || !near(o.Weekly.PacePct, 100.0/7) {
		t.Fatalf("weekly window %+v", o.Weekly)
	}
	if o.WeeklyTop != nil {
		t.Fatal("no top-model window was reported")
	}

	// A full window: limited until it resets, and nothing left to run out.
	a.Usage.FiveHourPct = 100
	if o := a.OutlookAt(now); !o.Limited || o.FiveHour.RunsOutIn != 0 {
		t.Fatalf("a full five-hour window: %+v", o)
	}
	// A reading older than the reset: the window starts over, the account works.
	c := &Account{Status: AccountLimited, Usage: Usage{FiveHourPct: 100, FiveHourResets: now.Add(-time.Hour)}}
	if o := c.OutlookAt(now); o.FiveHour.UsedPct != 0 || o.Limited {
		t.Fatalf("a window that reset: %+v", o)
	}
	// An unknown reset: no pace, no projection.
	b := &Account{Status: AccountOK, Usage: Usage{FiveHourPct: 30, WeeklyTopPct: 20}}
	if o := b.OutlookAt(now); o.FiveHour.PacePct != 0 || o.FiveHour.RunsOutIn != 0 || o.WeeklyTop == nil {
		t.Fatalf("unknown reset: %+v", o)
	}
}
