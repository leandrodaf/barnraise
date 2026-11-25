package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

func noScope() Actor { return Actor{Kind: ActorAgent, Name: "nobody"} }

func TestFindWithoutIndexSearchesIDsAndText(t *testing.T) {
	e := newEnv(t)
	a := e.create(NewItem{Title: "Export CSV", Body: "the wallet statement", Kind: "simple", Area: "wallet", Criteria: crit(ears)})
	e.create(NewItem{Title: "Unrelated", Kind: "simple", Area: "other", Criteria: crit(ears)})
	for _, q := range []string{"export", "STATEMENT", strings.ToLower(a.ID)} {
		hits, err := e.svc.Find(e.ctx, "demo", owner(), q, ports.SearchFilters{}, 0)
		if err != nil || len(hits) != 1 || hits[0].ID != a.ID || hits[0].Kind != "item" || hits[0].Area != "wallet" {
			t.Fatalf("find %q: %+v %v", q, hits, err)
		}
	}
	all, _ := e.svc.Find(e.ctx, "demo", owner(), "", ports.SearchFilters{}, 1)
	if len(all) != 1 {
		t.Fatalf("the limit is respected: %d", len(all))
	}
	if _, err := e.svc.Find(e.ctx, "demo", noScope(), "x", ports.SearchFilters{}, 0); !errors.Is(err, ErrForbidden) {
		t.Fatalf("without scope: %v", err)
	}
	if _, err := e.svc.Find(e.ctx, "nope", owner(), "x", ports.SearchFilters{}, 0); err == nil {
		t.Fatal("unknown project")
	}
}

func TestFindUsesTheIndex(t *testing.T) {
	e := newEnv(t, withKnowledge())
	e.know.Docs = []ports.Doc{
		{ID: "D-1", Kind: "decision", Title: "Money as integer cents", Body: "never floats", Areas: []string{"wallet"}},
		{ID: "S-1", Kind: "standard", Title: "Money formatting", Body: "two decimals", Areas: []string{"wallet"}},
	}
	hits, err := e.svc.Find(e.ctx, "demo", owner(), "money", ports.SearchFilters{Kind: "decision"}, 5)
	if err != nil || len(hits) != 1 || hits[0].ID != "D-1" {
		t.Fatalf("hits %+v %v", hits, err)
	}
}

func TestGetResolvesEveryKindOfID(t *testing.T) {
	e := newEnv(t, withKnowledge())
	e.know.Docs = []ports.Doc{{ID: "D-7", Kind: "decision", Title: "Use sqlite"}}
	it := e.create(NewItem{Title: "Cards", Kind: "simple", Area: "x", Criteria: crit(ears)})
	got, err := e.svc.Get(e.ctx, "demo", owner(), strings.ToLower(it.ID))
	if gi, ok := got.(*domain.Item); err != nil || !ok || gi.ID != it.ID {
		t.Fatalf("item by lowercase id: %T %v", got, err)
	}
	got, err = e.svc.Get(e.ctx, "demo", owner(), "D-7")
	if d, ok := got.(*ports.Doc); err != nil || !ok || d.Title != "Use sqlite" {
		t.Fatalf("document: %T %v", got, err)
	}
	if _, err := e.svc.Get(e.ctx, "demo", owner(), "Z-404"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := e.svc.Get(e.ctx, "demo", noScope(), it.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("without scope: %v", err)
	}
	if _, err := e.svc.Get(e.ctx, "nope", owner(), it.ID); err == nil {
		t.Fatal("unknown project")
	}

	// A question is reachable by its id too.
	q := newEnv(t)
	w := q.ready("simple", "needs the owner", "x")
	q.start(w.ID)
	q.mustDeliver(w.ID, domain.Delivery{Status: domain.DeliveryBlocked, Reason: "which bank?"})
	qu := q.question(w.ID)
	got, err = q.svc.Get(q.ctx, "demo", owner(), qu.ID)
	if gq, ok := got.(*domain.Question); err != nil || !ok || gq.ID != qu.ID {
		t.Fatalf("question: %T %v", got, err)
	}
}

func TestRelatedListsDependenciesAreaLinksAndCommits(t *testing.T) {
	g := &testkit.FakeGit{Commits: []ports.Commit{{SHA: "0123456789abcdef", Subject: "I-1: Cards"}}}
	e := newEnv(t, withKnowledge(), withFakeGit(g))
	base := e.create(NewItem{Title: "Cards", Kind: "simple", Area: "cards", Criteria: crit(ears),
		Anchors: domain.Anchors{Files: []string{"app/cards.go"}}})
	dep := e.create(NewItem{Title: "Cards filter", Kind: "simple", Area: "cards", Criteria: crit(ears), Deps: []string{base.ID}})
	e.create(NewItem{Title: "Elsewhere", Kind: "simple", Area: "other", Criteria: crit(ears)})
	e.know.LinkList = []ports.Link{{From: base.ID, To: "D-1", Kind: "decision"}}

	rel, err := e.svc.Related(e.ctx, "demo", owner(), strings.ToLower(base.ID))
	if err != nil {
		t.Fatal(err)
	}
	dependents, _ := rel["dependents"].([]string)
	same, _ := rel["same_area"].([]string)
	if len(dependents) != 1 || !strings.HasPrefix(dependents[0], dep.ID) {
		t.Fatalf("dependents %v", rel["dependents"])
	}
	if len(same) != 1 || !strings.Contains(same[0], "Cards filter") {
		t.Fatalf("same area %v", rel["same_area"])
	}
	if links, _ := rel["links"].([]ports.Link); len(links) != 1 {
		t.Fatalf("links %v", rel["links"])
	}
	if commits, _ := rel["commits"].([]string); len(commits) != 1 || !strings.Contains(commits[0], "I-1: Cards") {
		t.Fatalf("commits %v", rel["commits"])
	}
	if !g.Called("Log --all --grep " + domain.CommitTrailer(base.ID)) {
		t.Fatalf("commits are searched by trailer: %v", g.Calls())
	}
	if a, _ := rel["anchors"].(domain.Anchors); len(a.Files) != 1 {
		t.Fatalf("anchors %v", rel["anchors"])
	}
	// An id that is not an item still answers with its links.
	rel, err = e.svc.Related(e.ctx, "demo", owner(), "D-1")
	if err != nil || rel["dependents"] != nil || len(rel["links"].([]ports.Link)) != 1 {
		t.Fatalf("document: %v %v", rel, err)
	}
	if _, err := e.svc.Related(e.ctx, "demo", noScope(), base.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("without scope: %v", err)
	}
	if _, err := e.svc.Related(e.ctx, "nope", owner(), base.ID); err == nil {
		t.Fatal("unknown project")
	}
}

func TestWhyExplainsALine(t *testing.T) {
	g := &testkit.FakeGit{BlameSHA: "abc1234"}
	e := newEnv(t, withKnowledge(), withFakeGit(g))
	it := e.create(NewItem{Title: "Cards", Kind: "simple", Area: "cards", Criteria: crit(ears),
		Anchors: domain.Anchors{Decisions: []string{"D-1"}}})
	g.Commits = []ports.Commit{{SHA: "abc1234", Subject: it.ID + ": Cards", Author: "t", Time: time.Unix(1000, 0),
		Trailers: map[string]string{"Barnraise-Item": it.ID}}}
	e.know.RulesBy = map[string][]ports.Doc{"app/": {{ID: "S-1", Title: "Go style"}}}

	out, err := e.svc.Why(e.ctx, "demo", owner(), "app/cards.go:12")
	if err != nil {
		t.Fatal(err)
	}
	if out["commit"] != "abc1234" || out["item"] != it.ID || out["item_title"] != "Cards" || out["subject"] != it.ID+": Cards" {
		t.Fatalf("why %v", out)
	}
	if d, _ := out["decisions"].([]string); len(d) != 1 || d[0] != "D-1" {
		t.Fatalf("decisions %v", out["decisions"])
	}
	if r, _ := out["rules"].([]string); len(r) != 1 || r[0] != "S-1 Go style" {
		t.Fatalf("rules %v", out["rules"])
	}
	if !g.Called("Blame app/cards.go 12") {
		t.Fatalf("calls %v", g.Calls())
	}
	// Without a line number the first line is used.
	if _, err := e.svc.Why(e.ctx, "demo", owner(), "app/cards.go"); err != nil || !g.Called("Blame app/cards.go 1") {
		t.Fatalf("default line: %v %v", err, g.Calls())
	}
	for _, bad := range []string{"app/cards.go:zero", "app/cards.go:0", "app/cards.go:-3"} {
		if _, err := e.svc.Why(e.ctx, "demo", owner(), bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	g.BlameErr = errors.New("no such path")
	if _, err := e.svc.Why(e.ctx, "demo", owner(), "gone.go:1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("blame error: %v", err)
	}
	if _, err := e.svc.Why(e.ctx, "demo", noScope(), "a:1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("without scope: %v", err)
	}
	if _, err := e.svc.Why(e.ctx, "nope", owner(), "a:1"); err == nil {
		t.Fatal("unknown project")
	}
	if _, err := newEnv(t).svc.Why(e.ctx, "demo", owner(), "a:1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("without a repository: %v", err)
	}
}

func TestHistoryOfAPathAndOfASymbol(t *testing.T) {
	g := &testkit.FakeGit{}
	e := newEnv(t, withFakeGit(g))
	it := e.create(NewItem{Title: "Cards", Kind: "simple", Area: "cards", Criteria: crit(ears)})
	g.Commits = []ports.Commit{
		{SHA: "1111111111", Subject: it.ID + ": Cards", Time: time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC), Trailers: map[string]string{"Barnraise-Item": it.ID}},
		{SHA: "2222222222", Subject: "manual fix", Time: time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)},
	}
	h, err := e.svc.History(e.ctx, "demo", owner(), "app/cards.go")
	if err != nil || len(h) != 2 {
		t.Fatalf("history %v %v", h, err)
	}
	if h[0]["item"] != it.ID || h[0]["item_title"] != "Cards" || h[0]["date"] != "2026-03-04" || h[1]["item"] != nil {
		t.Fatalf("entries %v", h)
	}
	if !g.Called("Log -n 50 -- app/cards.go") {
		t.Fatalf("a path is searched as a path: %v", g.Calls())
	}
	if _, err := e.svc.History(e.ctx, "demo", owner(), "cards.ListCards"); err != nil || !g.Called("Log -n 50 -S ListCards") {
		t.Fatalf("a symbol is searched by its last name: %v %v", err, g.Calls())
	}
	if _, err := e.svc.History(e.ctx, "demo", owner(), "ListCards"); err != nil || !g.Called("Log -n 50 -S ListCards") {
		t.Fatalf("plain symbol: %v", err)
	}
	if _, err := e.svc.History(e.ctx, "demo", noScope(), "x"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("without scope: %v", err)
	}
	if _, err := e.svc.History(e.ctx, "nope", owner(), "x"); err == nil {
		t.Fatal("unknown project")
	}
	if _, err := newEnv(t).svc.History(e.ctx, "demo", owner(), "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("without a repository: %v", err)
	}
}

func TestRulesOfAPath(t *testing.T) {
	e := newEnv(t, withKnowledge())
	e.know.RulesBy = map[string][]ports.Doc{"app/": {{ID: "S-1", Title: "Go style"}}}
	docs, err := e.svc.Rules(e.ctx, "demo", owner(), "app/cards.go")
	if err != nil || len(docs) != 1 || docs[0].ID != "S-1" {
		t.Fatalf("rules %v %v", docs, err)
	}
	// Without an index the answer is the area that owns the path, when there is one.
	plain := newEnv(t, withCfg(func(c *domain.ProjectConfig) {
		c.Areas = map[string]domain.AreaDef{"wallet": {Paths: []string{"app/wallet/**"}}}
	}))
	docs, err = plain.svc.Rules(plain.ctx, "demo", owner(), "app/wallet")
	if err != nil || len(docs) != 1 || docs[0].ID != "area:wallet" {
		t.Fatalf("area fallback %v %v", docs, err)
	}
	docs, err = plain.svc.Rules(plain.ctx, "demo", owner(), "docs")
	if err != nil || docs != nil {
		t.Fatalf("no area: %v %v", docs, err)
	}
	if _, err := e.svc.Rules(e.ctx, "demo", noScope(), "x"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("without scope: %v", err)
	}
	if _, err := e.svc.Rules(e.ctx, "nope", owner(), "x"); err == nil {
		t.Fatal("unknown project")
	}
}

func TestReindexAfterIntegration(t *testing.T) {
	g := &testkit.FakeGit{Main: "feedface"}
	e := newEnv(t, withKnowledge(), withFakeGit(g))
	e.create(NewItem{Title: "Cards", Kind: "simple", Area: "cards", Criteria: crit(ears)})
	before := e.know.Indexed()
	if err := e.svc.Reindex(e.ctx, "demo", []string{"app/cards.go"}); err != nil {
		t.Fatal(err)
	}
	if r := e.know.Reindexed(); len(r) != 1 || r[0] != "demo@feedface:app/cards.go" {
		t.Fatalf("reindexed %v", r)
	}
	if e.know.Indexed() <= before {
		t.Fatal("the items are indexed again")
	}
	if err := e.svc.Reindex(e.ctx, "nope", nil); err == nil {
		t.Fatal("unknown project")
	}
	if err := newEnv(t).svc.Reindex(e.ctx, "demo", nil); err != nil {
		t.Fatalf("without an index it is a no-op: %v", err)
	}
}

func TestContextPackOrderAndBudget(t *testing.T) {
	e := newEnv(t, withKnowledge())
	e.know.Docs = []ports.Doc{
		{ID: "D-1", Kind: "decision", Title: "Money as cents", Body: "wallet amounts are integer cents", Areas: []string{"wallet"}},
		{ID: "S-1", Kind: "standard", Title: "Wallet errors", Body: "wallet errors are typed", Areas: []string{"wallet"}},
		{ID: "M-1", Kind: "memory", Title: "Flaky bank sandbox", Body: "the wallet bank sandbox times out on mondays", Areas: []string{"wallet"}},
		{ID: "S-9", Kind: "standard", Title: "Go style", Body: "gofmt always"},
	}
	e.know.RulesBy = map[string][]ports.Doc{"app/wallet/": {e.know.Docs[3]}}
	e.know.Syms = map[string][]ports.Symbol{"app/wallet/export.go": {
		{Name: "Export", File: "app/wallet/export.go", Line: 10, Signature: "func Export(w io.Writer) error"},
		{Name: "rows", File: "app/wallet/export.go", Line: 40},
	}}
	e.know.G = &testkit.Graph{CallersOf: map[string][]string{"Export": {"a", "b", "c", "d", "e", "f", "g"}}}
	sibling := e.create(NewItem{Title: "Wallet import", Kind: "simple", Area: "wallet", Criteria: crit(ears)})
	it := e.create(NewItem{Title: "Wallet export", Body: "Export the statement.", Kind: "simple", Area: "wallet", Criteria: crit(ears),
		Preserve: []string{"the old endpoint"}, Checklist: []string{"update the docs"},
		Anchors: domain.Anchors{Files: []string{"app/wallet/export.go"}, Decisions: []string{"D-1"}}})

	pack, err := e.svc.Context(e.ctx, "demo", owner(), strings.ToLower(it.ID), 100000)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	lastOrder := 0
	for _, p := range pack.Parts {
		ids = append(ids, p.ID)
		if p.Order < lastOrder {
			t.Fatalf("parts out of order: %v", ids)
		}
		lastOrder = p.Order
	}
	for _, want := range []string{it.ID, "S-9", "D-1", "S-1", "memories:wallet", "codemap:" + it.ID, "related:" + it.ID} {
		if !domain.Contains(ids, want) {
			t.Fatalf("part %s missing from %v", want, ids)
		}
	}
	text := pack.Render()
	for _, want := range []string{"Export the statement.", "What must not change:\n- the old endpoint", "Checklist:\n- update the docs", "Anchors: files [app/wallet/export.go]",
		"wallet amounts are integer cents", "- [M-1] Flaky bank sandbox", "app/wallet/export.go:10 func Export(w io.Writer) error ← a, b, c, d, e",
		"app/wallet/export.go:40 rows", "[" + sibling.ID + "] Wallet import"} {
		if !strings.Contains(text, want) {
			t.Fatalf("pack lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, ", f") {
		t.Fatal("at most five callers per symbol")
	}
	if pack.Tokens <= 0 || pack.Budget != 100000 || len(pack.Cut) != 0 {
		t.Fatalf("pack %d/%d cut %v", pack.Tokens, pack.Budget, pack.Cut)
	}

	// A tight budget keeps the item and says what was cut.
	small, err := e.svc.Context(e.ctx, "demo", owner(), it.ID, 120)
	if err != nil {
		t.Fatal(err)
	}
	if len(small.Parts) == 0 || small.Parts[0].ID != it.ID || len(small.Cut) == 0 {
		t.Fatalf("small pack parts %d cut %v", len(small.Parts), small.Cut)
	}
	if !strings.Contains(small.Render(), "cut to fit 120 tokens") {
		t.Fatal(small.Render())
	}
	// Budget 0 means the project's default.
	def, err := e.svc.Context(e.ctx, "demo", owner(), it.ID, 0)
	if err != nil || def.Budget != e.cfg.Budget.ContextTokens {
		t.Fatalf("default budget %d %v", def.Budget, err)
	}

	if _, err := e.svc.Context(e.ctx, "demo", owner(), "I-999", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing item: %v", err)
	}
	if _, err := e.svc.Context(e.ctx, "demo", owner(), "not an id", 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad id: %v", err)
	}
	if _, err := e.svc.Context(e.ctx, "demo", noScope(), it.ID, 0); !errors.Is(err, ErrForbidden) {
		t.Fatalf("without scope: %v", err)
	}
	if _, err := e.svc.Context(e.ctx, "nope", owner(), it.ID, 0); err == nil {
		t.Fatal("unknown project")
	}
}

func TestStepInstructionsPerStepKind(t *testing.T) {
	e := newEnv(t)
	it := &domain.Item{ID: "I-7", Kind: "planned"}
	for name, tc := range map[string]struct {
		st   domain.StepDef
		want string
	}{
		"refine":    {domain.StepDef{Name: RefineStep, Type: domain.StepAgent}, "`edit_item` tool on I-7"},
		"approval":  {domain.StepDef{Name: "approval", Type: domain.StepApproval}, "Approve the"},
		"review":    {domain.StepDef{Name: "review", Type: domain.StepAgent, Review: true}, "exactly one `review`"},
		"plan":      {domain.StepDef{Name: "plan", Type: domain.StepAgent, Output: "plan.md"}, "Write plan.md"},
		"reproduce": {domain.StepDef{Name: "reproduce", Type: domain.StepAgent, Output: "failing_test"}, "fails on main"},
		"implement": {domain.StepDef{Name: "implement", Type: domain.StepAgent}, domain.CommitTrailer("I-7")},
	} {
		if got := stepInstructions(e.p, it, tc.st); !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q lacks %q", name, got, tc.want)
		}
	}
	if got := stepInstructions(e.p, it, domain.StepDef{Name: RefineStep}); !strings.Contains(got, "main") {
		t.Fatalf("the refine step names the project checks: %s", got)
	}
}

func TestReviewerSeesDiffAndChecksNeverThePlan(t *testing.T) {
	g := &testkit.FakeGit{Main: "aaaaaaa", HeadSHA: "bbbbbbbbbbbb",
		DiffText: "diff --git a/app/x.go b/app/x.go\n+x\ndiff --git a/plan.md b/plan.md\n+secret reasoning\n"}
	e := newEnv(t, withFakeGit(g))
	it := e.create(NewItem{Title: "Reviewed", Kind: "reviewed", Area: "x", Criteria: crit(ears), Preserve: []string{"the public API"}})
	e.start(it.ID)
	e.mustDeliver(it.ID, domain.Delivery{})
	cur := e.wantStatus(it.ID, domain.StatusInProgress, "review")
	f, _ := e.cfg.FlowFor("reviewed")
	st, _ := f.Step("review")
	sp, err := e.svc.BuildStepPrompt(e.ctx, e.p, cur, st, "reviewer", "reviewer-m1-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Criteria", "What must not change:\n- the public API", "## Diff (main..", "app/x.go", "## This step"} {
		if !strings.Contains(sp.Prompt, want) {
			t.Fatalf("review prompt lacks %q:\n%s", want, sp.Prompt)
		}
	}
	if strings.Contains(sp.Prompt, "secret reasoning") {
		t.Fatal("the reviewer must never see plan.md")
	}
	if !strings.Contains(sp.System, "Barnraise conventions") {
		t.Fatal("system part")
	}
}

func TestApproverOfAPlanSeesThePlan(t *testing.T) {
	e := newEnv(t, withGit())
	it := e.create(NewItem{Title: "Planned", Kind: "planned", Area: "x", Criteria: crit(ears)})
	e.start(it.ID)
	f, _ := e.cfg.FlowFor("planned")
	st, _ := f.Step("approval")
	// The plan step produced nothing: the approver is told to send it back.
	sp, err := e.svc.BuildStepPrompt(e.ctx, e.p, e.item(it.ID), st, "analyst", "analyst-m1-1")
	if err != nil || !strings.Contains(sp.Prompt, "missing: the plan step produced no plan.md") {
		t.Fatalf("missing plan: %v\n%s", err, sp.Prompt)
	}
	e.workspace(it.ID, map[string]string{"plan.md": "# Plan\n1. add the endpoint\n"})
	sp, err = e.svc.BuildStepPrompt(e.ctx, e.p, e.item(it.ID), st, "analyst", "analyst-m1-1")
	if err != nil || !strings.Contains(sp.Prompt, "1. add the endpoint") || !strings.Contains(sp.Prompt, "Approve the plan") {
		t.Fatalf("plan approval: %v\n%s", err, sp.Prompt)
	}
	if strings.Contains(sp.Prompt, "## Diff") {
		t.Fatal("a plan approval shows the plan, not a diff")
	}
}

func TestImplementerPromptCarriesReworkJournalAndMessages(t *testing.T) {
	e := newEnv(t, withGit())
	it := e.create(NewItem{Title: "Reviewed", Kind: "reviewed", Area: "x", Criteria: crit(ears)})
	e.start(it.ID)
	e.workspace(it.ID, map[string]string{"plan.md": "# Plan\nstep one\n"})
	e.mustDeliver(it.ID, domain.Delivery{})
	if _, err := e.review(it.ID, reworkReview("the list is not sorted")); err != nil {
		t.Fatal(err)
	}
	cur := e.wantStatus(it.ID, domain.StatusInProgress, "implement")
	if err := e.svc.Journal(e.ctx, "demo", agent("implementer", it.ID, "implement"), "tried sorting in SQL"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SendMessage(e.ctx, "demo", owner(), domain.Message{To: "implementer-m1-1", ItemID: it.ID, Text: "sort by date, newest first"}); err != nil {
		t.Fatal(err)
	}
	f, _ := e.cfg.FlowFor("reviewed")
	st, _ := f.Step("implement")
	sp, err := e.svc.BuildStepPrompt(e.ctx, e.p, cur, st, "implementer", "implementer-m1-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"plan.md (the item's memory):", "step one", "Rework round 1", "the list is not sorted",
		"## Last journal entry\ntried sorting in SQL", "## Messages", "sort by date, newest first"} {
		if !strings.Contains(sp.Prompt, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, sp.Prompt)
		}
	}
}

// The owner reads the project's language: the agents are told to write their
// questions in it; an English project adds nothing.
func TestPromptAsksForTheOwnersLanguage(t *testing.T) {
	e := newEnv(t, withCfg(func(c *domain.ProjectConfig) { c.Language = "pt-BR" }))
	it := e.ready("simple", "Cards", "cards")
	f, _ := e.cfg.FlowFor("simple")
	st, _ := f.Step("implement")
	sp, err := e.svc.BuildStepPrompt(e.ctx, e.p, it, st, "implementer", "implementer-m1-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sp.System, "The owner reads Brazilian Portuguese") || !strings.Contains(sp.System, "`ask_owner`") {
		t.Fatalf("no language rule in the prompt:\n%s", sp.System)
	}
	if ownerLanguageRule("en") != "" || ownerLanguageRule("") != "" {
		t.Fatal("an English project needs no rule")
	}
}
