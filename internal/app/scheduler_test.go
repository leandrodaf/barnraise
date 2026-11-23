package app

import (
	"errors"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

func TestNextWorkCapacitySplit(t *testing.T) {
	e := newEnv(t)
	if _, _, err := e.svc.SetGoal(e.ctx, "demo", owner(), domain.Goal{Name: "MVP", DoneWhen: "done"}); err != nil {
		t.Fatal(err)
	}
	lane := map[string]domain.Lane{}
	for range 20 {
		lane[e.ready("feature", "goal work", "").ID] = domain.LaneGoal
	}
	for range 6 {
		lane[e.ready("bug", "bug work", "").ID] = domain.LaneBugs
	}
	for range 4 {
		lane[e.ready("improvement", "small work", "").ID] = domain.LaneImprovement
	}
	_ = lane
	counts := map[domain.Lane]int{}
	for i := range 20 {
		e.clock.Advance(1)
		a, err := e.svc.NextWork(e.ctx, "demo", Slot{Agent: "implementer-" + string(rune('a'+i)), Role: "implementer", Machine: domain.Machine{Name: "m1"}})
		if err != nil {
			t.Fatal(err)
		}
		if !a.New {
			t.Fatalf("claim %d returned held work %s", i, a.Item.ID)
		}
		counts[a.Item.Lane]++
	}
	if counts[domain.LaneGoal] != 16 || counts[domain.LaneBugs] != 3 || counts[domain.LaneImprovement] != 1 {
		t.Fatalf("lanes after 20 claims = %v, want 16/3/1", counts)
	}
}

func TestNextWorkOrder(t *testing.T) {
	t.Run("P0 bugs first", func(t *testing.T) {
		e := newEnv(t)
		e.ready("feature", "goal", "")
		e.ready("improvement", "imp", "")
		p0 := e.create(NewItem{Title: "outage", Kind: "bug", Priority: "P0", Criteria: crit(ears)})
		e.next("implementer", p0.ID)
	})
	t.Run("self improvement only when nothing else, once a week", func(t *testing.T) {
		e := newEnv(t)
		self := e.ready("self_improvement", "better prompts", "")
		self2 := e.ready("self_improvement", "better checks", "")
		e.nextBusy("implementer", self.ID)
		_ = self2
		_, err := e.svc.NextWork(e.ctx, "demo", Slot{Agent: "implementer-x", Role: "implementer"})
		wantErr(t, err, ErrNoWork)
	})
	t.Run("paused", func(t *testing.T) {
		e := newEnv(t)
		e.ready("feature", "goal", "")
		if err := e.svc.Pause(e.ctx, "demo", owner(), "test"); err != nil {
			t.Fatal(err)
		}
		_, err := e.svc.NextWork(e.ctx, "demo", slot("implementer"))
		wantErr(t, err, ErrPaused)
		_, err = e.svc.NextWork(e.ctx, "nope", slot("implementer"))
		wantErr(t, err, ErrUnknownProj)
	})
	t.Run("dependencies, drafts and claims of other machines", func(t *testing.T) {
		e := newEnv(t)
		base := e.ready("feature", "base", "")
		dep := e.create(NewItem{Title: "after base", Criteria: crit(ears), Deps: []string{base.ID}})
		e.create(NewItem{Title: "draft"})
		if dep.Status != domain.StatusBlocked {
			t.Fatal(dep.Status)
		}
		other := e.backend.Handle(testkit.NewClock())
		if _, err := other.Claim(e.ctx, claimKey(base.ID), "m2"); err != nil {
			t.Fatal(err)
		}
		_, err := e.svc.NextWork(e.ctx, "demo", slot("implementer"))
		wantErr(t, err, ErrNoWork)
		if e.item(base.ID).Claim != nil {
			t.Fatal("an item taken in the arbiter must not be claimed here")
		}
	})
	t.Run("a ready item whose dependency came back waits", func(t *testing.T) {
		e := newEnv(t)
		base := e.ready("feature", "base", "")
		if _, err := e.svc.CloseItem(e.ctx, "demo", owner(), base.ID, "obsolete", "", ""); err != nil {
			t.Fatal(err)
		}
		dep := e.create(NewItem{Title: "after base", Criteria: crit(ears), Deps: []string{base.ID}})
		if dep.Status != domain.StatusReady {
			t.Fatal(dep.Status)
		}
		if _, err := e.svc.Redo(e.ctx, "demo", owner(), base.ID, "plan", "reopened"); err != nil {
			t.Fatal(err)
		}
		e.nextBusy("implementer", base.ID)
		_, err := e.svc.NextWork(e.ctx, "demo", Slot{Agent: "implementer-2", Role: "implementer"})
		wantErr(t, err, ErrNoWork)
	})
	t.Run("file overlap keeps two agents apart", func(t *testing.T) {
		e := newEnv(t)
		a := e.create(NewItem{Title: "a", Criteria: crit(ears), Anchors: domain.Anchors{Files: []string{"app/x.go"}}})
		b := e.create(NewItem{Title: "b", Criteria: crit(ears), Anchors: domain.Anchors{Files: []string{"app/*.go"}}})
		c := e.create(NewItem{Title: "c", Criteria: crit(ears), Anchors: domain.Anchors{Files: []string{"lib/y.go"}}})
		e.nextBusy("implementer", a.ID)
		got, err := e.svc.NextWork(e.ctx, "demo", Slot{Agent: "implementer-2", Role: "implementer"})
		if err != nil || got.Item.ID != c.ID {
			t.Fatalf("got %v %v, want %s (not %s)", got, err, c.ID, b.ID)
		}
	})
	t.Run("roles and machine labels", func(t *testing.T) {
		e := newEnv(t, withCfg(func(c *domain.ProjectConfig) {
			r := c.Roles["implementer"]
			r.Needs = []string{"emulators"}
			c.Roles["implementer"] = r
		}))
		it := e.ready("feature", "mobile", "")
		_, err := e.svc.NextWork(e.ctx, "demo", slot("implementer"))
		wantErr(t, err, ErrNoWork)
		_, err = e.svc.NextWork(e.ctx, "demo", slot("reviewer"))
		wantErr(t, err, ErrNoWork)
		_, err = e.svc.NextWork(e.ctx, "demo", Slot{Agent: "x", Role: "ghost"})
		wantErr(t, err, ErrNoWork)
		a, err := e.svc.NextWork(e.ctx, "demo", Slot{Agent: "implementer-1", Role: "implementer", Machine: domain.Machine{Emulators: 1}})
		if err != nil || a.Item.ID != it.ID {
			t.Fatal(a, err)
		}
	})
	t.Run("a flow that starts with a driver step goes to its first agent role", func(t *testing.T) {
		e := newEnv(t, withCfg(func(c *domain.ProjectConfig) {
			c.Flows["checkfirst"] = &domain.Flow{Name: "checkfirst", Limit: domain.DefaultReworkLimit, Steps: []domain.StepDef{
				{Name: "check", Type: domain.StepCheck, Run: "checks.main"}, {Name: "implement", Type: domain.StepAgent, Role: "implementer"}, {Name: "integrate", Type: domain.StepMergeQueue}}}
		}))
		it := e.ready("checkfirst", "audit", "")
		_, err := e.svc.NextWork(e.ctx, "demo", slot("reviewer"))
		wantErr(t, err, ErrNoWork)
		a := e.next("implementer", it.ID)
		if a.Step.Name != "check" {
			t.Fatalf("step %s", a.Step.Name)
		}
		e.drive(it.ID)
		e.wantStatus(it.ID, domain.StatusInProgress, "implement")
	})
}

func TestNextWorkHeldFirst(t *testing.T) {
	e := newEnv(t)
	low := e.create(NewItem{Title: "low", Kind: "simple", Priority: "P3", Criteria: crit(ears)})
	high := e.create(NewItem{Title: "high", Kind: "simple", Priority: "P1", Criteria: crit(ears)})
	e.next("implementer", high.ID)
	e.next("implementer", high.ID) // held work comes before new work
	if a, _ := e.svc.NextWork(e.ctx, "demo", slot("implementer")); a.Item.ID != high.ID || a.New {
		t.Fatalf("held %+v", a)
	}
	// While its session runs, the held item is busy: the next slot claims new work.
	a, err := e.svc.NextWork(e.ctx, "demo", Slot{Agent: "implementer-2", Role: "implementer"})
	if err != nil || a.Item.ID != low.ID || !a.New {
		t.Fatalf("new work %+v %v", a, err)
	}
}

func TestClaimAttachesTweaks(t *testing.T) {
	e := newEnv(t)
	if _, _, err := e.svc.AddTweak(e.ctx, "demo", owner(), "cards", "rename the button"); err != nil {
		t.Fatal(err)
	}
	it := e.ready("feature", "cards", "cards")
	a := e.next("implementer", it.ID)
	if len(a.Item.Tweaks) != 1 || a.Item.Tweaks[0] != "rename the button" || a.Item.Branch == "" || a.Item.Claim.Machine != "m1" {
		t.Fatalf("claimed %+v", a.Item)
	}
	for _, tw := range e.p.Snapshot().Tweaks {
		if tw.ItemID != it.ID {
			t.Fatalf("tweak not attached: %+v", tw)
		}
	}
	if e.backend.Holder(claimKey(it.ID)) != "m1" {
		t.Fatal("the claim must be in the arbiter")
	}
}

func TestValidBeforeStart(t *testing.T) {
	g := &testkit.FakeGit{Main: "c1"}
	e := newEnv(t, withFakeGit(g), withTriage())
	untouched := e.create(NewItem{Title: "untouched", Criteria: crit(ears), Anchors: domain.Anchors{Files: []string{"a.go"}}})
	if untouched.Anchors.Base != "c1" {
		t.Fatalf("base %q", untouched.Anchors.Base)
	}
	g.SetMain("c2")
	g.Changed = map[string][]string{"*": {"b.go"}}
	e.nextBusy("implementer", untouched.ID)
	if b := e.item(untouched.ID).Anchors.Base; b != "c2" {
		t.Fatalf("base moved to %q, want c2", b)
	}

	stale := e.create(NewItem{Title: "stale", Criteria: crit(ears), Anchors: domain.Anchors{Files: []string{"c.go"}}})
	g.SetMain("c3")
	g.Changed = map[string][]string{"*": {"c.go", "d.go"}}
	e.triage.Set(domain.TriageResult{ItemID: stale.ID, Verdict: domain.TriageObsolete, Reason: "a.go was removed"})
	_, err := e.svc.NextWork(e.ctx, "demo", Slot{Agent: "implementer-2", Role: "implementer"})
	wantErr(t, err, ErrNoWork)
	e.wantStatus(stale.ID, domain.StatusClosed, "")

	ok := e.create(NewItem{Title: "still fine", Criteria: crit(ears), Anchors: domain.Anchors{Files: []string{"d.go"}}})
	g.SetMain("c4")
	e.triage.Err = errors.New("model down")
	e.next("implementer", ok.ID) // triage failing never blocks the queue
}

func TestRefineWork(t *testing.T) {
	e := newEnv(t)
	if !e.svc.holdHousekeeping(e.ctx, e.p) {
		t.Fatal("the only machine holds housekeeping")
	}
	_, err := e.svc.NextWork(e.ctx, "demo", slot("analyst"))
	wantErr(t, err, ErrNoWork)
	d := e.create(NewItem{Title: "vague", Body: "[NEEDS CLARIFICATION: what?]"})
	d2 := e.create(NewItem{Title: "urgent vague", Priority: "P0"})
	a, err := e.svc.NextWork(e.ctx, "demo", slot("analyst"))
	if err != nil || a.Item.ID != d2.ID || a.Step.Name != RefineStep || a.Step.Role != "analyst" {
		t.Fatalf("refine %+v %v", a, err)
	}
	e.svc.doneRunning(e.p, d2.ID, "")
	if a := e.next("analyst", d.ID); a.Step.Name != RefineStep {
		t.Fatal(a.Step)
	}
	_, err = e.svc.NextWork(e.ctx, "demo", slot("analyst"))
	wantErr(t, err, ErrNoWork) // each version is refined once; then it waits for a person
	if _, err := e.svc.UpdateItem(e.ctx, "demo", owner(), d.ID, ItemPatch{Size: new("4h")}); err != nil {
		t.Fatal(err)
	}
	e.next("analyst", d.ID)
	dir, err := e.svc.refineWorkspace(e.ctx, e.p, d.ID)
	if err != nil || dir != e.cfg.Root {
		t.Fatalf("without git the analyst reads the project root: %s %v", dir, err)
	}
}

func TestRefineWorkspaceOnGit(t *testing.T) {
	e := newEnv(t, withGit())
	d := e.create(NewItem{Title: "vague"})
	dir, err := e.svc.refineWorkspace(e.ctx, e.p, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	head := testkit.RunGit(t, dir, "rev-parse", "HEAD")
	if head != e.repo.RemoteMain(t) {
		t.Fatalf("refine worktree at %s, main %s", head, e.repo.RemoteMain(t))
	}
	if dir2, err := e.svc.refineWorkspace(e.ctx, e.p, d.ID); err != nil || dir2 != dir {
		t.Fatalf("second call reuses the worktree: %s %v", dir2, err)
	}
}

func TestClaimForAgent(t *testing.T) {
	e := newEnv(t)
	it := e.ready("feature", "x", "")
	_, err := e.svc.ClaimForAgent(e.ctx, "demo", Actor{Kind: ActorAgent, Name: "a"}, domain.Machine{})
	wantErr(t, err, ErrForbidden)
	a := agent("implementer", "", "")
	a.Role = ""
	_, err = e.svc.ClaimForAgent(e.ctx, "demo", a, domain.Machine{})
	wantErr(t, err, ErrInvalid)
	got, err := e.svc.ClaimForAgent(e.ctx, "demo", agent("implementer", "", ""), domain.Machine{})
	if err != nil || got.Item.ID != it.ID || !got.New {
		t.Fatalf("%+v %v", got, err)
	}
	cur, err := e.svc.ClaimForAgent(e.ctx, "demo", agent("implementer", it.ID, ""), domain.Machine{})
	if err != nil || cur.Item.ID != it.ID || cur.New || cur.Step.Name != "plan" {
		t.Fatalf("current item %+v %v", cur, err)
	}
	_, err = e.svc.ClaimForAgent(e.ctx, "nope", Actor{Kind: ActorAgent, Role: "implementer", Scopes: []string{ScopeAll}}, domain.Machine{})
	wantErr(t, err, ErrUnknownProj)
}

func TestReleaseErrorsAreQuiet(t *testing.T) {
	e := newEnv(t)
	e.svc.releaseLeaseValue(e.ctx, e.p, ports.Lease{})
	e.svc.releaseLeaseValue(e.ctx, e.p, ports.Lease{Key: "claims/I-1", Version: "v9"})
	e.backend.SetOffline(true)
	e.svc.releaseLeaseValue(e.ctx, e.p, ports.Lease{Key: "claims/I-1", Version: "v9"})
}

// Drafts have no claim, so only the machine holding the housekeeping lease
// refines them: two machines' analysts refined the same draft at once.
func TestRefineOnlyOnTheHousekeepingMachine(t *testing.T) {
	e := newEnv(t)
	d := e.create(NewItem{Title: "vague", Body: "[NEEDS CLARIFICATION: what?]"})
	if !e.svc.holdHousekeeping(e.ctx, e.p) {
		t.Fatal("m1 takes housekeeping")
	}
	p := e.peer("m2")
	if p.svc.holdHousekeeping(p.ctx, p.p) {
		t.Fatal("m2 cannot take a live housekeeping lease")
	}
	if _, err := p.svc.NextWork(p.ctx, "demo", slot("analyst")); !errors.Is(err, ErrNoWork) {
		t.Fatalf("m2 refines without the lease: %v", err)
	}
	if a := e.next("analyst", d.ID); a.Step.Name != RefineStep {
		t.Fatal(a.Step)
	}
}
