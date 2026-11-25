package app

import (
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

// integrateFake marks an item integrated now with rework rounds, as the merge queue would.
func (e *env) integrateFake(it *domain.Item, rounds int, from string) {
	e.t.Helper()
	_, err := e.svc.mutateItem(e.ctx, e.p, it.ID, domain.EvItemIntegrated, "merge-queue", func(x *domain.Item) error {
		x.Status, x.IntegratedAt, x.Claim = domain.StatusIntegrated, e.clock.Now().UTC(), &domain.Claim{Machine: "m1", Since: e.clock.Now().Add(-2 * time.Hour)}
		for range rounds {
			rr := domain.ReworkRecord{From: from, To: "implement"}
			x.Rework = append(x.Rework, rr)
			if _, err := e.svc.emitLocked(e.ctx, e.p, domain.EvReworkStarted, x.ID, "x", domain.Payload{Rework: &rr}); err != nil {
				return err
			}
		}
		return nil
	}, domain.Payload{})
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) usage(u domain.TokenUsage) {
	e.t.Helper()
	if _, err := e.svc.emit(e.ctx, e.p, domain.EvAgentUsage, u.ItemID, u.Agent, domain.Payload{Usage: &u}); err != nil {
		e.t.Fatal(err)
	}
}

func TestMetricsAndAlerts(t *testing.T) {
	e := newEnv(t)
	a := e.ready("feature", "A", "cards")
	b := e.ready("feature", "B", "cards")
	e.integrateFake(a, 3, "review")
	e.integrateFake(b, 4, "review")
	e.usage(domain.TokenUsage{Provider: "fake", Model: "big", Role: "implementer", Step: "implement", ItemID: a.ID, Input: 100, Output: 50, CacheRead: 300, Duration: time.Minute})
	e.usage(domain.TokenUsage{Provider: "fake", Model: "small", Role: "reviewer", Step: "review", ItemID: a.ID, Input: 40, Duration: 3 * time.Minute, ExploreReads: 7})
	for i := range 6 {
		if _, err := e.svc.Propose(e.ctx, "demo", owner(), prop("idea number "+string(rune('a'+i))+" unique"+string(rune('a'+i)), "area"+string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 6 {
		res := domain.CheckResult{ItemID: a.ID, Check: "flaky" + string(rune('a'+i)), Passed: true, Flaky: true}
		if _, err := e.svc.emit(e.ctx, e.p, domain.EvCheckCompleted, a.ID, "checks", domain.Payload{Check: &res}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.svc.RecordViolation(e.ctx, "demo", "x", "", "Bash", "r", "no", "sudo"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AskOwner(e.ctx, "demo", owner(), domain.Question{Context: "c", Decision: "d", Options: []domain.Option{{Label: "a", Recommended: true}, {Label: "b"}}, IfNoAnswer: "n"}); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(25 * time.Hour)
	m, err := e.svc.Metrics(e.ctx, "demo", owner(), 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if m.IntegratedByKind["feature"] != 2 || m.ReworkAvg != 3.5 || m.ReworkCauses["review"] != 7 || m.TokensByModel["fake/big"] != 450 ||
		m.TokensByRole["reviewer"] != 40 || m.TokensByItem[a.ID] != 490 || m.StepSeconds["review"] != 180 || m.ProposalsMade != 6 ||
		m.Violations != 1 || m.FlakyQuarantined != 6 || m.ExploreReads["cards"] != 7 || m.FixedCostRatio["cards"] != 3 || m.WaitingOwnerMax != "25h0m0s" {
		t.Fatalf("metrics %+v", m)
	}
	if m.CacheHitRate < 0.6 || m.CacheHitRate > 0.7 {
		t.Fatalf("cache hit rate %v", m.CacheHitRate)
	}
	want := []string{"rework average", "of rework comes from step review", "proposals approved", "waits for the owner", "flaky checks", "policy violations", "too small"}
	all := strings.Join(m.Alerts, "\n")
	for _, w := range want {
		if !strings.Contains(all, w) {
			t.Errorf("alert %q missing in\n%s", w, all)
		}
	}
	// Each number past its limit is named, for the panel to mark it.
	if got := strings.Join(m.Flags, ","); got != "rework_avg,proposals,waiting_owner,flaky,violations" {
		t.Errorf("flags %q", got)
	}
	if _, err := e.svc.Metrics(e.ctx, "demo", Actor{}, time.Hour); err == nil {
		t.Fatal("no scope")
	}
	if _, err := e.svc.Metrics(e.ctx, "nope", owner(), time.Hour); err == nil {
		t.Fatal("unknown project")
	}
	e.store.FailQuery = errStore
	if _, err := e.svc.Metrics(e.ctx, "demo", owner(), time.Hour); err == nil {
		t.Fatal("store error")
	}
	e.store.FailQuery = nil

	d, err := e.svc.BuildDigest(e.ctx, "demo")
	if err != nil || len(d.Alerts) == 0 || len(d.Proposals) != 6 {
		t.Fatal(d, err)
	}
	if txt := d.Text(); !strings.Contains(txt, "Integrated (0)") || !strings.Contains(txt, "Alert: ") || !strings.Contains(txt, "Proposals in") {
		t.Fatal(txt)
	}
	est, err := e.svc.EstimateItem(e.ctx, "demo", owner(), b.ID)
	if err != nil || est.BasedOnItems != 1 || est.Tokens != 490 || est.Hours != 2 {
		t.Fatalf("estimate %+v %v", est, err)
	}
}

var errStore = &storeErr{}

type storeErr struct{}

func (*storeErr) Error() string { return "disk gone" }

func TestStatusTimelineDigestEstimate(t *testing.T) {
	e := newEnv(t)
	g, _, _ := e.svc.SetGoal(e.ctx, "demo", owner(), domain.Goal{Name: "MVP", DoneWhen: "done"})
	it := e.ready("simple", "Cards", "cards")
	e.create(NewItem{Title: "a draft"})
	e.start(it.ID)
	e.mustDeliver(it.ID, domain.Delivery{Summary: "built the list", Criteria: []domain.CriterionMark{{ID: "C1", Met: true, Evidence: "ok"}}})
	e.checks.Script("main", testkit.Fail)
	e.drive(it.ID)
	if err := e.svc.Journal(e.ctx, "demo", agent("implementer", it.ID, "implement"), "retrying the list"); err != nil {
		t.Fatal(err)
	}
	e.usage(domain.TokenUsage{Agent: "implementer-m1-1", Provider: "fake", Model: "m", ItemID: it.ID, Input: 10, Duration: time.Second})
	if _, err := e.review(it.ID, approveReview("x")); err == nil {
		t.Fatal("review at implement")
	}
	e.svc.sessionState(e.ctx, e.p, slot("implementer"), &Assignment{Item: it, Step: domain.StepDef{Name: "implement"}}, "working", "Edit", "fake-1", "fake", "fake/m")
	e.svc.emitEphemeral(e.p, domain.EvMachineUpdated, "m2", "barnraise", domain.Payload{MachineI: &domain.Machine{Name: "m2", LastSeen: e.clock.Now().Add(-time.Hour)}})
	e.account("fake-1")
	if _, err := e.svc.Propose(e.ctx, "demo", owner(), prop("idea", "x")); err != nil {
		t.Fatal(err)
	}

	st, err := e.svc.GetStatus(e.ctx, "demo", owner())
	if err != nil || st.Goal == nil || st.Goal.ID != g.ID || st.Progress == nil || st.Counts["in_progress"] != 1 || st.Counts["draft"] != 1 ||
		len(st.Queue) != 1 || len(st.Agents) != 1 || len(st.Accounts) != 1 || len(st.Machines) != 1 || st.Machines[0].Health != "red" || st.Proposals != 1 {
		t.Fatalf("status %+v %v", st, err)
	}
	if !strings.Contains(strings.Join(st.Alerts, ";"), "machine m2 is out") {
		t.Fatal(st.Alerts)
	}
	if _, err := e.svc.GetStatus(e.ctx, "demo", Actor{}); err == nil {
		t.Fatal("no scope")
	}
	if _, err := e.svc.GetStatus(e.ctx, "nope", owner()); err == nil {
		t.Fatal("unknown project")
	}

	tl, err := e.svc.Timeline(e.ctx, "demo", owner(), strings.ToLower(it.ID))
	if err != nil {
		t.Fatal(err)
	}
	var sums []string
	for _, te := range tl {
		sums = append(sums, te.Type+": "+te.Summary)
	}
	joined := strings.Join(sums, "\n")
	for _, te := range tl {
		if te.Payload.Item != nil {
			t.Fatalf("%s carries the whole item: the timeline sends only what the event says", te.Type)
		}
		if strings.HasPrefix(te.Type, "item.") && te.Summary == "" {
			t.Fatalf("%s lost its summary", te.Type)
		}
	}
	for _, w := range []string{"item.created: ready at step", "delivered DONE: built the list", "check main passed=false", "rework check → implement", "retrying the list", "used 10 tokens on fake/m"} {
		if !strings.Contains(joined, w) {
			t.Errorf("timeline lacks %q:\n%s", w, joined)
		}
	}
	if _, err := e.svc.Timeline(e.ctx, "demo", owner(), "x"); err == nil {
		t.Fatal("bad id")
	}
	if _, err := e.svc.Timeline(e.ctx, "demo", Actor{}, it.ID); err == nil {
		t.Fatal("no scope")
	}
	if _, err := e.svc.Timeline(e.ctx, "nope", owner(), it.ID); err == nil {
		t.Fatal("unknown project")
	}
	e.store.FailQuery = errStore
	if _, err := e.svc.Timeline(e.ctx, "demo", owner(), it.ID); err == nil {
		t.Fatal("store error")
	}
	e.store.FailQuery = nil

	// Digest: integrated today, waiting for the owner, accounts.
	e.integrateFake(it, 0, "")
	post := e.ready("post", "Post", "blog")
	e.start(post.ID)
	e.mustDeliver(post.ID, domain.Delivery{})
	e.drive(post.ID)
	if err := e.svc.ReportUsage(e.ctx, "demo", owner(), "fake-1", domain.Usage{FiveHourPct: 40, WeeklyPct: 20, WeeklyResets: e.clock.Now().Add(48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	d, err := e.svc.BuildDigest(e.ctx, "demo")
	if err != nil || len(d.Integrated) != 1 || len(d.Waiting) != 1 || !strings.Contains(d.Goal, "MVP: ") || len(d.Accounts) != 1 || !strings.Contains(d.Accounts[0], "resets") {
		t.Fatalf("digest %+v %v", d, err)
	}
	txt := d.Text()
	for _, w := range []string{"Goal: MVP", "Integrated (1)", "Waiting for you", "Accounts: fake-1"} {
		if !strings.Contains(txt, w) {
			t.Errorf("digest text lacks %q: %s", w, txt)
		}
	}
	if _, err := e.svc.BuildDigest(e.ctx, "nope"); err == nil {
		t.Fatal("unknown project")
	}
	e.svc.sendDigest(e.ctx, e.p)
	if k := e.notes.Kinds(); k[len(k)-1] != "digest" {
		t.Fatal(k)
	}

	// Estimate: no history in the kind, from the target size; with history, the median.
	est, err := e.svc.EstimateItem(e.ctx, "demo", owner(), post.ID)
	if err != nil || est.BasedOnItems != 0 || est.Hours != 4 {
		t.Fatalf("estimate %+v %v", est, err)
	}
	other := e.ready("simple", "More cards", "cards")
	est, err = e.svc.EstimateItem(e.ctx, "demo", owner(), other.ID)
	if err != nil || est.BasedOnItems != 1 || est.Tokens != 10 || est.Account != "" {
		t.Fatalf("estimate %+v %v", est, err)
	}
	if _, err := e.svc.EstimateItem(e.ctx, "demo", owner(), "I-404"); err == nil {
		t.Fatal("not found")
	}
	if _, err := e.svc.EstimateItem(e.ctx, "demo", owner(), "x"); err == nil {
		t.Fatal("bad id")
	}
	if _, err := e.svc.EstimateItem(e.ctx, "demo", Actor{}, other.ID); err == nil {
		t.Fatal("no scope")
	}
	if _, err := e.svc.EstimateItem(e.ctx, "nope", owner(), other.ID); err == nil {
		t.Fatal("unknown project")
	}
}

func TestEstimateWeekShare(t *testing.T) {
	e := newEnv(t, withCfg(func(c *domain.ProjectConfig) {}))
	if _, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: "claude-1", Provider: "fake"}); err != nil {
		t.Fatal(err)
	}
	// EstimateItem looks for a claude account: register the provider name.
	e.svc.d.Providers["claude"] = e.prov
	if _, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: "claude-2", Provider: "claude", Usage: domain.Usage{WeeklyPct: 50}}); err != nil {
		t.Fatal(err)
	}
	done := e.ready("feature", "done", "x")
	e.integrateFake(done, 0, "")
	e.usage(domain.TokenUsage{Provider: "claude", Model: "opus", ItemID: done.ID, Input: 1000})
	e.usage(domain.TokenUsage{Provider: "claude", Model: "opus", ItemID: "", Input: 1000})
	it := e.ready("feature", "next", "x")
	est, err := e.svc.EstimateItem(e.ctx, "demo", owner(), it.ID)
	if err != nil || est.Account != "claude-2" || est.WeekPct != 25 {
		t.Fatalf("estimate %+v %v", est, err)
	}
	// The estimate reads only the usage events, and adds them up as Metrics does.
	m, err := e.svc.Metrics(e.ctx, "demo", owner(), 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	u, err := e.svc.usageTokens(e.ctx, "demo", 30*24*time.Hour)
	if err != nil || !maps.Equal(u.TokensByItem, m.TokensByItem) || !maps.Equal(u.TokensByModel, m.TokensByModel) {
		t.Fatalf("usage %+v, metrics %+v %+v (%v)", u, m.TokensByItem, m.TokensByModel, err)
	}
	e.store.FailQuery = errStore
	if _, err := e.svc.EstimateItem(e.ctx, "demo", owner(), it.ID); err == nil {
		t.Fatal("store error")
	}
	e.store.FailQuery = nil
}

func TestAccounts(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: "x"})
	wantErr(t, err, ErrInvalid)
	_, err = e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: "x", Provider: "gemini"})
	wantErr(t, err, ErrInvalid)
	_, err = e.svc.UpsertAccount(e.ctx, "demo", Actor{Kind: ActorAgent, Scopes: []string{ScopeDeliver}}, domain.Account{ID: "x", Provider: "fake"})
	wantErr(t, err, ErrForbidden)
	_, err = e.svc.UpsertAccount(e.ctx, "nope", owner(), domain.Account{ID: "x", Provider: "fake"})
	wantErr(t, err, ErrUnknownProj)
	acc, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: "fake-1", Provider: "fake"})
	if err != nil || acc.Status != domain.AccountOK || acc.AddedAt.IsZero() {
		t.Fatal(acc, err)
	}
	wantErr(t, e.svc.ReportUsage(e.ctx, "demo", agent("implementer", "", ""), "fake-2", domain.Usage{}), ErrForbidden)
	wantErr(t, e.svc.ReportUsage(e.ctx, "demo", owner(), "fake-9", domain.Usage{}), ErrNotFound)
	wantErr(t, e.svc.ReportUsage(e.ctx, "nope", owner(), "fake-1", domain.Usage{}), ErrUnknownProj)
	// The agent may report its own account; at 100% the account is limited until the reset.
	resets := e.clock.Now().Add(2 * time.Hour)
	if err := e.svc.ReportUsage(e.ctx, "demo", agent("implementer", "", ""), "fake-1", domain.Usage{FiveHourPct: 100, FiveHourResets: resets}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(e.types("fake-1"), domain.EvAccountLimitReached) {
		t.Fatal(e.types("fake-1"))
	}
	snap := e.p.Snapshot()
	if snap.Accounts["fake-1"].Available(e.clock.Now()) {
		t.Fatal("limited until the reset")
	}
	e.svc.notifyAccountsExhausted(e.ctx, e.p)
	if n := e.notes.Sent(); len(n) != 1 || n[0].Kind != "accounts_exhausted" || !strings.Contains(n[0].Body, "Next reset") {
		t.Fatalf("notifications %+v", n)
	}
	e.svc.markAccountLimited(e.ctx, e.p, "fake-1", time.Time{})
	e.svc.markAccountLimited(e.ctx, e.p, "fake-404", time.Time{})
	if a := e.p.Snapshot().Accounts["fake-1"]; a.Status != domain.AccountLimited || !a.Usage.FiveHourResets.Equal(e.clock.Now().Add(time.Hour)) {
		t.Fatalf("limited %+v", a)
	}
	e.clock.Advance(3 * time.Hour)
	if err := e.svc.ReportUsage(e.ctx, "demo", owner(), "fake-1", domain.Usage{FiveHourPct: 5}); err != nil {
		t.Fatal(err)
	}
	if a := e.p.Snapshot().Accounts["fake-1"]; a.Status != domain.AccountOK {
		t.Fatalf("back after the reset %+v", a)
	}
	e.svc.notifyAccountsExhausted(e.ctx, e.p) // one is available: nothing to say
	if len(e.notes.Sent()) != 1 {
		t.Fatal("no new notification")
	}
	empty := newEnv(t)
	empty.svc.notifyAccountsExhausted(empty.ctx, empty.p)
}

func TestMachineLeave(t *testing.T) {
	e := newEnv(t)
	held := e.ready("simple", "held", "x")
	queued := e.ready("simple", "queued", "x")
	e.start(held.ID)
	e.start(queued.ID)
	e.mustDeliver(queued.ID, domain.Delivery{})
	e.drive(queued.ID)
	if !e.svc.holdHousekeeping(e.ctx, e.p) {
		t.Fatal("housekeeping lease")
	}
	got, err := e.svc.MachineLeave(e.ctx, "demo", owner())
	if err != nil || strings.Join(got, ",") != held.ID+","+queued.ID {
		t.Fatal(got, err)
	}
	for _, id := range got {
		it := e.wantStatus(id, domain.StatusReady, "")
		if it.Claim != nil || e.backend.Holder(claimKey(id)) != "" {
			t.Fatalf("%s still held", id)
		}
	}
	if e.backend.Holder(HousekeepingKey) != "" {
		t.Fatal("housekeeping lease released")
	}
	if m := e.p.Snapshot().Machines["m1"]; m == nil || !m.Left {
		t.Fatal("machine left")
	}
	_, err = e.svc.MachineLeave(e.ctx, "demo", Actor{Kind: ActorAgent, Scopes: []string{ScopeDeliver}})
	wantErr(t, err, ErrForbidden)
	_, err = e.svc.MachineLeave(e.ctx, "nope", owner())
	wantErr(t, err, ErrUnknownProj)
}

// peer is a second machine on the same arbiter, fed by replication of e's log.
func (e *env) peer(name string) *env {
	e.t.Helper()
	p := newEnv(e.t, withMachine(name), withBackend(e.backend))
	p.sync(e)
	return p
}

// sync ingests every event of other's log, like the event bus would.
func (e *env) sync(other *env) {
	e.t.Helper()
	for _, ev := range other.store.Events() {
		if err := e.svc.Ingest(e.ctx, ev); err != nil {
			e.t.Fatalf("ingest %s: %v", ev.Type, err)
		}
	}
}

func TestIngestIsIdempotent(t *testing.T) {
	e := newEnv(t)
	it := e.ready("feature", "replicated", "x")
	p := e.peer("m2")
	if got := p.item(it.ID); got.Title != "replicated" {
		t.Fatal(got)
	}
	n := len(p.store.Events())
	p.sync(e)
	if len(p.store.Events()) != n {
		t.Fatal("replaying the same events stores nothing")
	}
	own := p.store.Events()[0]
	own.Machine = "m2"
	if err := p.svc.Ingest(p.ctx, own); err != nil {
		t.Fatal(err)
	}
	foreign := own
	foreign.Machine, foreign.Project, foreign.ID = "m3", "other-project", "x-1"
	if err := p.svc.Ingest(p.ctx, foreign); err != nil {
		t.Fatal("events of projects not driven here are ignored")
	}
	p.store.FailAppend = errStore
	foreign.Project, foreign.ID = "demo", "x-2"
	if err := p.svc.Ingest(p.ctx, foreign); err == nil {
		t.Fatal("store error")
	}
}

func TestHousekeepingLeaseAndDeadItems(t *testing.T) {
	e := newEnv(t, withNotifyClock())
	it := e.ready("simple", "work on m1", "x")
	free := e.ready("simple", "claim lost", "x")
	e.start(it.ID)
	e.start(free.ID)
	if !e.svc.holdHousekeeping(e.ctx, e.p) {
		t.Fatal("m1 takes housekeeping")
	}
	if !e.svc.holdHousekeeping(e.ctx, e.p) {
		t.Fatal("m1 renews housekeeping")
	}
	p := e.peer("m2")
	if p.svc.holdHousekeeping(p.ctx, p.p) {
		t.Fatal("m2 cannot take a live housekeeping lease")
	}
	// m1's claim on free vanished from the arbiter: m2 claims it at once.
	e.svc.releaseLease(e.ctx, e.p, free.ID)
	p.svc.takeOverDeadItems(p.ctx, p.p)
	if got := p.item(free.ID); got.Claim == nil || got.Claim.Machine != "m2" {
		t.Fatalf("free %+v", got.Claim)
	}
	if got := p.item(it.ID); got.Claim.Machine != "m1" {
		t.Fatal("a live claim stays")
	}
	// m1 stops renewing: after the ttl, by m2's own clock, m2 takes over.
	p.clock.Advance(4 * time.Second)
	p.svc.takeOverDeadItems(p.ctx, p.p)
	got := p.item(it.ID)
	if got.Claim == nil || got.Claim.Machine != "m2" || !strings.Contains(p.types(it.ID), domain.EvItemClaimed) {
		t.Fatalf("taken over %+v", got.Claim)
	}
	eventually(t, "takeover note", func() bool {
		for _, n := range p.notes.Sent() {
			if n.Kind == "digest" && strings.Contains(n.Body, "Machine m1 has been out") {
				return true
			}
		}
		return false
	})
	if !p.svc.holdHousekeeping(p.ctx, p.p) {
		t.Fatal("m2 takes over housekeeping after the ttl")
	}
	if e.svc.holdHousekeeping(e.ctx, e.p) {
		t.Fatal("m1 lost housekeeping")
	}
	// A restart of m2 adopts its own lease.
	p.p.mu.Lock()
	delete(p.p.leases, HousekeepingKey)
	p.p.mu.Unlock()
	if !p.svc.holdHousekeeping(p.ctx, p.p) {
		t.Fatal("adopt own lease")
	}
	// Offline: keep the duty.
	e.backend.SetOffline(true)
	if !p.svc.holdHousekeeping(p.ctx, p.p) {
		t.Fatal("offline keeps the duty")
	}
	p.svc.takeOverDeadItems(p.ctx, p.p)
	e.backend.SetOffline(false)
	none := newEnv(t, withoutArbiter())
	if !none.svc.holdHousekeeping(none.ctx, none.p) {
		t.Fatal("without an arbiter this machine does everything")
	}
	none.svc.takeOverDeadItems(none.ctx, none.p)
}

func withNotifyClock() opt { return func(*envOpts) {} }

func TestAddProjectRestoresLeases(t *testing.T) {
	store := testkit.NewStore()
	backend := testkit.NewArbiterBackend()
	e := newEnv(t, withStore(store), withBackend(backend), withKnowledge())
	it := e.ready("simple", "survives", "x")
	e.start(it.ID)
	if err := e.svc.Pause(e.ctx, "demo", owner(), "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(e.ctx, domain.Event{ID: "junk", Project: "demo", Type: "x", Data: []byte("{bad")}); err != nil {
		t.Fatal(err)
	}
	again := newEnv(t, withStore(store), withBackend(backend))
	if _, ok := again.p.leases[it.ID]; !ok {
		t.Fatal("the restarted daemon re-reads its leases")
	}
	if !again.p.Snapshot().Paused || again.item(it.ID).Claim == nil {
		t.Fatal("the state is replayed")
	}
	// Indexing runs in the background, off the path of the command.
	eventually(t, "the items to be indexed", func() bool { return e.know.Indexed() > 0 })
	if err := again.svc.ReloadConfig("demo", again.cfg); err != nil {
		t.Fatal(err)
	}
	bad := *again.cfg
	bad.Name = ""
	if err := again.svc.ReloadConfig("demo", &bad); err == nil {
		t.Fatal("invalid config")
	}
	if err := again.svc.ReloadConfig("nope", again.cfg); err == nil {
		t.Fatal("unknown project")
	}
	if _, err := again.svc.AddProject(again.ctx, &bad, ProjectPorts{}); err == nil {
		t.Fatal("invalid project")
	}
	store.FailQuery = errStore
	if _, err := again.svc.AddProject(again.ctx, again.cfg, ProjectPorts{}); err == nil {
		t.Fatal("store error")
	}
	if got := again.svc.Projects(); len(got) != 1 || got[0] != "demo" {
		t.Fatal(got)
	}
	if again.svc.Machine() != "m1" || again.svc.Deps().Version != "test" || again.svc.Hub() == nil || again.svc.Tokens() == nil {
		t.Fatal("accessors")
	}
	if _, err := again.svc.Project(""); err != nil {
		t.Fatal("the only project")
	}
	_ = ports.Lease{}
}
