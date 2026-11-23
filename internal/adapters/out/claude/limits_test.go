package claude

import (
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// The line a real headless session printed (Claude Code 2.1.330).
const rateLimitLine = `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1795463400,"rateLimitType":"five_hour","overageStatus":"rejected","isUsingOverage":false,"unifiedWindows":{"five_hour":{"utilization":0.42,"resetsAt":1795463400,"status":"allowed"},"seven_day":{"utilization":0.17,"resetsAt":1795561200,"status":"allowed"},"seven_day_opus":{"utilization":0.6,"resetsAt":1795561200,"status":"allowed"}}},"session_id":"s1","uuid":"u1"}`

func TestRateLimitEventUpdatesTheAccountsWindows(t *testing.T) {
	p := NewParser(domain.TokenUsage{})
	evs := p.Parse([]byte(rateLimitLine))
	if len(evs) != 1 || evs[0].Kind != ports.PEUsage || evs[0].Limits == nil {
		t.Fatalf("events %+v", evs)
	}
	u := evs[0].Limits
	if u.FiveHourPct != 42 || u.WeeklyPct != 17 || u.WeeklyTopPct != 60 || u.Source != "session" {
		t.Fatalf("usage %+v", u)
	}
	if u.FiveHourResets.Unix() != 1795463400 || u.WeeklyResets.Unix() != 1795561200 {
		t.Fatalf("resets %v %v", u.FiveHourResets, u.WeeklyResets)
	}
	// The older shape: one window per event, no unifiedWindows.
	old := p.Parse([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","resetsAt":1795561200,"rateLimitType":"seven_day","utilization":0.91}}`))
	if len(old) != 1 || old[0].Limits == nil || old[0].Limits.WeeklyPct != 91 || old[0].Limits.FiveHourPct != 0 {
		t.Fatalf("old shape %+v", old)
	}
	// An event without numbers says nothing about the account.
	none := p.Parse([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour"}}`))
	if len(none) != 1 || none[0].Limits != nil {
		t.Fatalf("no numbers %+v", none)
	}
	// A rejected window still ends the session as a limit.
	rej := p.Parse([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1795463400,"rateLimitType":"five_hour"}}`))
	if len(rej) != 1 || rej[0].Kind != ports.PELimit {
		t.Fatalf("rejected %+v", rej)
	}
}
