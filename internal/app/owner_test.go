package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

func TestFocusPreviewApplyClear(t *testing.T) {
	e := newEnv(t)
	if _, _, err := e.svc.SetGoal(e.ctx, "demo", owner(), domain.Goal{Name: "MVP", DoneWhen: "done"}); err != nil {
		t.Fatal(err)
	}
	other := e.create(NewItem{Title: "other", Kind: "simple", Area: "wallet", Criteria: crit(ears)})
	early := e.ready("feature", "early", "profile")
	cards := e.ready("feature", "cards", "cards")
	e.start(other.ID)

	_, err := e.svc.Focus(e.ctx, "demo", owner(), domain.Focus{}, true)
	wantErr(t, err, ErrInvalid)
	_, err = e.svc.Focus(e.ctx, "demo", owner(), domain.Focus{Area: "nothing-here"}, true)
	if !strings.Contains(err.Error(), "areas with live items: cards, profile, wallet") {
		t.Fatal(err)
	}
	_, err = e.svc.Focus(e.ctx, "demo", owner(), domain.Focus{Area: "cards", Share: 120}, true)
	wantErr(t, err, ErrInvalid)
	_, err = e.svc.Focus(e.ctx, "demo", owner(), domain.Focus{Items: []string{"P-1"}}, true)
	wantErr(t, err, ErrInvalid)
	_, err = e.svc.Focus(e.ctx, "demo", Actor{Kind: ActorAgent, Scopes: []string{ScopeDeliver}}, domain.Focus{Area: "cards"}, true)
	wantErr(t, err, ErrForbidden)
	eff, err := e.svc.Focus(e.ctx, "demo", owner(), domain.Focus{Area: "A-cards"}, true)
	if err != nil || eff.FocusItems != 1 || eff.Share != 80 || len(eff.ParkedElsewhere) != 1 || eff.ParkedElsewhere[0] != other.ID {
		t.Fatalf("preview %+v %v", eff, err)
	}
	if e.p.Snapshot().Focus != nil {
		t.Fatal("a preview changes nothing")
	}
	if _, err := e.svc.Focus(e.ctx, "demo", owner(), domain.Focus{Area: "cards", Share: 60}, false); err != nil {
		t.Fatal(err)
	}
	st, _ := e.svc.GetStatus(e.ctx, "demo", owner())
	if st.Focus == nil || st.Focus.Name != "cards" || st.FocusAll != 1 {
		t.Fatalf("status focus %+v", st.Focus)
	}
	// Focus items go first within the goal lane.
	e.p.mu.Lock()
	cands := e.svc.candidatesLocked(e.p, slot("implementer"))
	e.p.mu.Unlock()
	if len(cands) != 2 || cands[0].ID != cards.ID || cands[1].ID != early.ID {
		t.Fatalf("candidates %v", cands)
	}
	e.start(cards.ID)
	// The item outside the focus finishes its step and waits (parked).
	e.mustDeliver(other.ID, domain.Delivery{})
	parked := e.item(other.ID)
	if !parked.Parked || parked.Status != domain.StatusReady || parked.Claim != nil {
		t.Fatalf("parked %+v", parked)
	}
	if info, _ := e.arb.Inspect(e.ctx, claimKey(other.ID)); info.Held {
		t.Fatal("a parked item releases its claim")
	}
	e.p.mu.Lock()
	cands = e.svc.candidatesLocked(e.p, slot("implementer"))
	e.p.mu.Unlock()
	if len(cands) != 1 || cands[0].ID != early.ID {
		t.Fatalf("parked items wait during the focus: %v", cands)
	}
	if err := e.svc.ClearFocus(e.ctx, "demo", owner()); err != nil {
		t.Fatal(err)
	}
	if e.item(other.ID).Parked || e.p.Snapshot().Focus != nil {
		t.Fatal("clearing the focus unparks")
	}
	wantErr(t, e.svc.ClearFocus(e.ctx, "demo", Actor{}), ErrForbidden)
	wantErr(t, e.svc.ClearFocus(e.ctx, "nope", owner()), ErrUnknownProj)
	_, err = e.svc.Focus(e.ctx, "nope", owner(), domain.Focus{Area: "cards"}, true)
	wantErr(t, err, ErrUnknownProj)
	// A focus on items by id is named after them.
	if _, err := e.svc.Focus(e.ctx, "demo", owner(), domain.Focus{Items: []string{strings.TrimPrefix(early.ID, "I-")}}, false); err != nil {
		t.Fatal(err)
	}
	if f := e.p.Snapshot().Focus; f.Name != early.ID || f.Items[0] != early.ID {
		t.Fatalf("focus %+v", f)
	}
}

func TestFocusSplitsTheRemainingCapacity(t *testing.T) {
	e := newEnv(t)
	if _, _, err := e.svc.SetGoal(e.ctx, "demo", owner(), domain.Goal{Name: "MVP", DoneWhen: "done"}); err != nil {
		t.Fatal(err)
	}
	for range 6 {
		e.ready("feature", "goal", "cards")
	}
	for range 6 {
		e.ready("bug", "bug", "")
	}
	if _, err := e.svc.Focus(e.ctx, "demo", owner(), domain.Focus{Area: "cards", Share: 50}, false); err != nil {
		t.Fatal(err)
	}
	counts := map[domain.Lane]int{}
	for i := range 8 {
		a, err := e.svc.NextWork(e.ctx, "demo", Slot{Agent: "implementer-" + string(rune('a'+i)), Role: "implementer"})
		if err != nil {
			t.Fatal(err)
		}
		counts[a.Item.Lane]++
	}
	if counts[domain.LaneGoal] != 4 || counts[domain.LaneBugs] != 4 {
		t.Fatalf("50%% focus over 8 claims: %v", counts)
	}
}

func TestPauseResume(t *testing.T) {
	e := newEnv(t)
	wantErr(t, e.svc.Pause(e.ctx, "demo", Actor{Kind: ActorAgent, Scopes: []string{ScopeDeliver}}, "x"), ErrForbidden)
	wantErr(t, e.svc.Pause(e.ctx, "nope", owner(), "x"), ErrUnknownProj)
	if err := e.svc.Pause(e.ctx, "demo", owner(), "release freeze"); err != nil {
		t.Fatal(err)
	}
	st, _ := e.svc.GetStatus(e.ctx, "demo", owner())
	if !st.Paused || st.PausedWhy != "release freeze" || len(st.Alerts) == 0 || st.Alerts[0] != "paused: release freeze" {
		t.Fatalf("status %+v", st)
	}
	wantErr(t, e.svc.Resume(e.ctx, "demo", Actor{}), ErrForbidden)
	wantErr(t, e.svc.Resume(e.ctx, "nope", owner()), ErrUnknownProj)
	if err := e.svc.Resume(e.ctx, "demo", owner()); err != nil {
		t.Fatal(err)
	}
	if e.p.Snapshot().Paused {
		t.Fatal("still paused")
	}
}

func TestTakeoverHandback(t *testing.T) {
	e := newEnv(t, withGit())
	it := e.ready("simple", "Hand work", "x")
	dir, got, err := e.svc.Takeover(e.ctx, "demo", owner(), it.ID)
	if err != nil || got.Status != domain.StatusPaused || dir == "" {
		t.Fatalf("takeover %+v %v", got, err)
	}
	if info, _ := e.arb.Inspect(e.ctx, claimKey(it.ID)); !info.Held || info.Holder != "m1" {
		t.Fatal("a taken-over item is reserved")
	}
	testkit.WriteFile(t, dir, "by-hand.txt", "done by a person\n")
	_, err = e.svc.Handback(e.ctx, "demo", owner(), "I-404", "")
	wantErr(t, err, ErrNotFound)
	back, err := e.svc.Handback(e.ctx, "demo", owner(), it.ID, "fixed the layout")
	if err != nil || back.Status != domain.StatusInProgress || back.Step != "implement" || back.Claim == nil {
		t.Fatalf("handback %+v %v", back, err)
	}
	if log := testkit.RunGit(t, e.repo.Root, "--git-dir", e.repo.Remote, "log", "--format=%s", back.Branch); !strings.Contains(log, it.ID+": changes by leandro") {
		t.Fatalf("the person's work is committed and pushed: %s", log)
	}
	_, err = e.svc.Handback(e.ctx, "demo", owner(), it.ID, "")
	wantErr(t, err, ErrConflict)
	inbox, _ := e.svc.Inbox(e.ctx, "demo", "x", "", it.ID, false)
	if len(inbox) != 1 || inbox[0].Text != "Owner worked on this item: fixed the layout" {
		t.Fatalf("inbox %+v", inbox)
	}
	for _, f := range []func() error{
		func() error { _, _, err := e.svc.Takeover(e.ctx, "demo", owner(), "bad"); return err },
		func() error { _, err := e.svc.Handback(e.ctx, "demo", owner(), "bad", ""); return err },
	} {
		wantErr(t, f(), ErrInvalid)
	}
	_, _, err = e.svc.Takeover(e.ctx, "demo", Actor{}, it.ID)
	wantErr(t, err, ErrForbidden)
	_, err = e.svc.Handback(e.ctx, "demo", Actor{}, it.ID, "")
	wantErr(t, err, ErrForbidden)
	_, _, err = e.svc.Takeover(e.ctx, "nope", owner(), it.ID)
	wantErr(t, err, ErrUnknownProj)
	_, err = e.svc.Handback(e.ctx, "nope", owner(), it.ID, "")
	wantErr(t, err, ErrUnknownProj)
}

func TestTakeoverGuards(t *testing.T) {
	e := newEnv(t)
	it := e.ready("simple", "x", "x")
	e.p.mu.Lock()
	e.p.state.Items[it.ID].Claim = &domain.Claim{Machine: "m2"}
	e.p.mu.Unlock()
	_, _, err := e.svc.Takeover(e.ctx, "demo", owner(), it.ID)
	wantErr(t, err, ErrConflict)
	done := e.ready("simple", "y", "x")
	if _, err := e.svc.CloseItem(e.ctx, "demo", owner(), done.ID, "obsolete", "", ""); err != nil {
		t.Fatal(err)
	}
	_, _, err = e.svc.Takeover(e.ctx, "demo", owner(), done.ID)
	wantErr(t, err, ErrConflict)
	// Without git the person works in the project root; handback starts the first step.
	fresh := e.ready("simple", "z", "x")
	dir, _, err := e.svc.Takeover(e.ctx, "demo", owner(), fresh.ID)
	if err != nil || dir != e.root {
		t.Fatal(dir, err)
	}
	back, err := e.svc.Handback(e.ctx, "demo", owner(), fresh.ID, "")
	if err != nil || back.Step != "implement" {
		t.Fatal(back, err)
	}
}

func TestRedo(t *testing.T) {
	e := newEnv(t)
	it := e.ready("simple", "x", "x")
	e.start(it.ID)
	e.mustDeliver(it.ID, domain.Delivery{})
	got, err := e.svc.Redo(e.ctx, "demo", owner(), it.ID, "implement", "try again")
	if err != nil || got.Step != "implement" || got.Status != domain.StatusInProgress {
		t.Fatal(got, err)
	}
	_, err = e.svc.Redo(e.ctx, "demo", owner(), it.ID, "nope", "")
	wantErr(t, err, ErrInvalid)
	_, err = e.svc.Redo(e.ctx, "demo", owner(), "bad", "implement", "")
	wantErr(t, err, ErrInvalid)
	_, err = e.svc.Redo(e.ctx, "demo", Actor{}, it.ID, "implement", "")
	wantErr(t, err, ErrForbidden)
	_, err = e.svc.Redo(e.ctx, "nope", owner(), it.ID, "implement", "")
	wantErr(t, err, ErrUnknownProj)
	e.p.mu.Lock()
	e.p.state.Items[it.ID].Status = domain.StatusIntegrated
	e.p.mu.Unlock()
	_, err = e.svc.Redo(e.ctx, "demo", owner(), it.ID, "implement", "")
	wantErr(t, err, ErrConflict)
}

func TestMessagesAndInbox(t *testing.T) {
	e := newEnv(t)
	it := e.ready("simple", "x", "x")
	cases := []struct {
		a   Actor
		m   domain.Message
		err error
	}{
		{owner(), domain.Message{To: "role:reviewer", Text: "  "}, ErrInvalid},
		{owner(), domain.Message{To: "role:reviewer", Text: strings.Repeat("x", domain.MaxMessageLen+1)}, ErrInvalid},
		{owner(), domain.Message{Text: "hi"}, ErrInvalid},
		{agent("implementer", "", ""), domain.Message{To: "role:reviewer", Text: "hi"}, ErrInvalid},
		{Actor{Kind: ActorAgent, Scopes: []string{ScopeDeliver}}, domain.Message{To: "x", Text: "hi"}, ErrForbidden},
	}
	for _, c := range cases {
		_, err := e.svc.SendMessage(e.ctx, "demo", c.a, c.m)
		wantErr(t, err, c.err)
	}
	_, err := e.svc.SendMessage(e.ctx, "nope", owner(), domain.Message{})
	wantErr(t, err, ErrUnknownProj)
	m, err := e.svc.SendMessage(e.ctx, "demo", agent("implementer", it.ID, "implement"), domain.Message{To: "role:reviewer", Text: " look at the cache "})
	if err != nil || m.ItemID != it.ID || m.From != "implementer-m1-1" || m.Text != "look at the cache" {
		t.Fatal(m, err)
	}
	if _, err := e.svc.SendMessage(e.ctx, "demo", owner(), domain.Message{To: "reviewer-m1-1", Text: "direct"}); err != nil {
		t.Fatal(err)
	}
	got, _ := e.svc.Inbox(e.ctx, "demo", "reviewer-m1-1", "reviewer", "", true)
	if len(got) != 2 {
		t.Fatalf("inbox %+v", got)
	}
	if again, _ := e.svc.Inbox(e.ctx, "demo", "reviewer-m1-1", "reviewer", "", true); len(again) != 0 {
		t.Fatal("delivered messages are not repeated")
	}
	if _, err := e.svc.Inbox(e.ctx, "nope", "", "", "", false); err == nil {
		t.Fatal("unknown project")
	}
	if err := e.svc.Journal(e.ctx, "demo", agent("implementer", it.ID, "implement"), "left a note"); err != nil {
		t.Fatal(err)
	}
	if e.svc.lastJournal(e.p, it.ID) != "left a note" || e.svc.lastJournal(e.p, "I-404") != "" {
		t.Fatal("journal")
	}
	wantErr(t, e.svc.Journal(e.ctx, "demo", Actor{}, "x"), ErrForbidden)
	wantErr(t, e.svc.Journal(e.ctx, "nope", owner(), "x"), ErrUnknownProj)
	if err := e.svc.RecordViolation(e.ctx, "demo", "implementer-m1-1", it.ID, "Bash", "deny_commands", "gh secret", "gh secret list"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "violation notification", func() bool { return strings.Contains(strings.Join(e.notes.Kinds(), ","), "policy_violation") })
	if err := e.svc.RecordViolation(e.ctx, "nope", "a", "", "", "", "", ""); err == nil {
		t.Fatal("unknown project")
	}
}

func prop(title, area string) domain.Proposal {
	return domain.Proposal{Title: title, Problem: "the " + title + " problem happens often", Evidence: "log", Impact: "slow", Size: "2h", Area: area, GoalLink: "speed"}
}

func TestProposals(t *testing.T) {
	e := newEnv(t)
	ag := agent("implementer", "", "")
	_, err := e.svc.Propose(e.ctx, "demo", ag, domain.Proposal{Title: "x"})
	wantErr(t, err, ErrInvalid)
	_, err = e.svc.Propose(e.ctx, "demo", Actor{Kind: ActorAgent, Scopes: []string{ScopeDeliver}}, prop("x", "a"))
	wantErr(t, err, ErrForbidden)
	_, err = e.svc.Propose(e.ctx, "nope", ag, prop("x", "a"))
	wantErr(t, err, ErrUnknownProj)
	first, err := e.svc.Propose(e.ctx, "demo", ag, prop("cache quotes", "A-cards"))
	if err != nil || first.Proposal.ID != "P-1" || first.Proposal.Area != "cards" || first.Proposal.Kind != "improvement" || first.Proposal.By != ag.Name {
		t.Fatal(first, err)
	}
	dup, err := e.svc.Propose(e.ctx, "demo", ag, prop("cache quotes", "cards"))
	if err != nil || dup.Duplicate != "P-1" || len(dup.Proposal.Comments) != 1 {
		t.Fatalf("duplicate %+v %v", dup, err)
	}
	it := e.create(NewItem{Title: "slow wallet balance", Body: "the slow wallet balance problem happens often", Area: "wallet", Criteria: crit(ears)})
	onItem, err := e.svc.Propose(e.ctx, "demo", ag, prop("slow wallet balance", "wallet"))
	if err != nil || onItem.Duplicate != it.ID || onItem.Proposal != nil {
		t.Fatalf("duplicate of an item %+v %v", onItem, err)
	}
	if _, err := e.svc.Propose(e.ctx, "demo", ag, prop("refactor login", "login")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Propose(e.ctx, "demo", ag, prop("compress images", "media")); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Propose(e.ctx, "demo", ag, prop("one too many", "misc"))
	wantErr(t, err, ErrConflict) // 3 per agent per day
	if _, err := e.svc.Propose(e.ctx, "demo", owner(), domain.Proposal{Title: "Export to PDF", Problem: "people print", Evidence: "emails", Impact: "users", Size: "6h", Area: "export", GoalLink: "growth", Kind: "feature"}); err != nil {
		t.Fatal("the owner has no daily limit:", err)
	}
	open, groups, err := e.svc.ListProposals("demo", owner(), false)
	if err != nil || len(open) != 4 || len(groups) == 0 {
		t.Fatal(open, groups, err)
	}
	if _, _, err := e.svc.ListProposals("demo", Actor{}, false); err == nil {
		t.Fatal("no scope")
	}
	if _, _, err := e.svc.ListProposals("nope", owner(), false); err == nil {
		t.Fatal("unknown project")
	}

	analyst := agent("analyst", "", "")
	_, err = e.svc.DecideProposal(e.ctx, "demo", analyst, "P-4", "approve", "")
	wantErr(t, err, ErrForbidden) // features are the owner's
	got, err := e.svc.DecideProposal(e.ctx, "demo", analyst, "p-1", "approve", "good")
	if err != nil || got.Status != domain.ProposalApproved || got.ItemID == "" {
		t.Fatal(got, err)
	}
	if it := e.item(got.ItemID); it.Status != domain.StatusDraft || it.Kind != "improvement" || !strings.Contains(it.Body, "From proposal P-1") {
		t.Fatalf("approved proposal item %+v", it)
	}
	_, err = e.svc.DecideProposal(e.ctx, "demo", analyst, "P-1", "reject", "")
	wantErr(t, err, ErrConflict)
	later, err := e.svc.DecideProposal(e.ctx, "demo", owner(), "P-2", "later", "next month")
	if err != nil || later.Status != domain.ProposalLater || !later.LaterUntil.Equal(e.clock.Now().Add(7*24*time.Hour)) {
		t.Fatal(later, err)
	}
	if err := e.svc.Approve(e.ctx, "demo", owner(), "P-2", "delivery", true, ""); err == nil {
		t.Fatal("a proposal is approved as a proposal")
	}
	if err := e.svc.Approve(e.ctx, "demo", owner(), "P-2", "", false, "no"); err != nil {
		t.Fatal(err)
	}
	if e.p.Snapshot().Proposals["P-2"].Status != domain.ProposalRejected {
		t.Fatal("rejected through approve")
	}
	feat, err := e.svc.DecideProposal(e.ctx, "demo", owner(), "P-4", "approve", "")
	if err != nil || e.item(feat.ItemID).Kind != "feature" {
		t.Fatal(feat, err)
	}
	_, err = e.svc.DecideProposal(e.ctx, "demo", owner(), "P-3", "maybe", "")
	wantErr(t, err, ErrInvalid)
	_, err = e.svc.DecideProposal(e.ctx, "demo", owner(), "P-99", "approve", "")
	wantErr(t, err, ErrNotFound)
	_, err = e.svc.DecideProposal(e.ctx, "demo", Actor{Kind: ActorAgent, Scopes: []string{ScopeDeliver}}, "P-3", "approve", "")
	wantErr(t, err, ErrForbidden)
	_, err = e.svc.DecideProposal(e.ctx, "nope", owner(), "P-3", "approve", "")
	wantErr(t, err, ErrUnknownProj)

	for _, kind := range []string{"bug", "self_improvement"} {
		p, err := e.svc.Propose(e.ctx, "demo", owner(), domain.Proposal{Title: kind + " idea " + kind, Problem: "unique " + kind + " text", Evidence: "e", Impact: "i", Size: "1h", Area: "z" + kind, GoalLink: "g", Kind: kind, Priority: domain.P1})
		if err != nil {
			t.Fatal(err)
		}
		d, err := e.svc.DecideProposal(e.ctx, "demo", owner(), p.Proposal.ID, "approve", "")
		if err != nil || e.item(d.ItemID).Kind != kind {
			t.Fatalf("%s: %+v %v", kind, d, err)
		}
	}

	// Expiry: open proposals not decided in 14 days close with the reason.
	e.clock.Advance(15 * 24 * time.Hour)
	if n := e.svc.expireProposals(e.ctx, e.p); n != 1 {
		t.Fatalf("expired %d", n)
	}
	if p := e.p.Snapshot().Proposals["P-3"]; p.Status != domain.ProposalExpired || !strings.Contains(p.Reason, "not approved within") {
		t.Fatalf("expired %+v", p)
	}
}

func TestTweaksBecomeAnItem(t *testing.T) {
	e := newEnv(t)
	_, _, err := e.svc.AddTweak(e.ctx, "demo", owner(), " ", "x")
	wantErr(t, err, ErrInvalid)
	_, _, err = e.svc.AddTweak(e.ctx, "demo", Actor{Kind: ActorAgent, Scopes: []string{ScopeDeliver}}, "cards", "x")
	wantErr(t, err, ErrForbidden)
	_, _, err = e.svc.AddTweak(e.ctx, "nope", owner(), "cards", "x")
	wantErr(t, err, ErrUnknownProj)
	for i := 1; i <= 4; i++ {
		e.clock.Advance(time.Second)
		tw, it, err := e.svc.AddTweak(e.ctx, "demo", owner(), "cards", "fix spacing "+string(rune('0'+i)))
		if err != nil || it != nil || !strings.HasPrefix(tw.ID, "T-") {
			t.Fatal(tw, it, err)
		}
	}
	e.clock.Advance(time.Second)
	_, it, err := e.svc.AddTweak(e.ctx, "demo", owner(), "A-cards", "fix spacing 5")
	if err != nil || it == nil || it.Title != "Tweaks in cards (5)" || len(it.Checklist) != 5 || it.Kind != "improvement" || it.Status != domain.StatusDraft {
		t.Fatalf("tweak item %+v %v", it, err)
	}
	for _, tw := range e.p.Snapshot().Tweaks {
		if tw.ItemID != it.ID {
			t.Fatalf("tweak %+v not attached", tw)
		}
	}
}

func TestGoalsAndInventory(t *testing.T) {
	e := newEnv(t)
	_, _, err := e.svc.SetGoal(e.ctx, "demo", owner(), domain.Goal{DoneWhen: "x"})
	wantErr(t, err, ErrInvalid)
	_, _, err = e.svc.SetGoal(e.ctx, "demo", owner(), domain.Goal{Name: "x"})
	wantErr(t, err, ErrInvalid)
	_, _, err = e.svc.SetGoal(e.ctx, "demo", Actor{Kind: ActorAgent, Scopes: []string{ScopeDeliver}}, domain.Goal{Name: "x", DoneWhen: "y"})
	wantErr(t, err, ErrForbidden)
	_, _, err = e.svc.SetGoal(e.ctx, "nope", owner(), domain.Goal{Name: "x", DoneWhen: "y"})
	wantErr(t, err, ErrUnknownProj)

	testkit.WriteFile(t, e.root, "docs/proto/login-screen.html", "<html>")
	testkit.WriteFile(t, e.root, "docs/proto/home_feed.html", "<html>")
	testkit.WriteFile(t, e.root, "docs/proto/.hidden", "x")
	testkit.WriteFile(t, e.root, "docs/proto/settings/index.html", "<html>")
	testkit.WriteFile(t, e.root, "docs/todo.md", "# Todo\n- export csv\n\n* import csv\n")
	g, items, err := e.svc.SetGoal(e.ctx, "demo", owner(), domain.Goal{Name: "Prototype", DoneWhen: "every screen", Inventory: "screens docs/proto"})
	if err != nil || !g.Active || len(items) != 3 {
		t.Fatalf("screens %+v %d %v", g, len(items), err)
	}
	var titles []string
	for _, it := range items {
		titles = append(titles, it.Title)
		if it.GoalID != g.ID || it.Lane != domain.LaneGoal || it.Status != domain.StatusDraft {
			t.Fatalf("inventory item %+v", it)
		}
	}
	if strings.Join(titles, "|") != "Screen: home feed|Screen: login screen|Screen: settings" {
		t.Fatal(titles)
	}
	_, items, err = e.svc.SetGoal(e.ctx, "demo", owner(), domain.Goal{Name: "Todo", DoneWhen: "all lines", Inventory: "lines docs/todo.md"})
	if err != nil || len(items) != 3 || items[1].Title != "export csv" {
		t.Fatalf("lines %+v %v", items, err)
	}
	_, items, err = e.svc.SetGoal(e.ctx, "demo", owner(), domain.Goal{Name: "Files", DoneWhen: "x", Inventory: "files docs/proto/settings/index.html"})
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	for _, inv := range []string{"files docs/missing", "lines docs/missing.md", "tables x"} {
		if _, _, err := e.svc.SetGoal(e.ctx, "demo", owner(), domain.Goal{Name: "bad", DoneWhen: "x", Inventory: inv}); err == nil {
			t.Errorf("inventory %q accepted", inv)
		}
	}
	if g := e.p.Snapshot().ActiveGoal(); g.Name != "bad" {
		t.Fatalf("the newest goal is active: %s", g.Name)
	}
}

func TestGoalFinishesAndAsksForTheNext(t *testing.T) {
	e := newEnv(t)
	g, _, err := e.svc.SetGoal(e.ctx, "demo", owner(), domain.Goal{Name: "Small", DoneWhen: "one item"})
	if err != nil {
		t.Fatal(err)
	}
	it := e.create(NewItem{Title: "only one", Kind: "simple", Criteria: crit(ears)})
	e.svc.finishGoalIfDone(e.ctx, e.p)
	if !e.p.Snapshot().Goals[g.ID].Active {
		t.Fatal("not done yet")
	}
	if _, err := e.svc.CloseItem(e.ctx, "demo", owner(), it.ID, "done_by", "I-0", ""); err != nil {
		t.Fatal(err)
	}
	e.p.mu.Lock()
	cur := e.p.state.Items[it.ID]
	cur.Status, cur.IntegratedAt = domain.StatusIntegrated, e.clock.Now()
	e.p.mu.Unlock()
	e.svc.finishGoalIfDone(e.ctx, e.p)
	snap := e.p.Snapshot()
	if snap.Goals[g.ID].Active || snap.ActiveGoal() != nil {
		t.Fatal("the goal finished")
	}
	qs := snap.OpenQuestions()
	if len(qs) != 1 || qs[0].Decision != "What is the next goal?" {
		t.Fatalf("questions %+v", qs)
	}
	if _, _, err := e.svc.SetGoal(e.ctx, "demo", owner(), domain.Goal{Name: "Next", DoneWhen: "x"}); err != nil {
		t.Fatal(err)
	}
	if len(e.p.Snapshot().OpenQuestions()) != 0 {
		t.Fatal("a new goal answers the question")
	}
	e.svc.finishGoalIfDone(e.ctx, e.p) // no items in the new goal: nothing to finish
}

func TestInventoryOfMissingDirectoryIsInvalid(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.inventory(e.p, "screens "+filepath.Join("no", "dir"))
	wantErr(t, err, ErrInvalid)
	os.MkdirAll(filepath.Join(e.root, "empty"), 0o755)
	if out, err := e.svc.inventory(e.p, "screens empty"); err != nil || len(out) != 0 {
		t.Fatal(out, err)
	}
}
