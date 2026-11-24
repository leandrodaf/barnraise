package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// The overview travels through JSON into the proto message: a field the
// proto does not have would be dropped without a word. Here every field is
// filled and the strict reader must accept all of them.
func TestOverviewFitsTheProto(t *testing.T) {
	at := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	usage := domain.UsageTotals{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4, Tokens: 10, CostUSD: 0.5, Seconds: 60, CPUSeconds: 7, PeakRSSBytes: 9, Sessions: 2, LastAt: at}
	by := domain.UsageBy{"k": &usage}
	procs := &domain.ProcSummary{Count: 1, CPUPct: 50, RSSBytes: 100, ByKind: map[string]int{"test": 1}, CPUSeconds: 3, PeakRSSBytes: 200,
		Top: []domain.ProcInfo{{PID: 7, Name: "node", Command: "node jest", Kind: "test", CPUPct: 50, RSSBytes: 100, CPUSeconds: 3, StartedAt: at}}}
	wt := domain.WorktreeInfo{Machine: "m2", Project: "demo", Name: "I-1", Kind: domain.WorktreeItem, ItemID: "I-1", Path: "/d/I-1", Branch: "b", Head: "abc",
		InUse: true, SetupDone: true, ModifiedAt: at, SizeBytes: 5, Files: 2, SizedAt: at, Procs: procs}
	sess := &domain.SessionInfo{Agent: "a", Role: "implementer", Account: "acc", Provider: "fake", Model: "big", Effort: "high", ItemID: "I-1", Step: "implement",
		SessionID: "s", State: "working", Detail: "Edit", At: at, Machine: "m2", Since: at, Tokens: 42, CostUSD: 0.1}
	item := &app.ItemOverview{ID: "I-1", Title: "t", Kind: "feature", Priority: 1, Lane: "goal", Area: "cards", GoalID: "G-1", Size: "2h-6h", Class: "normal",
		Status: "in_progress", Step: "implement", Activity: app.ActWorking, Detail: "d", Machine: "m2", Claim: &domain.Claim{Machine: "m2", Agent: "a", Since: at},
		Branch: "b", PR: &app.PRView{Number: 12, URL: "u", State: "draft"}, TrackerRef: "gh#1", Adopted: true, Paused: true, Parked: true, Risky: true, Tier: 1,
		Attempts: 2, ReworkRounds: 3, Deps: []string{"I-2"}, BlockedBy: []string{"I-2"}, WaitingQuestion: "Q-1", CriteriaMet: 1, CriteriaTotal: 2,
		CreatedAt: at, UpdatedAt: at, StepStartedAt: at, IntegratedAt: at, MergeCommit: "m", ClosedReason: "r", ClosedBy: "x", StepSeconds: 1, QuietSeconds: 2, LeadSeconds: 3,
		Sessions: []*domain.SessionInfo{sess}, Worktrees: []domain.WorktreeInfo{wt}, Procs: procs, Usage: usage, UsageByStep: by, UsageByModel: by, UsageByMachine: by, UsageByRole: by,
		Runs:   map[string]domain.StepRun{"implement": {Agent: "a", Account: "acc", Provider: "fake", Model: "big", Family: "fake"}},
		Checks: []*domain.CheckMark{{Check: "unit", Passed: true, Flaky: true, MainRed: true, Commit: "c", RunsOn: "m2", Seconds: 2, Runs: 3, At: at}}, Reviews: 1,
		LastReview: &domain.ReviewMark{Step: "review", Reviewer: "r", Decision: "APPROVE", Criteria: "pass", Code: "pass", Findings: 1, At: at}, Deliveries: 1,
		LastDelivery: &domain.DeliveryMark{Step: "implement", Agent: "a", Status: "DONE", Commit: "c", At: at},
		LastActivity: &domain.Activity{Type: "item.updated", Machine: "m2", Actor: "a", At: at}}
	o := &app.Overview{GeneratedAt: at, Machine: "m1", Activity: map[string]int{"working": 1}, Usage: usage,
		Projects: []*app.ProjectOverview{{Project: "demo", Paused: true, PausedWhy: "w", Goal: &domain.Goal{ID: "G-1", Name: "g", Active: true, CreatedAt: at},
			Progress: &domain.GoalProgress{Total: 2, Done: 1}, Focus: &domain.Focus{Name: "f", Share: 50, SetAt: at}, Status: map[string]int{"in_progress": 1},
			Activity: map[string]int{"working": 1}, OpenQuestions: 1, OpenProposals: 2, Items: []*app.ItemOverview{item}, ItemsMatched: 1, ItemsTotal: 9, Truncated: true,
			Usage: usage, UsageByModel: by, UsageByRole: by, UsageByAccount: by, UsageByMachine: by, UsageByAgent: by, UsageByDay: by,
			Accounts: []*domain.Account{{ID: "acc", Provider: "fake", Machine: "m2"}}}},
		Machines: []*app.MachineOverview{{Machine: app.MachineView{Machine: domain.Machine{Name: "m2", CPUs: 8, LastSeen: at}, Health: "green"}, Projects: []string{"demo"},
			Sessions: []*domain.SessionInfo{sess}, Items: []string{"demo/I-1"}, Worktrees: []domain.WorktreeInfo{wt}, WorktreeBytes: 5, DiskFreeBytes: 6, DiskTotalBytes: 7,
			Procs: procs, ProcsSupported: true, ReportedAt: at, Usage: usage}}}

	data, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	var strict pb.OverviewResponse
	if err := (protojson.UnmarshalOptions{}).Unmarshal(data, &strict); err != nil {
		t.Fatalf("a field of the overview is missing in the proto: %v", err)
	}
	var out pb.OverviewResponse
	if err := ToPB(o, &out); err != nil {
		t.Fatal(err)
	}
	it := out.Projects[0].Items[0]
	if it.Pr.Number != 12 || it.Sessions[0].Machine != "m2" || it.Sessions[0].Tokens != 42 || it.Worktrees[0].Procs.Top[0].Command != "node jest" ||
		it.Usage.CpuSeconds != 7 || it.UsageByStep["k"].Tokens != 10 || it.Checks[0].Runs != 3 || it.LastReview.CriteriaVerdict != "pass" || it.Runs["implement"].Model != "big" {
		t.Fatalf("item lost on the way: %v", it)
	}
	if m := out.Machines[0]; m.Machine.Name != "m2" || m.Machine.Health != "green" || m.Procs.ByKind["test"] != 1 || m.Usage.PeakRssBytes != 9 || m.ReportedAt == "" {
		t.Fatalf("machine lost on the way: %v", m)
	}
	// What the API and the CLI print has the same names the daemon uses.
	back, err := marshalPB.Marshal(&out)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{`"usage_by_step"`, `"cpu_seconds"`, `"peak_rss_bytes"`, `"procs_supported"`, `"items_matched"`, `"tracker_ref"`, `"worktree_bytes"`} {
		if !strings.Contains(string(back), name) {
			t.Errorf("%s is missing in the JSON", name)
		}
	}
}
