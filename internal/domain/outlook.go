package domain

import "time"

// UsageWindow is one usage window of an account seen from now: how much is
// used, how much would be used if use were even over the window (the pace),
// when it resets and, when use runs ahead of the pace, when it runs out.
type UsageWindow struct {
	UsedPct float64 `json:"used_pct"`
	// PacePct is where even use would be now (0 when the reset is unknown).
	PacePct float64 `json:"pace_pct,omitempty"`
	// ResetsIn is seconds to the reset; 0 when unknown or past.
	ResetsIn float64 `json:"resets_in,omitempty"`
	// RunsOutIn is seconds until 100% at the current rate, only when that
	// comes before the reset; 0 otherwise.
	RunsOutIn float64 `json:"runs_out_in,omitempty"`
}

// AccountOutlook is what the owner needs to know about an account now.
type AccountOutlook struct {
	// Limited: the account cannot take a step now (Available is false).
	Limited   bool         `json:"limited,omitempty"`
	FiveHour  UsageWindow  `json:"five_hour"`
	Weekly    UsageWindow  `json:"weekly"`
	WeeklyTop *UsageWindow `json:"weekly_top,omitempty"`
}

// Windows of the providers' usage limits.
const (
	FiveHourWindow = 5 * time.Hour
	WeeklyWindow   = 7 * 24 * time.Hour
)

// OutlookAt reads the account's usage against its windows at now.
func (a *Account) OutlookAt(now time.Time) AccountOutlook {
	u := a.Usage
	o := AccountOutlook{
		Limited:  !a.Available(now),
		FiveHour: usageWindow(u.FiveHourPct, u.FiveHourResets, FiveHourWindow, now),
		Weekly:   usageWindow(u.WeeklyPct, u.WeeklyResets, WeeklyWindow, now),
	}
	if u.WeeklyTopPct > 0 {
		w := usageWindow(u.WeeklyTopPct, u.WeeklyResets, WeeklyWindow, now)
		o.WeeklyTop = &w
	}
	return o
}

func usageWindow(used float64, resets time.Time, window time.Duration, now time.Time) UsageWindow {
	w := UsageWindow{UsedPct: min(max(used, 0), 100)}
	if resets.IsZero() {
		return w
	}
	if !resets.After(now) {
		// The window reset after the reading: what was used is gone.
		w.UsedPct = 0
		return w
	}
	left := resets.Sub(now)
	if left > window {
		left = window
	}
	elapsed := window - left
	w.ResetsIn = left.Seconds()
	w.PacePct = 100 * elapsed.Seconds() / window.Seconds()
	if w.UsedPct <= 0 || w.UsedPct >= 100 || elapsed <= 0 {
		return w
	}
	// At the rate so far, the rest of the window lasts this long.
	if out := (100 - w.UsedPct) / w.UsedPct * elapsed.Seconds(); out < left.Seconds() {
		w.RunsOutIn = out
	}
	return w
}
