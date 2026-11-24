package domain

import (
	"testing"
	"time"
)

func TestLedgerAddsUpTheLog(t *testing.T) {
	st := NewState("demo")
	at := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	use := func(id, machine string, u TokenUsage, when time.Time) Event {
		return NewEvent(id, EvAgentUsage, "demo", u.ItemID, machine, u.Agent, when, Payload{Usage: &u})
	}
	a := use("e1", "m1", TokenUsage{Agent: "impl-m1-1", Account: "acc", Provider: "fake", Model: "big", Role: "implementer", ItemID: "I-1", Step: "implement",
		Input: 100, Output: 50, CacheRead: 300, CacheWrite: 10, CostUSD: 0.5, Duration: time.Minute, CPUSeconds: 12, PeakRSSBytes: 900}, at)
	b := use("e2", "m2", TokenUsage{Agent: "rev-m2-1", Account: "acc2", Provider: "fake", Model: "small", Role: "reviewer", ItemID: "I-1", Step: "review",
		Input: 10, Output: 5, Duration: 30 * time.Second, CPUSeconds: 3, PeakRSSBytes: 400}, at.Add(26*time.Hour))
	triage := use("e3", "m1", TokenUsage{Agent: "triage", Provider: "fake", Model: "small", Role: "triage", Input: 7}, at)
	for _, e := range []Event{a, b, triage, a, b} { // a repeated event counts once
		if err := st.Apply(e); err != nil {
			t.Fatal(err)
		}
	}
	l := st.Ledger
	if l.Total.Tokens != 460+15+7 || l.Total.Sessions != 3 || l.Total.CostUSD != 0.5 || l.Total.Seconds != 90 {
		t.Fatalf("total %+v", l.Total)
	}
	if l.Total.CPUSeconds != 15 || l.Total.PeakRSSBytes != 900 {
		t.Fatalf("cpu is added and the memory peak is the highest: %+v", l.Total)
	}
	it := l.ByItem["I-1"]
	if it == nil || it.Usage.Tokens != 475 || it.Usage.Sessions != 2 {
		t.Fatalf("item %+v", it)
	}
	if it.ByStep["implement"].Tokens != 460 || it.ByStep["review"].Tokens != 15 || it.ByMachine["m2"].Tokens != 15 || it.ByModel["fake/big"].Input != 100 || it.ByRole["reviewer"].Output != 5 {
		t.Fatalf("item split %+v", it)
	}
	if it.Last == nil || it.Last.Type != EvAgentUsage || it.Last.Machine != "m2" || !it.Usage.LastAt.Equal(at.Add(26*time.Hour)) {
		t.Fatalf("last activity %+v", it.Last)
	}
	if l.ByMachine["m1"].Tokens != 467 || l.ByAccount["acc2"].Tokens != 15 || l.ByAccount["-"].Tokens != 7 || l.ByAgent["triage"].Sessions != 1 {
		t.Fatalf("splits %+v %+v", l.ByMachine, l.ByAccount)
	}
	if l.ByDay["2026-03-01"].Tokens != 467 || l.ByDay["2026-03-02"].Tokens != 15 {
		t.Fatalf("by day %+v", l.ByDay)
	}
	if len(l.ByItem) != 1 {
		t.Fatalf("usage without an item belongs to no item: %v", l.ByItem)
	}
	// A copy does not follow the original.
	c := l.Clone()
	_ = st.Apply(use("e4", "m1", TokenUsage{ItemID: "I-1", Step: "implement", Input: 1}, at))
	if c.Total.Tokens != 482 || c.ByItem["I-1"].ByStep["implement"].Tokens != 460 || l.ByItem["I-1"].ByStep["implement"].Tokens != 461 {
		t.Fatalf("clone shares memory with the ledger")
	}
}

func TestLedgerChecksReviewsDeliveries(t *testing.T) {
	st := NewState("demo")
	at := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	ev := func(id, typ string, p Payload, when time.Time) {
		t.Helper()
		if err := st.Apply(NewEvent(id, typ, "demo", "I-2", "m1", "x", when, p)); err != nil {
			t.Fatal(err)
		}
	}
	ev("c1", EvCheckCompleted, Payload{Check: &CheckResult{ItemID: "I-2", Check: "unit", Passed: false, Commit: "aaa", Duration: 2 * time.Second, RunsOn: "m1", At: at}}, at)
	ev("c2", EvCheckCompleted, Payload{Check: &CheckResult{ItemID: "I-2", Check: "unit", Passed: true, Flaky: true, Commit: "bbb", RunsOn: "m2"}}, at.Add(time.Minute))
	ev("r1", EvReviewSubmitted, Payload{Review: &Review{ItemID: "I-2", Step: "review", Reviewer: "rev", Decision: ReviewRework, Criteria: "pass", Code: "fail", Findings: []Finding{{}, {}}}}, at.Add(2*time.Minute))
	ev("d1", EvDeliverySubmitted, Payload{Delivery: &Delivery{ItemID: "I-2", Step: "implement", Agent: "impl", Status: DeliveryDone, Commit: "ccc"}}, at.Add(3*time.Minute))
	il := st.Ledger.ByItem["I-2"]
	if c := il.Checks["unit"]; c == nil || !c.Passed || !c.Flaky || c.Runs != 2 || c.Commit != "bbb" || c.RunsOn != "m2" || !c.At.Equal(at.Add(time.Minute)) {
		t.Fatalf("the last result of the check, with how many times it ran: %+v", il.Checks["unit"])
	}
	if il.Reviews != 1 || il.LastReview.Decision != string(ReviewRework) || il.LastReview.Findings != 2 || il.LastReview.Code != "fail" {
		t.Fatalf("review %+v", il.LastReview)
	}
	if il.Deliveries != 1 || il.LastDelivery.Status != string(DeliveryDone) || il.LastDelivery.Commit != "ccc" {
		t.Fatalf("delivery %+v", il.LastDelivery)
	}
	if il.Last.Type != EvDeliverySubmitted {
		t.Fatalf("last %+v", il.Last)
	}
	if il.Usage.Sessions != 0 {
		t.Fatalf("no session ran: %+v", il.Usage)
	}
}

func TestLiveStateOfMachinesIsNotHistory(t *testing.T) {
	st := NewState("demo")
	at := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	session := func(id, state string, when time.Time, tokens int64) {
		t.Helper()
		e := NewEvent(id, EvAgentSession, "demo", "I-3", "m7", "impl-m7-1", when, Payload{Session: &SessionInfo{Agent: "impl-m7-1", ItemID: "I-3", Step: "implement", State: state, At: when, Tokens: tokens}})
		if !e.LiveOnly() {
			t.Fatal("a session's progress is live state")
		}
		if err := st.Apply(e); err != nil {
			t.Fatal(err)
		}
	}
	session("s1", "starting", at, 0)
	session("s2", "working", at.Add(time.Minute), 1200)
	s := st.Sessions["impl-m7-1"]
	if s.Machine != "m7" || !s.Since.Equal(at) || s.Tokens != 1200 || !s.Live() {
		t.Fatalf("the session keeps its machine and when it started: %+v", s)
	}
	session("s3", "done", at.Add(2*time.Minute), 1500)
	session("s4", "starting", at.Add(time.Hour), 0)
	if s := st.Sessions["impl-m7-1"]; !s.Since.Equal(at.Add(time.Hour)) {
		t.Fatalf("a new run starts its own clock: %+v", s)
	}

	report := func(id string, when time.Time, n int) Event {
		rep := &WorktreeReport{Machine: "m7", Project: "demo", At: when}
		for i := range n {
			rep.Worktrees = append(rep.Worktrees, WorktreeInfo{Machine: "m7", Name: FormatID(KindItem, i+1)})
		}
		return NewEvent(id, EvMachineWorktrees, "demo", "m7", "m7", "barnraise", when, Payload{Worktrees: rep})
	}
	newer, older := report("w2", at.Add(time.Minute), 2), report("w1", at, 5)
	if !newer.LiveOnly() {
		t.Fatal("a machine's worktrees are live state")
	}
	_ = st.Apply(newer)
	_ = st.Apply(older) // arrives late: the newer word stays
	if got := st.Worktrees["m7"]; got == nil || len(got.Worktrees) != 2 {
		t.Fatalf("the latest report of the machine wins: %+v", got)
	}
	if len(st.Ledger.ByItem) != 0 {
		t.Fatalf("live state leaves no trace in the ledger: %v", st.Ledger.ByItem)
	}
	c := CloneWorktrees(st.Worktrees)
	delete(c, "m7")
	if st.Worktrees["m7"] == nil {
		t.Fatal("the copy is its own map")
	}
}
