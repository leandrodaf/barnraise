package app

import (
	"fmt"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func (e *env) mutate(id string, fn func(it *domain.Item)) {
	e.t.Helper()
	if _, err := e.svc.mutateItem(e.ctx, e.p, id, domain.EvItemUpdated, "test", func(it *domain.Item) error {
		fn(it)
		return nil
	}, domain.Payload{}); err != nil {
		e.t.Fatal(err)
	}
}

// live applies a live event as if machine had published it.
func (e *env) live(typ, machine string, pl domain.Payload) {
	e.t.Helper()
	ev := domain.NewEvent(e.ids.NewID(), typ, "demo", "", machine, "barnraise", e.clock.Now(), pl)
	e.p.mu.Lock()
	err := e.p.state.Apply(ev)
	e.p.mu.Unlock()
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) heartbeat(machine string) {
	e.live(domain.EvMachineUpdated, machine, domain.Payload{MachineI: &domain.Machine{Name: machine, Slots: 2, LastSeen: e.clock.Now()}})
}

func (o *Overview) item(t *testing.T, id string) *ItemOverview {
	t.Helper()
	for _, p := range o.Projects {
		for _, it := range p.Items {
			if it.ID == id {
				return it
			}
		}
	}
	t.Fatalf("item %s is not in the overview", id)
	return nil
}

func (o *Overview) machine(t *testing.T, name string) *MachineOverview {
	t.Helper()
	for _, m := range o.Machines {
		if m.Machine.Name == name {
			return m
		}
	}
	t.Fatalf("machine %s is not in the overview", name)
	return nil
}

func TestOverviewJoinsEveryMachine(t *testing.T) {
	e := newEnv(t, withHost())
	now := e.clock.Now()
	working := e.ready("simple", "working", "cards")
	checking := e.ready("simple", "checking", "cards")
	idle := e.ready("simple", "idle", "cards")
	stalled := e.ready("simple", "stalled", "cards")
	waiting := e.ready("simple", "waiting", "cards")
	blocked := e.ready("simple", "blocked", "cards")
	queued := e.ready("simple", "queued", "cards")
	ready := e.ready("simple", "ready", "cards")
	draft := e.create(NewItem{Title: "draft"})
	done := e.ready("simple", "done", "cards")
	oldDone := e.ready("simple", "done long ago", "cards")
	taken := e.ready("simple", "taken over", "cards")

	hold := func(id, machine string) {
		e.mutate(id, func(it *domain.Item) {
			it.Status, it.Step, it.StepStartedAt = domain.StatusInProgress, "implement", now.Add(-10*time.Minute)
			it.Claim = &domain.Claim{Machine: machine, Agent: "implementer-" + machine + "-1", Since: now.Add(-10 * time.Minute)}
			it.Branch = "item/" + id
		})
	}
	hold(working.ID, "m2")
	hold(checking.ID, "m2")
	hold(idle.ID, "m1")
	hold(stalled.ID, "m3")
	e.mutate(working.ID, func(it *domain.Item) { it.PR, it.TrackerRef = "#12 https://git.example/pr/12", "gh#40" })
	e.mutate(checking.ID, func(it *domain.Item) {
		it.Step, it.PR = "check", "#13"
		it.Runs = map[string]domain.StepRun{"implement": {Agent: "implementer-m2-1", Provider: "fake", Model: "big"}}
	})
	q, err := e.svc.AskOwner(e.ctx, "demo", owner(), domain.Question{ItemID: waiting.ID, Context: "c", Decision: "Which color?", Options: []domain.Option{{Label: "red", Recommended: true}, {Label: "blue"}}, IfNoAnswer: "red"})
	if err != nil {
		t.Fatal(err)
	}
	e.mutate(waiting.ID, func(it *domain.Item) { it.Status, it.WaitingQuestion = domain.StatusWaitingOwner, q.ID })
	e.mutate(blocked.ID, func(it *domain.Item) { it.Status, it.Deps = domain.StatusBlocked, []string{ready.ID, done.ID} })
	e.mutate(queued.ID, func(it *domain.Item) { it.Status, it.PR = domain.StatusQueued, "#14" })
	e.mutate(taken.ID, func(it *domain.Item) { it.Status = domain.StatusPaused })
	e.integrateFake(done, 1, "review")
	e.mutate(done.ID, func(it *domain.Item) { it.PR = "#9" })
	e.integrateFake(oldDone, 0, "")

	// What only the machines know.
	e.heartbeat("m1")
	e.heartbeat("m2")
	e.live(domain.EvMachineUpdated, "m3", domain.Payload{MachineI: &domain.Machine{Name: "m3", LastSeen: now.Add(-time.Hour)}})
	e.live(domain.EvAgentSession, "m2", domain.Payload{Session: &domain.SessionInfo{Agent: "implementer-m2-1", Role: "implementer", ItemID: working.ID, Step: "implement",
		State: "working", Detail: "Edit", At: now, Tokens: 4200, CostUSD: 0.12}})
	e.live(domain.EvAgentSession, "m3", domain.Payload{Session: &domain.SessionInfo{Agent: "implementer-m3-1", Role: "implementer", ItemID: stalled.ID, Step: "implement", State: "working", At: now.Add(-time.Hour)}})
	e.live(domain.EvAgentSession, "m1", domain.Payload{Session: &domain.SessionInfo{Agent: "implementer-m1-1", Role: "implementer", ItemID: idle.ID, Step: "implement", State: "done", At: now}})
	procs := &domain.ProcSummary{Count: 2, CPUPct: 150, RSSBytes: 3 << 30, ByKind: map[string]int{"test": 1, "browser": 1}, CPUSeconds: 40, PeakRSSBytes: 4 << 30,
		Top: []domain.ProcInfo{{PID: 7, Name: "node", Command: "node jest", Kind: "test", CPUPct: 100, RSSBytes: 1 << 30}, {PID: 8, Name: "chrome", Command: "chrome", Kind: "browser", CPUPct: 50, RSSBytes: 2 << 30}}}
	e.live(domain.EvMachineWorktrees, "m2", domain.Payload{Worktrees: &domain.WorktreeReport{Machine: "m2", Project: "demo", At: now, SizeBytes: 5 << 30,
		DiskFreeBytes: 10 << 30, DiskTotalBytes: 100 << 30, ProcsSupported: true, Worktrees: []domain.WorktreeInfo{
			{Machine: "m2", Project: "demo", Name: working.ID, Kind: domain.WorktreeItem, ItemID: working.ID, Path: "/d/" + working.ID, Branch: "item/" + working.ID, Head: "abc", InUse: true, SizeBytes: 2 << 30},
			{Machine: "m2", Project: "demo", Name: checking.ID, Kind: domain.WorktreeItem, ItemID: checking.ID, Path: "/d/" + checking.ID, SizeBytes: 3 << 30, Procs: procs},
			{Machine: "m2", Project: "demo", Name: "demo", Kind: domain.WorktreeRoot, Path: "/src/demo"},
		}}})
	e.live(domain.EvMachineWorktrees, "m1", domain.Payload{Worktrees: &domain.WorktreeReport{Machine: "m1", Project: "demo", At: now, SizeBytes: 1 << 30,
		Worktrees: []domain.WorktreeInfo{{Machine: "m1", Project: "demo", Name: working.ID, Kind: domain.WorktreeItem, ItemID: working.ID, Path: "/e/" + working.ID, SizeBytes: 1 << 30}}}})

	// What it cost, as the log says.
	use := func(machine, id, step, model string, in int64, d time.Duration) {
		u := domain.TokenUsage{Agent: "implementer-" + machine + "-1", Account: "acc-" + machine, Provider: "fake", Model: model, Role: "implementer", ItemID: id, Step: step, Input: in, CostUSD: 1, Duration: d}
		ev := domain.NewEvent(e.ids.NewID(), domain.EvAgentUsage, "demo", id, machine, u.Agent, e.clock.Now(), domain.Payload{Usage: &u})
		if machine == e.machine {
			e.usage(u)
			return
		}
		if err := e.svc.Ingest(e.ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	use("m2", working.ID, "implement", "big", 1000, time.Hour)
	use("m1", working.ID, "plan", "small", 200, time.Minute)
	use("m2", done.ID, "implement", "big", 50, time.Minute)
	if _, err := e.svc.emit(e.ctx, e.p, domain.EvCheckCompleted, checking.ID, "driver", domain.Payload{Check: &domain.CheckResult{ItemID: checking.ID, Check: "main", Passed: true, RunsOn: "m2", Duration: 3 * time.Second}}); err != nil {
		t.Fatal(err)
	}
	// The old one is finished for longer than the default window.
	e.clock.Advance(8 * 24 * time.Hour)
	later := e.clock.Now()
	for _, it := range []*domain.Item{working, checking, idle, stalled, waiting, blocked, queued, ready, draft, done, taken} {
		e.mutate(it.ID, func(*domain.Item) {})
	}
	e.heartbeat("m1")
	e.heartbeat("m2")
	e.live(domain.EvAgentSession, "m2", domain.Payload{Session: &domain.SessionInfo{Agent: "implementer-m2-1", Role: "implementer", ItemID: working.ID, Step: "implement", State: "working", At: later, Tokens: 4200}})

	o, err := e.svc.Overview(e.ctx, owner(), OverviewQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Projects) != 1 || o.Machine != "m1" || !o.GeneratedAt.Equal(later) {
		t.Fatalf("overview %+v", o)
	}
	p := o.Projects[0]
	want := map[string]string{working.ID: ActWorking, checking.ID: ActRunning, idle.ID: ActIdle, stalled.ID: ActStalled, waiting.ID: ActWaitingOwner,
		blocked.ID: ActBlocked, queued.ID: ActQueued, ready.ID: ActReady, draft.ID: ActDraft, done.ID: ActIntegrated, taken.ID: ActPaused}
	for id, act := range want {
		if got := o.item(t, id); got.Activity != act {
			t.Errorf("%s (%s) is %s, want %s (%s)", id, got.Title, got.Activity, act, got.Detail)
		}
	}
	if p.ItemsTotal != 12 || p.ItemsMatched != 11 || len(p.Items) != 11 || p.Truncated {
		t.Fatalf("an item finished 8 days ago is counted, not listed: total %d matched %d", p.ItemsTotal, p.ItemsMatched)
	}
	if p.Activity[ActIntegrated] != 2 || p.Status["in_progress"] != 4 || o.Activity[ActWorking] != 1 {
		t.Fatalf("counts cover every item: %v %v", p.Activity, p.Status)
	}
	if p.Items[0].ID != working.ID || p.Items[len(p.Items)-1].ID != done.ID {
		t.Fatalf("what runs comes first and what ended last: %s … %s", p.Items[0].ID, p.Items[len(p.Items)-1].ID)
	}

	w := o.item(t, working.ID)
	if w.Machine != "m2" || len(w.Sessions) != 1 || w.Sessions[0].Machine != "m2" || w.Sessions[0].Tokens != 4200 || !w.Sessions[0].Since.Equal(now) {
		t.Fatalf("the session of the item, with its machine and start: %+v", w.Sessions)
	}
	if len(w.Worktrees) != 2 || w.Worktrees[0].Machine != "m1" || w.Worktrees[1].Machine != "m2" || !w.Worktrees[1].InUse || w.Worktrees[1].Head != "abc" {
		t.Fatalf("the worktrees of the item on both machines: %+v", w.Worktrees)
	}
	if w.PR == nil || w.PR.Number != 12 || w.PR.URL != "https://git.example/pr/12" || w.PR.State != "draft" || w.TrackerRef != "gh#40" {
		t.Fatalf("pull request and tracker: %+v %q", w.PR, w.TrackerRef)
	}
	if w.Usage.Tokens != 1200 || w.Usage.Sessions != 2 || w.UsageByMachine["m2"].Tokens != 1000 || w.UsageByStep["plan"].Tokens != 200 || w.UsageByModel["fake/big"].Seconds != 3600 {
		t.Fatalf("usage of the item: %+v", w.Usage)
	}
	if w.StepSeconds < 8*24*3600 || w.QuietSeconds != 0 || w.LeadSeconds <= 0 || w.CriteriaTotal != 1 {
		t.Fatalf("times: step %v quiet %v lead %v", w.StepSeconds, w.QuietSeconds, w.LeadSeconds)
	}
	c := o.item(t, checking.ID)
	if c.Procs == nil || c.Procs.Count != 2 || c.Procs.CPUPct != 150 || len(c.Procs.Top) != 2 || c.Procs.Top[0].Kind != "test" || c.PR.State != "ready" {
		t.Fatalf("what runs for the item, and a pull request past its implementation: %+v %+v", c.Procs, c.PR)
	}
	if len(c.Checks) != 1 || !c.Checks[0].Passed || c.Checks[0].RunsOn != "m2" || c.Checks[0].Seconds != 3 || c.LastActivity == nil {
		t.Fatalf("checks: %+v", c.Checks)
	}
	if s := o.item(t, stalled.ID); len(s.Sessions) != 0 || s.Detail != "machine m3 is out" {
		t.Fatalf("a machine that is out has no live session: %+v %q", s.Sessions, s.Detail)
	}
	if got := o.item(t, idle.ID); len(got.Sessions) != 0 {
		t.Fatalf("a session that ended is not running: %+v", got.Sessions)
	}
	if got := o.item(t, waiting.ID); got.Detail != "Which color?" || got.WaitingQuestion != q.ID {
		t.Fatalf("waiting: %+v", got)
	}
	if got := o.item(t, blocked.ID); len(got.BlockedBy) != 1 || got.BlockedBy[0] != ready.ID || got.Detail != ready.ID {
		t.Fatalf("blocked by what is not integrated yet: %+v", got.BlockedBy)
	}
	if got := o.item(t, done.ID); got.PR.State != "merged" || got.IntegratedAt.IsZero() || got.ReworkRounds != 1 || got.Usage.Tokens != 50 || got.StepSeconds != 0 {
		t.Fatalf("integrated: %+v", got)
	}
	if got := o.item(t, queued.ID); got.PR.State != "ready" {
		t.Fatalf("queued: %+v", got.PR)
	}
	if got := o.item(t, taken.ID); !got.Paused {
		t.Fatalf("taken over: %+v", got)
	}
	if p.Usage.Tokens != 1250 || p.UsageByMachine["m2"].Tokens != 1050 || p.UsageByAccount["acc-m1"].Tokens != 200 || len(p.UsageByDay) != 1 || o.Usage.CostUSD != 3 {
		t.Fatalf("project usage: %+v", p.Usage)
	}
	if p.OpenQuestions != 1 {
		t.Fatalf("open questions %d", p.OpenQuestions)
	}

	m2 := o.machine(t, "m2")
	if m2.Machine.Health != "green" || len(m2.Sessions) != 1 || len(m2.Worktrees) != 3 || m2.WorktreeBytes != 5<<30 || m2.DiskFreeBytes != 10<<30 || !m2.ProcsSupported {
		t.Fatalf("m2: %+v", m2)
	}
	if m2.Procs == nil || m2.Procs.Count != 2 || m2.Usage.Tokens != 1050 || len(m2.Items) != 2 || m2.Items[0] != "demo/"+working.ID || !m2.ReportedAt.Equal(now) {
		t.Fatalf("m2 totals: procs %+v usage %+v items %v", m2.Procs, m2.Usage, m2.Items)
	}
	if m3 := o.machine(t, "m3"); m3.Machine.Health != "red" || len(m3.Sessions) != 0 || len(m3.Items) != 1 {
		t.Fatalf("m3: %+v", m3)
	}
	if m1 := o.machine(t, "m1"); m1.Usage.Tokens != 200 || m1.ProcsSupported || len(m1.Worktrees) != 1 {
		t.Fatalf("m1: %+v", m1)
	}

	// Choosing what to see.
	all, _ := e.svc.Overview(e.ctx, owner(), OverviewQuery{All: true})
	if all.Projects[0].ItemsMatched != 12 {
		t.Fatalf("--all lists the old ones too: %d", all.Projects[0].ItemsMatched)
	}
	longer, _ := e.svc.Overview(e.ctx, owner(), OverviewQuery{Finished: 30 * 24 * time.Hour})
	if longer.Projects[0].ItemsMatched != 12 {
		t.Fatalf("a longer window: %d", longer.Projects[0].ItemsMatched)
	}
	byAct, _ := e.svc.Overview(e.ctx, owner(), OverviewQuery{Activity: []string{ActWorking, "integrated"}})
	if n := byAct.Projects[0].ItemsMatched; n != 3 {
		t.Fatalf("by activity or status, finished ones of any age: %d", n)
	}
	onM2, _ := e.svc.Overview(e.ctx, owner(), OverviewQuery{Machine: "m2"})
	if n := onM2.Projects[0].ItemsMatched; n != 2 || len(onM2.Machines) != 1 || onM2.Machines[0].Machine.Name != "m2" {
		t.Fatalf("on one machine: %d items, %d machines", n, len(onM2.Machines))
	}
	onM1, _ := e.svc.Overview(e.ctx, owner(), OverviewQuery{Machine: "m1"})
	if n := onM1.Projects[0].ItemsMatched; n != 3 {
		t.Fatalf("m1 holds one item, keeps a worktree of another and integrated a third: %d", n)
	}
	one, err := e.svc.Overview(e.ctx, owner(), OverviewQuery{Project: "demo", Item: "i-" + oldDone.ID[2:]})
	if err != nil || len(one.Projects[0].Items) != 1 || one.Projects[0].Items[0].ID != oldDone.ID {
		t.Fatalf("one item, whatever its age: %v %+v", err, one.Projects[0].Items)
	}
	page, _ := e.svc.Overview(e.ctx, owner(), OverviewQuery{Limit: 2, Offset: 1})
	if pp := page.Projects[0]; len(pp.Items) != 2 || !pp.Truncated || pp.ItemsMatched != 11 || pp.Items[0].ID != checking.ID {
		t.Fatalf("page: %d items, truncated=%v, first %s", len(pp.Items), pp.Truncated, pp.Items[0].ID)
	}
	past, _ := e.svc.Overview(e.ctx, owner(), OverviewQuery{Offset: 100})
	if pp := past.Projects[0]; len(pp.Items) != 0 || pp.Items == nil || pp.Truncated {
		t.Fatalf("past the end: %+v", pp.Items)
	}
	brief, _ := e.svc.Overview(e.ctx, owner(), OverviewQuery{Brief: true})
	bw, bc := brief.item(t, working.ID), brief.item(t, checking.ID)
	if bw.Usage.Tokens != 1200 || bw.UsageByStep != nil || bc.Checks != nil || bc.Procs.Count != 2 || len(bc.Procs.Top) != 0 || len(bc.Worktrees[0].Procs.Top) != 0 {
		t.Fatalf("brief keeps the totals and drops the lists: %+v", bc.Procs)
	}
	if got := o.item(t, checking.ID); len(got.Worktrees[0].Procs.Top) != 2 {
		t.Fatalf("a brief answer changed what the daemon remembers")
	}

	// Who may see.
	if _, err := e.svc.Overview(e.ctx, Actor{Kind: ActorToken, Name: "blind"}, OverviewQuery{}); err == nil {
		t.Fatal("a token without scopes saw the overview")
	}
	_, err = e.svc.Overview(e.ctx, Actor{Kind: ActorToken, Name: "blind"}, OverviewQuery{Project: "demo"})
	wantErr(t, err, ErrForbidden)
	_, err = e.svc.Overview(e.ctx, owner(), OverviewQuery{Project: "nope"})
	wantErr(t, err, ErrUnknownProj)
	_, err = e.svc.Overview(e.ctx, owner(), OverviewQuery{Item: "not an id"})
	wantErr(t, err, ErrInvalid)

	// The whole project paused.
	if err := e.svc.Pause(e.ctx, "demo", owner(), "release freeze"); err != nil {
		t.Fatal(err)
	}
	paused, _ := e.svc.Overview(e.ctx, owner(), OverviewQuery{})
	if pp := paused.Projects[0]; !pp.Paused || pp.Activity[ActPaused] != 10 || pp.Activity[ActIntegrated] != 2 {
		t.Fatalf("paused project: %v", pp.Activity)
	}
}

// Another machine answers the same from the replicated log alone.
func TestOverviewIsTheSameOnEveryMachine(t *testing.T) {
	e := newEnv(t)
	a := e.ready("simple", "A", "cards")
	e.usage(domain.TokenUsage{Agent: "implementer-m1-1", Provider: "fake", Model: "big", Role: "implementer", ItemID: a.ID, Step: "implement", Input: 500, Duration: time.Minute, CPUSeconds: 30, PeakRSSBytes: 1 << 30})
	other := e.peer("m2")
	other.sync(e) // a second delivery of the same log changes nothing
	here, _ := e.svc.Overview(e.ctx, owner(), OverviewQuery{})
	there, err := other.svc.Overview(other.ctx, owner(), OverviewQuery{})
	if err != nil {
		t.Fatal(err)
	}
	h, th := here.item(t, a.ID), there.item(t, a.ID)
	if th.Usage != h.Usage || th.Usage.Tokens != 500 || th.Usage.CPUSeconds != 30 || th.Usage.PeakRSSBytes != 1<<30 || th.UsageByMachine["m1"].Tokens != 500 {
		t.Fatalf("m2 sees %+v, m1 sees %+v", th.Usage, h.Usage)
	}
	if there.Machine != "m2" || there.Usage != here.Usage {
		t.Fatalf("totals differ: %+v %+v", there.Usage, here.Usage)
	}
}

// The answer does not grow with the history: finished items are counted
// from memory and only the ones asked for are detailed.
func TestOverviewWithALongHistory(t *testing.T) {
	e := newEnv(t)
	const n = 1500
	at := e.clock.Now().Add(-90 * 24 * time.Hour)
	e.p.mu.Lock()
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("I-%d", 1000+i)
		it := &domain.Item{ID: id, Version: 1, Title: "old " + id, Kind: "simple", Status: domain.StatusIntegrated, CreatedAt: at, UpdatedAt: at, IntegratedAt: at}
		_ = e.p.state.Apply(domain.NewEvent("old-"+id, domain.EvItemIntegrated, "demo", id, "m1", "x", at, domain.Payload{Item: it}))
		u := domain.TokenUsage{ItemID: id, Step: "implement", Provider: "fake", Model: "big", Role: "implementer", Input: 10}
		_ = e.p.state.Apply(domain.NewEvent("use-"+id, domain.EvAgentUsage, "demo", id, "m1", "x", at, domain.Payload{Usage: &u}))
	}
	e.p.mu.Unlock()
	active := e.ready("simple", "now", "cards")
	o, err := e.svc.Overview(e.ctx, owner(), OverviewQuery{})
	if err != nil {
		t.Fatal(err)
	}
	p := o.Projects[0]
	if len(p.Items) != 1 || p.Items[0].ID != active.ID || p.ItemsTotal != n+1 || p.Activity[ActIntegrated] != n || p.Usage.Tokens != 10*n {
		t.Fatalf("items %d of %d, usage %d", len(p.Items), p.ItemsTotal, p.Usage.Tokens)
	}
	if len(p.UsageByDay) != 0 {
		t.Fatalf("days older than the window are left out: %d", len(p.UsageByDay))
	}
	all, _ := e.svc.Overview(e.ctx, owner(), OverviewQuery{All: true, Limit: 100000})
	if pp := all.Projects[0]; len(pp.Items) != n+1 || pp.Truncated {
		t.Fatalf("the limit has a ceiling above this history: %d", len(pp.Items))
	}
	capped, _ := e.svc.Overview(e.ctx, owner(), OverviewQuery{All: true})
	if pp := capped.Projects[0]; len(pp.Items) != overviewLimit || !pp.Truncated || pp.ItemsMatched != n+1 {
		t.Fatalf("default page: %d items, truncated=%v", len(pp.Items), pp.Truncated)
	}
}

// The cost of an answer, with 5000 finished items behind the active ones.
func BenchmarkOverviewLongHistory(b *testing.B) {
	e := newEnv(&testing.T{})
	at := e.clock.Now().Add(-90 * 24 * time.Hour)
	e.p.mu.Lock()
	for i := 1; i <= 5000; i++ {
		id := fmt.Sprintf("I-%d", 1000+i)
		it := &domain.Item{ID: id, Version: 1, Title: "old", Kind: "simple", Status: domain.StatusIntegrated, CreatedAt: at, UpdatedAt: at, IntegratedAt: at}
		_ = e.p.state.Apply(domain.NewEvent("old-"+id, domain.EvItemIntegrated, "demo", id, "m1", "x", at, domain.Payload{Item: it}))
		u := domain.TokenUsage{ItemID: id, Step: "implement", Provider: "fake", Model: "big", Input: 10}
		_ = e.p.state.Apply(domain.NewEvent("use-"+id, domain.EvAgentUsage, "demo", id, "m1", "x", at, domain.Payload{Usage: &u}))
	}
	e.p.mu.Unlock()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := e.svc.Overview(e.ctx, owner(), OverviewQuery{}); err != nil {
			b.Fatal(err)
		}
	}
}
