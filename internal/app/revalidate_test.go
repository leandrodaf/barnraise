package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

func anchored(e *env, title, file string) *domain.Item {
	return e.create(NewItem{Title: title, Criteria: crit(ears), Anchors: domain.Anchors{Files: []string{file}}})
}

func TestRevalidateVerdicts(t *testing.T) {
	g := &testkit.FakeGit{Main: "c1"}
	e := newEnv(t, withFakeGit(g), withTriage())
	still := anchored(e, "still", "a.go")
	upd := anchored(e, "update", "b.go")
	done := anchored(e, "done", "c.go")
	obs := anchored(e, "obsolete", "d.go")
	dup := anchored(e, "duplicate", "e.go")
	target := e.create(NewItem{Title: "target", Criteria: []domain.Criterion{{Text: "WHEN x, THE SYSTEM SHALL y", Check: "main"}}})
	dep := anchored(e, "new dep", "f.go")
	conf := anchored(e, "conflict", "g.go")
	bad := anchored(e, "bad verdict", "h.go")
	running := anchored(e, "running", "i.go")
	e.start(running.ID)
	newCrit := []domain.Criterion{{Text: "WHEN the user pays, THE SYSTEM SHALL charge once", Check: "main"}}
	for _, v := range []domain.TriageResult{
		{ItemID: still.ID, Verdict: domain.TriageStillValid, Reason: "unrelated"},
		{ItemID: upd.ID, Verdict: domain.TriageNeedsUpdate, Reason: "API renamed", Criteria: newCrit, Anchors: &domain.Anchors{Files: []string{"b2.go"}}, ScopeChange: true},
		{ItemID: done.ID, Verdict: domain.TriageMaybeDone, Reason: "looks done"},
		{ItemID: obs.ID, Verdict: domain.TriageObsolete, Reason: "screen removed"},
		{ItemID: dup.ID, Verdict: domain.TriageDuplicate, Target: target.ID, Reason: "same thing"},
		{ItemID: dep.ID, Verdict: domain.TriageNewDependency, Target: target.ID, Reason: "needs the target"},
		{ItemID: conf.ID, Verdict: domain.TriageConflict, Reason: "contradicts D-3"},
		{ItemID: bad.ID, Verdict: domain.TriageObsolete},
	} {
		e.triage.Set(v)
	}
	e.triage.Usage = domain.TokenUsage{Input: 100, Output: 20}
	ch := domain.Change{Kind: "integration", Cause: "main c2", Commit: "c2", Files: []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go", "g.go", "h.go", "i.go"}, Summary: "big change"}
	res, err := e.svc.Revalidate(e.ctx, "demo", owner(), ch)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Candidates) != 9 || len(res.Verdicts) != 7 {
		t.Fatalf("candidates %d verdicts %d (the bad one is refused)", len(res.Candidates), len(res.Verdicts))
	}
	if it := e.item(still.ID); it.Anchors.Base != "c2" || !strings.Contains(e.types(still.ID), domain.EvItemRevalidated) {
		t.Fatalf("still valid %+v", it.Anchors)
	}
	u := e.item(upd.ID)
	if u.Status != domain.StatusDraft || u.Criteria[0].ID != "C1" || u.Criteria[0].Text != newCrit[0].Text || u.Anchors.Files[0] != "b2.go" || u.Anchors.Base != "c2" ||
		!strings.Contains(u.Body, "Updated by main c2: API renamed") || len(u.Clarifications) != 1 {
		t.Fatalf("needs update %+v", u)
	}
	if it := e.item(done.ID); it.Status != domain.StatusClosed || it.ClosedReason != "done_by" || it.ClosedBy != "main c2" {
		t.Fatalf("maybe done %+v", it)
	}
	if !g.Called("AddDetachedWorktree") || !g.Called("RemoveWorktree") {
		t.Fatalf("maybe done checks main in a worktree: %v", g.Calls())
	}
	if it := e.item(obs.ID); it.ClosedReason != "obsolete" {
		t.Fatal(it.Status)
	}
	if it := e.item(dup.ID); it.ClosedReason != "duplicate" || it.ClosedBy != target.ID {
		t.Fatalf("duplicate %+v", it)
	}
	tg := e.item(target.ID)
	if len(tg.Criteria) != 2 || tg.Criteria[1].ID != "C2" || !strings.Contains(tg.Body, "Merged from "+dup.ID) {
		t.Fatalf("target gets the duplicate's criteria %+v", tg)
	}
	if it := e.item(dep.ID); it.Status != domain.StatusBlocked || it.Deps[0] != target.ID {
		t.Fatalf("new dependency %+v", it)
	}
	if it := e.item(conf.ID); it.Status != domain.StatusWaitingOwner || it.WaitingQuestion == "" {
		t.Fatalf("conflict %+v", it)
	}
	if it := e.item(bad.ID); it.Status != domain.StatusReady {
		t.Fatal("a refused verdict changes nothing")
	}
	inbox, _ := e.svc.Inbox(e.ctx, "demo", "x", "", running.ID, false)
	if len(inbox) != 1 || !strings.Contains(inbox[0].Text, "Main changed under your anchors (main c2)") {
		t.Fatalf("the running item hears about it: %+v", inbox)
	}
	m, _ := e.svc.Metrics(e.ctx, "demo", owner(), time.Hour)
	if m.TokensByRole["triage"] != 120 {
		t.Fatalf("triage usage %v", m.TokensByRole)
	}
	// Nothing anchored to the change: no triage at all.
	calls := len(e.triage.Calls())
	if res, err := e.svc.Revalidate(e.ctx, "demo", owner(), domain.Change{Kind: "integration", Files: []string{"zzz.go"}}); err != nil || len(res.Candidates) != 0 || len(e.triage.Calls()) != calls {
		t.Fatal(res, err)
	}
	_, err = e.svc.Revalidate(e.ctx, "demo", Actor{}, ch)
	wantErr(t, err, ErrForbidden)
	_, err = e.svc.Revalidate(e.ctx, "nope", owner(), ch)
	wantErr(t, err, ErrUnknownProj)
	e.triage.Err = errors.New("model down")
	if _, err := e.svc.Revalidate(e.ctx, "demo", owner(), domain.Change{Kind: "integration", Files: []string{"a.go"}}); err == nil {
		t.Fatal("triage errors are returned")
	}
}

func TestRevalidateEdgeCases(t *testing.T) {
	g := &testkit.FakeGit{Main: "c1"}
	e := newEnv(t, withFakeGit(g), withTriage(), withCfg(func(c *domain.ProjectConfig) {
		c.Budget.TriageWeekPct = 0.05 // two items per pass
		c.Checks["e2e"] = domain.CheckDef{Name: "e2e", RunsOn: "github-actions", Workflow: "e2e.yml"}
	}))
	var items []*domain.Item
	for range 3 {
		items = append(items, anchored(e, "budget", "a.go"))
	}
	res, err := e.svc.Revalidate(e.ctx, "demo", owner(), domain.Change{Kind: "integration", Files: []string{"a.go"}})
	if err != nil || len(res.Verdicts) != 2 || len(res.Deferred) != 1 || res.Deferred[0] != items[2].ID {
		t.Fatalf("budget %+v %v", res, err)
	}
	// maybe_done cannot be proven with a remote check, nor when a check fails.
	remote := e.create(NewItem{Title: "remote", Criteria: []domain.Criterion{{Text: ears, Check: "e2e"}}, Anchors: domain.Anchors{Files: []string{"r.go"}}})
	failing := anchored(e, "failing", "f.go")
	e.checks.ScriptMain("main", testkit.Fail)
	e.triage.Set(domain.TriageResult{ItemID: remote.ID, Verdict: domain.TriageMaybeDone, Reason: "?"})
	e.triage.Set(domain.TriageResult{ItemID: failing.ID, Verdict: domain.TriageMaybeDone, Reason: "?"})
	if _, err := e.svc.Revalidate(e.ctx, "demo", owner(), domain.Change{Kind: "integration", Files: []string{"r.go", "f.go"}}); err != nil {
		t.Fatal(err)
	}
	e.wantStatus(remote.ID, domain.StatusReady, "")
	e.wantStatus(failing.ID, domain.StatusReady, "")
	// Duplicate of itself or of nothing, dependency on itself: ignored.
	self := anchored(e, "self", "s.go")
	e.triage.Set(domain.TriageResult{ItemID: self.ID, Verdict: domain.TriageDuplicate, Target: self.ID, Reason: "x"})
	if _, err := e.svc.Revalidate(e.ctx, "demo", owner(), domain.Change{Kind: "integration", Files: []string{"s.go"}}); err != nil {
		t.Fatal(err)
	}
	e.wantStatus(self.ID, domain.StatusReady, "")
	e.svc.mergeInto(e.ctx, e.p, self.ID, "nonsense", "x", "y")
	e.svc.mergeInto(e.ctx, e.p, "I-999", "I-1", "x", "y")
	e.triage.Set(domain.TriageResult{ItemID: self.ID, Verdict: domain.TriageNewDependency, Target: self.ID, Reason: "x"})
	if _, err := e.svc.Revalidate(e.ctx, "demo", owner(), domain.Change{Kind: "integration", Files: []string{"s.go"}}); err != nil {
		t.Fatal(err)
	}
	if it := e.item(self.ID); len(it.Deps) != 0 {
		t.Fatal("an item cannot depend on itself")
	}
	// maybe_done without git, checks or criteria does nothing.
	noGit := newEnv(t)
	x := noGit.create(NewItem{Title: "x"})
	noGit.svc.maybeDone(noGit.ctx, noGit.p, domain.TriageResult{ItemID: x.ID}, domain.Change{})
	e.svc.maybeDone(e.ctx, e.p, domain.TriageResult{ItemID: "I-999"}, domain.Change{})
	g.MainErr = errors.New("no main")
	e.svc.maybeDone(e.ctx, e.p, domain.TriageResult{ItemID: failing.ID}, domain.Change{})
	g.MainErr = nil
	g.WorktreeErr = errors.New("disk full")
	e.svc.maybeDone(e.ctx, e.p, domain.TriageResult{ItemID: failing.ID}, domain.Change{})
	e.wantStatus(failing.ID, domain.StatusReady, "")
}

func TestRevalidateWithoutTriageAndAsync(t *testing.T) {
	e := newEnv(t, withKnowledge())
	e.know.G = &testkit.Graph{InFile: map[string][]string{"lib/pay.go": {"Charge"}}, CallersOf: map[string][]string{"Charge": {"Checkout"}}}
	it := e.create(NewItem{Title: "checkout", Criteria: crit(ears), Anchors: domain.Anchors{Symbols: []string{"Checkout"}}})
	res, err := e.svc.Revalidate(e.ctx, "demo", owner(), domain.Change{Kind: "integration", Cause: "main c9", Commit: "c9", Files: []string{"lib/pay.go"}})
	if err != nil || len(res.Verdicts) != 1 || res.Verdicts[0].Verdict != domain.TriageStillValid || !strings.Contains(res.Verdicts[0].Reason, "no triage model") {
		t.Fatalf("%+v %v", res, err)
	}
	if e.item(it.ID).Anchors.Base != "c9" {
		t.Fatal("base not updated")
	}
	e.svc.revalidateAsync(e.p, domain.Change{Kind: "integration", Cause: "async", Commit: "c10", Files: []string{"lib/pay.go"}})
	eventually(t, "async revalidation", func() bool { return e.item(it.ID).Anchors.Base == "c10" })
}

func TestSweep(t *testing.T) {
	e := newEnv(t, withTriage(), withCfg(func(c *domain.ProjectConfig) { c.Budget.TriageWeekPct = 0.025 }))
	a := e.ready("feature", "old a", "")
	b := e.ready("feature", "old b", "")
	if res, err := e.svc.Sweep(e.ctx, "demo"); err != nil || len(res.Verdicts) != 0 {
		t.Fatal(res, err)
	}
	e.clock.Advance(15 * 24 * time.Hour)
	res, err := e.svc.Sweep(e.ctx, "demo")
	if err != nil || len(res.Verdicts) != 1 || res.Verdicts[0].ItemID != a.ID || len(res.Deferred) != 1 || res.Deferred[0] != b.ID {
		t.Fatalf("%+v %v", res, err)
	}
	if !e.item(a.ID).ValidatedAt.Equal(e.clock.Now()) {
		t.Fatal("swept item is validated now")
	}
	if _, err := e.svc.Sweep(e.ctx, "nope"); err == nil {
		t.Fatal("unknown project")
	}
}

func TestProviderTriage(t *testing.T) {
	e := newEnv(t)
	it := e.ready("feature", "x", "")
	pt := &ProviderTriage{Provider: e.prov, Model: "fake-small", Workdir: t.TempDir(), Account: func() (domain.Account, bool) { return domain.Account{ID: "fake-1"}, true }}
	e.prov.Handle(func(ctx context.Context, spec ports.SessionSpec, emit func(ports.ProviderEvent)) (ports.SessionResult, error) {
		if !strings.Contains(spec.Prompt, it.ID) || spec.MaxTurns != 1 {
			t.Errorf("prompt %q", spec.Prompt)
		}
		emit(ports.ProviderEvent{Kind: ports.PEMessage, Text: "thinking"})
		return ports.SessionResult{Text: "```json\n{\"results\":[{\"item_id\":\"" + it.ID + "\",\"verdict\":\"obsolete\",\"reason\":\"gone\"}]}\n```", Usage: domain.TokenUsage{Input: 5}}, nil
	})
	res, u, err := pt.Triage(e.ctx, domain.Change{Kind: "integration"}, []*domain.Item{it})
	if err != nil || len(res) != 1 || res[0].Verdict != domain.TriageObsolete || u.Input != 5 {
		t.Fatalf("%+v %+v %v", res, u, err)
	}
	e.prov.Handle(func(context.Context, ports.SessionSpec, func(ports.ProviderEvent)) (ports.SessionResult, error) {
		return ports.SessionResult{Text: "I think it is fine"}, nil
	})
	if _, _, err := pt.Triage(e.ctx, domain.Change{}, []*domain.Item{it}); err == nil {
		t.Fatal("a free-text answer is refused")
	}
	e.prov.Handle(func(context.Context, ports.SessionSpec, func(ports.ProviderEvent)) (ports.SessionResult, error) {
		return ports.SessionResult{}, errors.New("crashed")
	})
	if _, _, err := pt.Triage(e.ctx, domain.Change{}, []*domain.Item{it}); err == nil {
		t.Fatal("session error")
	}
	e.prov.FailOpen(errors.New("no binary"))
	if _, _, err := pt.Triage(e.ctx, domain.Change{}, []*domain.Item{it}); err == nil {
		t.Fatal("open error")
	}
	pt.Account = func() (domain.Account, bool) { return domain.Account{}, false }
	if _, _, err := pt.Triage(e.ctx, domain.Change{}, nil); err == nil {
		t.Fatal("no account")
	}
	if extractJSON("no json") != "no json" || extractJSON("x {\"a\":1} y") != "{\"a\":1}" {
		t.Fatal("extractJSON")
	}
}

func TestTrackerImportAndSync(t *testing.T) {
	e := newEnv(t, withTracker(), withCfg(func(c *domain.ProjectConfig) { c.Tracker.Trusted = []string{"ana", "label:accepted"} }))
	mine := e.ready("feature", "Mine", "")
	eventually(t, "item created in the tracker", func() bool { return e.item(mine.ID).TrackerRef == "mem#1" })
	if _, err := e.svc.UpdateItem(e.ctx, "demo", owner(), mine.ID, ItemPatch{Size: new("2h")}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "item updated in the tracker", func() bool { return len(e.tracker.Updated()) > 0 })
	e.tracker.Items = []ports.TrackerItem{
		{Ref: "gh#10", Title: "Dark mode", Body: "please", Author: "ana"},
		{Ref: "gh#11", Title: "Crash on start", Body: "trace", Author: "stranger", Labels: []string{"bug"}, URL: "https://x/11"},
		{Ref: "gh#12", Title: "Accepted idea", Body: "ok", Author: "stranger", Labels: []string{"accepted", "bug"}},
		{Ref: "gh#13", Title: "Closed one", Closed: true},
		{Ref: "gh#14", Title: "[I-1] mirror of ours"},
		{Ref: "mem#1", Title: "[" + mine.ID + "] Mine, renamed", URL: "https://x/1"},
	}
	n, err := e.svc.ImportFromTracker(e.ctx, "demo", time.Time{})
	if err != nil || n != 3 {
		t.Fatalf("imported %d %v", n, err)
	}
	var trusted, accepted *domain.Item
	for _, it := range e.p.Snapshot().Items {
		switch it.TrackerRef {
		case "gh#10":
			trusted = it
		case "gh#12":
			accepted = it
		}
	}
	if trusted == nil || trusted.Status != domain.StatusDraft || !strings.Contains(trusted.Body, "UNTRUSTED") || trusted.Kind != "feature" {
		t.Fatalf("trusted author %+v", trusted)
	}
	if accepted == nil || accepted.Kind != "bug" {
		t.Fatalf("trusted label %+v", accepted)
	}
	props, _, _ := e.svc.ListProposals("demo", owner(), false)
	if len(props) != 1 || props[0].Kind != "bug" || props[0].By != "tracker" {
		t.Fatalf("stranger's entry is a proposal %+v", props)
	}
	e.tracker.ListErr = errors.New("rate limited")
	if _, err := e.svc.ImportFromTracker(e.ctx, "demo", time.Time{}); err == nil {
		t.Fatal("list error")
	}
	if _, err := e.svc.ImportFromTracker(e.ctx, "nope", time.Time{}); err == nil {
		t.Fatal("unknown project")
	}
	e.tracker.CreateErr = errors.New("tracker down")
	e.svc.SyncAllToTracker("demo")
	e.svc.SyncAllToTracker("nope")

	none := newEnv(t)
	if n, err := none.svc.ImportFromTracker(none.ctx, "demo", time.Time{}); n != 0 || err != nil {
		t.Fatal(n, err)
	}
}
