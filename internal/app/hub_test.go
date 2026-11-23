package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

func TestMatchType(t *testing.T) {
	for _, tc := range []struct {
		filters []string
		typ     string
		want    bool
	}{
		{nil, "item.created", true},
		{[]string{"*"}, "anything", true},
		{[]string{"item.created"}, "item.created", true},
		{[]string{"item.created"}, "item.updated", false},
		{[]string{"item.*"}, "item.updated", true},
		{[]string{"item.*"}, "itemized.updated", false},
		{[]string{"check.*", "item.created"}, "item.created", true},
		{[]string{"check.*"}, "item.created", false},
	} {
		if got := MatchType(tc.filters, tc.typ); got != tc.want {
			t.Errorf("MatchType(%v, %q) = %v, want %v", tc.filters, tc.typ, got, tc.want)
		}
	}
}

func TestHubFiltersAndNeverBlocks(t *testing.T) {
	h := NewHub()
	all, cancelAll := h.Subscribe("", nil, 0)
	demo, cancelDemo := h.Subscribe("demo", []string{"item.*"}, 1)
	ev := func(project, typ string) domain.Event {
		return domain.Event{ID: project + typ, Project: project, Type: typ}
	}

	h.Publish(ev("demo", "item.created"))
	h.Publish(ev("other", "item.created")) // another project
	h.Publish(ev("demo", "check.result"))  // another type
	h.Publish(ev("demo", "item.updated"))  // demo's buffer of one is full: dropped, not blocked

	if got := len(all); got != 4 {
		t.Fatalf("the unfiltered subscriber gets everything: %d", got)
	}
	if got := len(demo); got != 1 {
		t.Fatalf("filtered and bounded: %d", got)
	}
	if e := <-demo; e.Type != "item.created" || e.Project != "demo" {
		t.Fatalf("event %+v", e)
	}
	cancelDemo()
	cancelDemo() // twice is fine
	if _, open := <-demo; open {
		t.Fatal("cancel closes the channel")
	}
	h.Publish(ev("demo", "item.created")) // nobody panics after a cancel
	cancelAll()
}

func TestServiceEventsReachTheHub(t *testing.T) {
	e := newEnv(t)
	ch, cancel := e.svc.Hub().Subscribe("demo", []string{domain.EvItemCreated}, 8)
	defer cancel()
	it := e.create(NewItem{Title: "Seen live", Kind: "simple", Area: "x", Criteria: crit(ears)})
	select {
	case ev := <-ch:
		if ev.Subject != it.ID || ev.Machine != "m1" {
			t.Fatalf("event %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no live event")
	}
}

func TestMCPToolsFollowTheScopes(t *testing.T) {
	if got := MCPTools(nil); len(got) != 0 {
		t.Fatalf("no scopes, no tools: %v", got)
	}
	impl := MCPTools(RoleScopes("implementer", false))
	rev := MCPTools(RoleScopes("reviewer", true))
	if !contains(impl, "deliver") || contains(impl, "review") || contains(impl, "approve") {
		t.Fatalf("implementer tools %v", impl)
	}
	if !contains(rev, "review") || contains(rev, "deliver") {
		t.Fatalf("reviewer tools %v", rev)
	}
	for _, tools := range [][]string{impl, rev} {
		for _, want := range []string{"status", "context", "find"} {
			if !contains(tools, want) {
				t.Fatalf("%v lacks %s", tools, want)
			}
		}
	}
	all := MCPTools([]string{ScopeAll})
	for _, want := range []string{"next_item", "deliver", "review", "propose", "ask_owner", "message", "status", "context", "find", "approve", "focus", "pause"} {
		if !contains(all, want) {
			t.Fatalf("owner tools %v lack %s", all, want)
		}
	}
}

func TestSessionTokens(t *testing.T) {
	r := NewTokenRegistry()
	a := agent("implementer", "I-1", "implement")
	b := agent("reviewer", "I-2", "review")
	ta, ta2, tb := r.Issue(a, time.Hour), r.Issue(a, time.Hour), r.Issue(b, time.Hour)
	if ta == ta2 || len(ta) < 40 {
		t.Fatal("tokens are random and long")
	}
	if got, ok := r.Resolve(ta); !ok || got.Name != a.Name || got.ItemID != "I-1" {
		t.Fatalf("resolve %+v %v", got, ok)
	}
	if _, ok := r.Resolve("brs_unknown"); ok {
		t.Fatal("unknown token")
	}
	if _, ok := r.Resolve(r.Issue(a, -time.Second)); ok {
		t.Fatal("expired token")
	}
	r.RevokeAgent(a.Name)
	if _, ok := r.Resolve(ta); ok {
		t.Fatal("revoked")
	}
	if _, ok := r.Resolve(ta2); ok {
		t.Fatal("every token of the agent is revoked")
	}
	if _, ok := r.Resolve(tb); !ok {
		t.Fatal("other agents keep their tokens")
	}
	r.Revoke(tb)
	if _, ok := r.Resolve(tb); ok {
		t.Fatal("revoked")
	}
	// The owner's tokens are not an agent's, whatever the name.
	own := Owner(a.Name)
	to := r.Issue(own, time.Hour)
	r.RevokeAgent(a.Name)
	if _, ok := r.Resolve(to); !ok {
		t.Fatal("RevokeAgent only touches agent tokens")
	}
}

func TestValidateScopes(t *testing.T) {
	if err := ValidateScopes(append([]string{ScopeAll}, AllScopes...)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateScopes(nil); err != nil {
		t.Fatal(err)
	}
	if err := ValidateScopes([]string{ScopeItemsRead, "items:destroy"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown scope: %v", err)
	}
}

func TestActorNameAndItemAccess(t *testing.T) {
	if actorName(Actor{Kind: ActorSystem}) != string(ActorSystem) || actorName(owner()) != "leandro" {
		t.Fatal("actor name")
	}
	a := agent("implementer", "I-1", "implement")
	if err := checkItemAccess(a, "I-1"); err != nil {
		t.Fatal(err)
	}
	if err := checkItemAccess(a, "I-2"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an agent only writes to its own item: %v", err)
	}
	if err := checkItemAccess(owner(), "I-2"); err != nil {
		t.Fatal(err)
	}
}

// Events mirrored from another machine before this machine loaded the
// project must not be lost: the replication cursor has already moved on.
func TestIngestKeepsEventsOfAProjectLoadedLater(t *testing.T) {
	later := func(o *envOpts) { o.yaml = strings.Replace(o.yaml, "project: demo", "project: later", 1) }
	alpha := newEnv(t, withMachine("alpha"), later)
	res, err := alpha.svc.CreateItem(alpha.ctx, "later", owner(), NewItem{Title: "Made on alpha", Kind: "simple", Area: "x", Criteria: crit(ears)})
	if err != nil {
		t.Fatal(err)
	}
	evs, err := alpha.store.Load(alpha.ctx, "later", 0)
	if err != nil || len(evs) == 0 {
		t.Fatalf("alpha events %d %v", len(evs), err)
	}

	beta := newEnv(t, withMachine("beta")) // drives "demo" only, for now
	for _, e := range evs {
		if err := beta.svc.Ingest(beta.ctx, e); err != nil {
			t.Fatalf("ingest %s: %v", e.Type, err)
		}
	}
	// Live-only and project-less events of an unknown project are dropped, not stored.
	if err := beta.svc.Ingest(beta.ctx, domain.Event{ID: "hb-1", Type: domain.EvMachineUpdated, Project: "later", Machine: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := beta.svc.Ingest(beta.ctx, domain.Event{ID: "np-1", Type: domain.EvItemUpdated, Machine: "alpha"}); err != nil {
		t.Fatal(err)
	}
	kept, _ := beta.store.Load(beta.ctx, "later", 0)
	if len(kept) != len(evs) {
		t.Fatalf("beta kept %d of %d events", len(kept), len(evs))
	}
	if _, err := beta.svc.GetItem("later", owner(), res.Item.ID); err == nil {
		t.Fatal("the project is not loaded yet")
	}

	p, err := beta.svc.AddProject(beta.ctx, alpha.cfg, ProjectPorts{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := beta.svc.GetItem("later", owner(), res.Item.ID)
	if err != nil || got.Title != "Made on alpha" {
		t.Fatalf("after loading the project beta must see alpha's item: %+v %v", got, err)
	}
	// Delivered again (at-least-once): nothing changes.
	for _, e := range evs {
		if err := beta.svc.Ingest(beta.ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if again, _ := beta.store.Load(beta.ctx, "later", 0); len(again) != len(evs) || len(p.Snapshot().Items) != 1 {
		t.Fatalf("repeats must be ignored: %d events, %d items", len(again), len(p.Snapshot().Items))
	}
	// A machine never ingests its own events.
	own := evs[0]
	own.ID, own.Machine = "own-1", "beta"
	if err := beta.svc.Ingest(beta.ctx, own); err != nil {
		t.Fatal(err)
	}
	if n, _ := beta.store.Load(beta.ctx, "later", 0); len(n) != len(evs) {
		t.Fatal("own event stored")
	}
}

// An item this machine worked on and another machine finished (a takeover)
// leaves no worktree behind here.
func TestIngestDropsTheWorktreeOfAnItemFinishedElsewhere(t *testing.T) {
	g := &testkit.FakeGit{}
	e := newEnv(t, withFakeGit(g))
	it := e.ready("simple", "taken over", "x")
	e.start(it.ID)
	dir := e.workspace(it.ID, map[string]string{"a.txt": "a"})
	done := e.item(it.ID)
	done.Status, done.Claim, done.Version = domain.StatusIntegrated, nil, done.Version+5
	ev := domain.NewEvent("m2-done", domain.EvItemIntegrated, "demo", it.ID, "m2", "merge-queue", e.clock.Now(), domain.Payload{Item: done})
	if err := e.svc.Ingest(e.ctx, ev); err != nil {
		t.Fatal(err)
	}
	e.wantStatus(it.ID, domain.StatusIntegrated, "")
	if !g.Called("RemoveWorktree " + dir) {
		t.Fatalf("the stale worktree must be removed: %v", g.Calls())
	}
	// An item still in progress elsewhere keeps its worktree.
	other := e.ready("simple", "still going", "x")
	e.start(other.ID)
	odir := e.workspace(other.ID, nil)
	cur := e.item(other.ID)
	cur.Version += 5
	if err := e.svc.Ingest(e.ctx, domain.NewEvent("m2-upd", domain.EvItemUpdated, "demo", other.ID, "m2", "x", e.clock.Now(), domain.Payload{Item: cur})); err != nil {
		t.Fatal(err)
	}
	if g.Called("RemoveWorktree " + odir) {
		t.Fatal("only finished items lose their worktree")
	}
}
