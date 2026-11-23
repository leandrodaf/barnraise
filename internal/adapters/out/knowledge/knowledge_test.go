package knowledge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/sqlite"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

var now = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

var tree = map[string]string{
	".barnraise/INDEX.md":        "# Index\nWhere things are: standards per area, decisions in decisions/.\n",
	".barnraise/constitution.md": "---\ntype: constitution\n---\n# Constitution\nNever commit secrets.\n\n## Money\nAlways integer cents.\n",
	".barnraise/glossary.md":     "# Glossary\nCard: a payment card shown in the wallet screen; valores em centavos.\n",
	".barnraise/areas.yaml": `cards:
  paths: [app/cards/, web/]
  screens: [CardsScreen]
  contracts: [api/cards.yaml]
  standard: standards/cards.md
payments:
  paths: ["pay/**"]
`,
	".barnraise/standards/cards.md": "---\nid: standard:cards\ntype: standard\nupdated_at: 2026-11-01\n---\nCards screens use the shared CardList component.\n\n## Layout\nTwo columns on tablets.\n",
	".barnraise/decisions/D-4-float.md": `---
id: D-4
type: decision
status: active
areas: [A-payments]
---
Money is stored as float dollars.
`,
	".barnraise/decisions/D-12-valores-em-centavos.md": `---
id: D-12
type: decision
status: active
areas: [A-payments, A-cards]
supersedes: D-4
links: [G-3]
updated_at: 2026-11-20
---
Money is stored and sent as integer cents; never float.

## Why
Rounding errors in the cartões screen.
`,
	".barnraise/memories/M-301-emulador.md": "---\nid: M-301\ntype: gotcha\nareas: [cards]\nreview_by: 2026-01-01\n---\nEmulator needs 4 GB of memory for the cartões flow.\n",
	".barnraise/memories/M-302.md":          "---\ntype: convention\nareas: [cards]\nreview_by: 2027-06-01\n---\nOs cartões aparecem em ordem de vencimento.\n",
	".barnraise/roles/implementer.md":       "# Implementer\nWrites the code for one item, following the plan.\n",
	".barnraise/goals/G-3.md":               "---\nstatus: active\n---\n# Mobile app like the mockup\nShip the mobile app matching the mockup.\n",
	"app/cards/CardsScreen.kt": `package app.cards

class CardsScreen(private val repo: Repo) {
    fun render() {
        val cards = loadCards()
        formatCents(cards.size)
    }

    private fun formatCents(n: Int): String = n.toString()
}

fun loadCards(): List<Card> {
    return listOf()
}
`,
	"pay/service.go": `package pay

// PaymentService charges cards.
type PaymentService struct{ fee int }

// Charge takes money from a card.
func (p *PaymentService) Charge(cents int) error {
	return validate(cents)
}

func validate(cents int) error {
	if cents <= 0 {
		return nil
	}
	return nil
}

func helper(s *PaymentService) {
	_ = s.Charge(10)
}
`,
	"web/cards.ts": `export function fetchCards(): Promise<Card[]> {
  return loadCards();
}

export class CardStore {
  load(): void {
    fetchCards();
  }
}

export const sumCards = (xs: number[]) => xs.length;
`,
	"py/util.py": `class Money:
    def cents(self):
        return int(self.value * 100)

def to_cents(v):
    return Money().cents()
`,
	"rs/lib.rs": `pub struct Ledger;

impl Ledger {
    pub fn post(&self) {
        audit();
    }
}

fn audit() {}
`,
	"ios/Wallet.swift": `struct Wallet {
    func pay() {
        charge()
    }
}

func charge() {}
`,
	"lib/cart.dart": `class Cart {
  void add(int item) {
    total();
  }
}

int total() {
  return 0;
}
`,
	"java/Bank.java": `public class Bank {
    public void deposit(int x) {
        log(x);
    }

    private static void log(int x) {
    }
}
`,
	"node_modules/dep/index.js": "function ignored() {}\n",
	"build/gen.go":              "package gen\nfunc Generated() {}\n",
	"README.txt":                "not code\n",
}

func writeTree(t testing.TB, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// a binary file with a code extension is skipped
	os.WriteFile(filepath.Join(root, "pay", "blob.go"), []byte("package pay\x00\x01\x02func Hidden() {}"), 0o644)
}

func setup(t testing.TB) (*Index, string) {
	t.Helper()
	st, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ix, err := New(st.DB())
	if err != nil {
		t.Fatal(err)
	}
	ix.SetClock(func() time.Time { return now })
	root := t.TempDir()
	writeTree(t, root, tree)
	if err := ix.Reindex(context.Background(), "p", root, "c1", nil); err != nil {
		t.Fatal(err)
	}
	return ix, root
}

func ids(hits []ports.SearchHit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, h.ID)
	}
	return out
}

func symNames(t *testing.T, ix *Index, file string) []string {
	t.Helper()
	syms, err := ix.Symbols(context.Background(), "p", file)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range syms {
		out = append(out, s.Name)
	}
	return out
}

func TestSymbolsPerLanguage(t *testing.T) {
	ix, _ := setup(t)
	want := map[string][]string{
		"pay/service.go":           {"PaymentService", "PaymentService.Charge", "validate", "helper"},
		"app/cards/CardsScreen.kt": {"CardsScreen", "CardsScreen.render", "CardsScreen.formatCents", "loadCards"},
		"web/cards.ts":             {"fetchCards", "CardStore", "CardStore.load", "sumCards"},
		"py/util.py":               {"Money", "Money.cents", "to_cents"},
		"rs/lib.rs":                {"Ledger", "Ledger.post", "audit"},
		"ios/Wallet.swift":         {"Wallet", "Wallet.pay", "charge"},
		"lib/cart.dart":            {"Cart", "Cart.add", "total"},
		"java/Bank.java":           {"Bank", "Bank.deposit", "Bank.log"},
	}
	for f, w := range want {
		if got := symNames(t, ix, f); !slices.Equal(got, w) {
			t.Errorf("%s: got %v want %v", f, got, w)
		}
	}
	for _, f := range []string{"node_modules/dep/index.js", "build/gen.go", "pay/blob.go"} {
		if got := symNames(t, ix, f); len(got) != 0 {
			t.Errorf("%s should be skipped: %v", f, got)
		}
	}
	syms, _ := ix.Symbols(context.Background(), "p", "pay/service.go")
	if syms[1].Signature != "func (p *PaymentService) Charge(cents int) error" || syms[1].Kind != "method" || syms[1].Line != 7 {
		t.Errorf("go signature: %+v", syms[1])
	}
}

func TestCallers(t *testing.T) {
	ix, _ := setup(t)
	g := ix.GraphFor("p")
	cases := map[string][]string{
		"loadCards":             {"CardsScreen.render", "fetchCards"},
		"PaymentService.Charge": {"helper"},
		"validate":              {"PaymentService.Charge"},
		"audit":                 {"Ledger.post"},
		"Money.cents":           {"to_cents"},
		"charge":                {"Wallet.pay"},
		"total":                 {"Cart.add"},
		"Bank.log":              {"Bank.deposit"},
		"fetchCards":            {"CardStore.load"},
	}
	for sym, want := range cases {
		if got := g.Callers(sym); !slices.Equal(got, want) {
			t.Errorf("Callers(%s) = %v want %v", sym, got, want)
		}
	}
	if got := ix.Graph().Callers("sym:pay/service.go#validate"); !slices.Equal(got, []string{"PaymentService.Charge"}) {
		t.Errorf("global graph: %v", got)
	}
	if got := g.SymbolsInFile("rs/lib.rs"); !slices.Equal(got, []string{"Ledger", "Ledger.post", "audit"}) {
		t.Errorf("SymbolsInFile: %v", got)
	}
	// FindImpacted through the graph: a change in pay/service.go reaches callers.
	items := []*domain.Item{{ID: "I-9", Status: domain.StatusReady, Anchors: domain.Anchors{Symbols: []string{"helper"}}}}
	imp := domain.FindImpacted(domain.Change{Kind: "integration", Files: []string{"pay/service.go"}}, items, g)
	if len(imp) != 1 {
		t.Errorf("impact: %+v", imp)
	}
}

func TestIncrementalReindex(t *testing.T) {
	ix, root := setup(t)
	ctx := context.Background()
	stamp := func(p string) int64 {
		var n int64
		ix.db.QueryRow(`SELECT indexed FROM file_hashes WHERE project = 'p' AND path = ?`, p).Scan(&n)
		return n
	}
	before := stamp("web/cards.ts")
	later := now.Add(time.Hour)
	ix.SetClock(func() time.Time { return later })

	// full walk with nothing changed reprocesses nothing
	if err := ix.Reindex(ctx, "p", root, "c2", nil); err != nil {
		t.Fatal(err)
	}
	if stamp("web/cards.ts") != before || stamp("pay/service.go") != before {
		t.Fatal("unchanged files were reprocessed")
	}

	src := tree["pay/service.go"] + "\nfunc Refund(p *PaymentService) { p.Charge(-1) }\n"
	os.WriteFile(filepath.Join(root, "pay/service.go"), []byte(src), 0o644)
	os.Remove(filepath.Join(root, "py/util.py"))
	// web/cards.ts changes on disk but is not listed: it must stay as is
	os.WriteFile(filepath.Join(root, "web/cards.ts"), []byte("export function other() {}\n"), 0o644)
	if err := ix.Reindex(ctx, "p", root, "c3", []string{"pay/service.go", filepath.Join(root, "py/util.py")}); err != nil {
		t.Fatal(err)
	}
	if got := symNames(t, ix, "pay/service.go"); !slices.Contains(got, "Refund") {
		t.Errorf("new symbol missing: %v", got)
	}
	if got := symNames(t, ix, "py/util.py"); len(got) != 0 {
		t.Errorf("deleted file still indexed: %v", got)
	}
	if _, err := ix.Get(ctx, "p", "sym:py/util.py#Money"); err == nil {
		t.Error("deleted symbol doc still there")
	}
	if got := symNames(t, ix, "web/cards.ts"); !slices.Contains(got, "fetchCards") || stamp("web/cards.ts") != before {
		t.Errorf("unlisted file reprocessed: %v", got)
	}
	if stamp("pay/service.go") != later.UnixNano() {
		t.Error("changed file not reprocessed")
	}
	if got := ix.GraphFor("p").Callers("PaymentService.Charge"); !slices.Equal(got, []string{"Refund", "helper"}) {
		t.Errorf("callers after reindex: %v", got)
	}
	d, err := ix.Get(ctx, "p", "Refund")
	if err != nil || d.Meta["commit"] != "c3" {
		t.Fatalf("commit freshness: %+v %v", d, err)
	}
	if c, _, _ := ix.IndexedCommit(ctx, "p"); c != "c3" {
		t.Errorf("indexed commit %q", c)
	}
	// full walk picks up the unlisted change
	if err := ix.Reindex(ctx, "p", root, "c4", nil); err != nil {
		t.Fatal(err)
	}
	if got := symNames(t, ix, "web/cards.ts"); !slices.Equal(got, []string{"other"}) {
		t.Errorf("full walk: %v", got)
	}
	// deleting a decision file removes the doc and its links
	os.Remove(filepath.Join(root, ".barnraise/decisions/D-12-valores-em-centavos.md"))
	if err := ix.Reindex(ctx, "p", root, "c5", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Get(ctx, "p", "D-12"); err == nil {
		t.Error("deleted decision still indexed")
	}
	hits, _ := ix.Search(ctx, "p", "float", ports.SearchFilters{}, 5)
	if !slices.Contains(ids(hits), "D-4") {
		t.Errorf("D-4 should be live again: %v", ids(hits))
	}
}

func TestDocsParsed(t *testing.T) {
	ix, _ := setup(t)
	ctx := context.Background()
	cases := map[string]string{"index": "index", "constitution": "constitution", "glossary": "glossary", "standard:cards": "standard",
		"role:implementer": "role", "G-3": "goal", "D-12": "decision", "M-302": "memory", "A-cards": "area"}
	for id, kind := range cases {
		d, err := ix.Get(ctx, "p", id)
		if err != nil {
			t.Errorf("%s: %v", id, err)
			continue
		}
		if d.Kind != kind {
			t.Errorf("%s kind %s", id, d.Kind)
		}
	}
	d, _ := ix.Get(ctx, "p", "d-12")
	if d.Meta["summary"] != "Money is stored and sent as integer cents; never float." || d.Status != "active" ||
		!slices.Equal(d.Areas, []string{"A-payments", "A-cards"}) || !d.Updated.Equal(time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)) ||
		!slices.Contains(d.Links, "D-4") || !slices.Contains(d.Links, "G-3") || d.Meta["commit"] != "c1" || d.SourceSHA == "" {
		t.Errorf("D-12: %+v", d)
	}
	g, _ := ix.Get(ctx, "p", "G-3")
	if g.Title != "Mobile app like the mockup" || g.Meta["summary"] != "Ship the mobile app matching the mockup." {
		t.Errorf("G-3: %+v", g)
	}
	m, _ := ix.Get(ctx, "p", "M-301")
	if m.Meta["type"] != "gotcha" || m.ReviewBy.IsZero() {
		t.Errorf("M-301: %+v", m)
	}
	if d, err := ix.Get(ctx, "p", ".barnraise/roles/implementer.md"); err != nil || d.ID != "role:implementer" {
		t.Errorf("by path: %+v %v", d, err)
	}
	if d, err := ix.Get(ctx, "p", "PaymentService.Charge"); err != nil || d.ID != "sym:pay/service.go#PaymentService.Charge" ||
		!strings.Contains(d.Body, "return validate(cents)") || d.Meta["callers"] != "helper" {
		t.Errorf("by symbol: %+v %v", d, err)
	}
	if d, err := ix.Get(ctx, "p", "app/cards/CardsScreen.kt:12"); err != nil || d.Kind != "file" || !slices.Contains(d.Areas, "A-cards") {
		t.Errorf("file doc: %+v %v", d, err)
	}
	if _, err := ix.Get(ctx, "p", "nope"); err != ErrNotFound {
		t.Errorf("missing: %v", err)
	}
}

func TestSearchAccents(t *testing.T) {
	ix, _ := setup(t)
	for _, q := range []string{"cartoes", "cartões", "CARTÕES"} {
		hits, err := ix.Search(context.Background(), "p", q, ports.SearchFilters{}, 10)
		if err != nil {
			t.Fatal(err)
		}
		got := ids(hits)
		if !slices.Contains(got, "M-302") || !slices.Contains(got, "D-12") {
			t.Errorf("%q: %v", q, got)
		}
		for _, h := range hits {
			if h.Layer != LayerText || h.Tokens == 0 || h.Title == "" {
				t.Errorf("hit %+v", h)
			}
		}
	}
	// FTS syntax in user text is harmless
	if _, err := ix.Search(context.Background(), "p", `"cart* OR NEAR( -x ^`, ports.SearchFilters{}, 10); err != nil {
		t.Errorf("unsafe query: %v", err)
	}
}

func TestSearchExactFirstAndFusion(t *testing.T) {
	ix, _ := setup(t)
	ctx := context.Background()
	hits, err := ix.Search(ctx, "p", "D-12 centavos", ports.SearchFilters{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 2 || hits[0].ID != "D-12" || hits[0].Layer != LayerExact || hits[0].Kind != "decision" || hits[0].Area != "A-payments" {
		t.Fatalf("exact first: %+v", hits)
	}
	// G-3 matches only exactly; D-12 and the glossary only by text. The
	// exact layer weighs twice, so G-3 leads, then the text ranking.
	hits, _ = ix.Search(ctx, "p", "G-3 centavos", ports.SearchFilters{}, 10)
	got := ids(hits)
	text, _ := ix.Search(ctx, "p", "centavos", ports.SearchFilters{}, 10)
	want := domain.RRF([]string{"G-3"}, []string{"G-3"}, ids(text))
	if !slices.Equal(got, want) || got[0] != "G-3" {
		t.Errorf("fusion: got %v want %v", got, want)
	}
	for i := 1; i < len(hits); i++ {
		if hits[i].Score > hits[i-1].Score {
			t.Errorf("scores not descending: %+v", hits)
		}
	}
	// a doc found both exactly and by text beats one found only exactly
	hits, _ = ix.Search(ctx, "p", "A-cards G-3 cards", ports.SearchFilters{}, 10)
	if ids(hits)[0] != "A-cards" {
		t.Errorf("both layers first: %v", ids(hits))
	}
	// symbol names and paths are exact hits
	hits, _ = ix.Search(ctx, "p", "Charge", ports.SearchFilters{}, 5)
	if hits[0].ID != "sym:pay/service.go#PaymentService.Charge" || hits[0].Layer != LayerExact {
		t.Errorf("symbol: %+v", hits)
	}
	hits, _ = ix.Search(ctx, "p", "app/cards/CardsScreen.kt", ports.SearchFilters{}, 5)
	if hits[0].ID != "file:app/cards/CardsScreen.kt" {
		t.Errorf("path: %+v", hits)
	}
	hits, _ = ix.Search(ctx, "p", "standard:cards", ports.SearchFilters{}, 5)
	if hits[0].ID != "standard:cards" {
		t.Errorf("standard id: %+v", hits)
	}
}

func TestSearchHidesSupersededAndExpired(t *testing.T) {
	ix, _ := setup(t)
	ctx := context.Background()
	hits, _ := ix.Search(ctx, "p", "float", ports.SearchFilters{}, 10)
	if slices.Contains(ids(hits), "D-4") || !slices.Contains(ids(hits), "D-12") {
		t.Errorf("superseded shown: %v", ids(hits))
	}
	hits, _ = ix.Search(ctx, "p", "D-4", ports.SearchFilters{}, 10)
	if slices.Contains(ids(hits), "D-4") {
		t.Errorf("superseded exact shown: %v", ids(hits))
	}
	hits, _ = ix.Search(ctx, "p", "float", ports.SearchFilters{Status: "all"}, 10)
	if !slices.Contains(ids(hits), "D-4") {
		t.Errorf("all: %v", ids(hits))
	}
	hits, _ = ix.Search(ctx, "p", "", ports.SearchFilters{Status: "superseded"}, 10)
	if !slices.Equal(ids(hits), []string{"D-4"}) {
		t.Errorf("superseded filter: %v", ids(hits))
	}
	hits, _ = ix.Search(ctx, "p", "emulator", ports.SearchFilters{}, 10)
	if slices.Contains(ids(hits), "M-301") {
		t.Errorf("expired memory shown: %v", ids(hits))
	}
	hits, _ = ix.Search(ctx, "p", "emulator", ports.SearchFilters{Status: "all"}, 10)
	if !slices.Contains(ids(hits), "M-301") {
		t.Errorf("expired with all: %v", ids(hits))
	}
	// with the clock before review_by the memory is live
	ix.SetClock(func() time.Time { return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC) })
	hits, _ = ix.Search(ctx, "p", "emulator", ports.SearchFilters{}, 10)
	if !slices.Contains(ids(hits), "M-301") {
		t.Errorf("memory before review_by: %v", ids(hits))
	}
}

func TestSearchFilters(t *testing.T) {
	ix, _ := setup(t)
	ctx := context.Background()
	hits, _ := ix.Search(ctx, "p", "", ports.SearchFilters{Kind: "memory"}, 10)
	if !slices.Equal(ids(hits), []string{"M-302"}) || hits[0].Layer != LayerFilter {
		t.Errorf("kind: %+v", hits)
	}
	hits, _ = ix.Search(ctx, "p", "money", ports.SearchFilters{Area: "A-payments"}, 10)
	for _, h := range hits {
		if h.ID != "D-12" && !strings.HasPrefix(h.ID, "file:pay/") && !strings.HasPrefix(h.ID, "sym:pay/") && h.ID != "A-payments" {
			t.Errorf("area filter leaked %s", h.ID)
		}
	}
	if !slices.Contains(ids(hits), "D-12") {
		t.Errorf("area: %v", ids(hits))
	}
	hits, _ = ix.Search(ctx, "p", "", ports.SearchFilters{Goal: "G-3"}, 10)
	if !slices.Contains(ids(hits), "G-3") {
		t.Errorf("goal: %v", ids(hits))
	}
	hits, _ = ix.Search(ctx, "p", "", ports.SearchFilters{Kind: "decision", Since: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)}, 10)
	if !slices.Equal(ids(hits), []string{"D-12"}) {
		t.Errorf("since: %v", ids(hits))
	}
	hits, _ = ix.Search(ctx, "q", "cards", ports.SearchFilters{}, 10)
	if len(hits) != 0 {
		t.Errorf("other project: %v", ids(hits))
	}
}

func TestIndexItemsAndLinks(t *testing.T) {
	ix, _ := setup(t)
	ctx := context.Background()
	items := []*domain.Item{
		{ID: "I-1", Title: "Cards screen", Body: "Show the cards list.\nMore text.", Area: "cards", GoalID: "G-3", Status: domain.StatusReady,
			Deps: []string{"I-2"}, UpdatedAt: now,
			Anchors:  domain.Anchors{Files: []string{"app/cards/CardsScreen.kt"}, Symbols: []string{"CardsScreen.render"}, Decisions: []string{"D-12"}},
			Criteria: []domain.Criterion{{ID: "C1", Text: "When the user opens the screen, the system shall list the cartões."}}},
		{ID: "I-2", Title: "Cards API", Area: "A-cards", Status: domain.StatusIntegrated, UpdatedAt: now},
	}
	props := []*domain.Proposal{{ID: "P-1", Title: "Cache card list", Problem: "List loads slowly.", Area: "cards", Status: domain.ProposalApproved, ItemID: "I-1", CreatedAt: now}}
	if err := ix.IndexItems(ctx, "p", items, props); err != nil {
		t.Fatal(err)
	}
	links, err := ix.Links(ctx, "p", "I-1")
	if err != nil {
		t.Fatal(err)
	}
	has := func(ls []ports.Link, l ports.Link) bool { return slices.Contains(ls, l) }
	for _, l := range []ports.Link{
		{From: "I-1", To: "I-2", Kind: "depends_on"}, {From: "I-1", To: "file:app/cards/CardsScreen.kt", Kind: "anchors"}, {From: "I-1", To: "CardsScreen.render", Kind: "anchors"},
		{From: "I-1", To: "D-12", Kind: "anchors"}, {From: "I-1", To: "G-3", Kind: "goal"}, {From: "P-1", To: "I-1", Kind: "item"},
	} {
		if !has(links, l) {
			t.Errorf("missing link %+v in %+v", l, links)
		}
	}
	fl, _ := ix.Links(ctx, "p", "app/cards/CardsScreen.kt")
	if !has(fl, ports.Link{From: "I-1", To: "file:app/cards/CardsScreen.kt", Kind: "anchors"}) {
		t.Errorf("file links: %+v", fl)
	}
	sl, _ := ix.Links(ctx, "p", "sym:app/cards/CardsScreen.kt#CardsScreen.render")
	if !has(sl, ports.Link{From: "I-1", To: "CardsScreen.render", Kind: "anchors"}) ||
		!has(sl, ports.Link{From: "sym:app/cards/CardsScreen.kt#CardsScreen.render", To: "sym:app/cards/CardsScreen.kt#loadCards", Kind: "calls"}) {
		t.Errorf("symbol links: %+v", sl)
	}
	hits, _ := ix.Search(ctx, "p", "I-1", ports.SearchFilters{}, 5)
	if hits[0].ID != "I-1" || hits[0].Summary != "Show the cards list." || hits[0].Area != "A-cards" || !hits[0].Date.Equal(now) {
		t.Errorf("item hit: %+v", hits[0])
	}
	hits, _ = ix.Search(ctx, "p", "cartoes", ports.SearchFilters{Kind: "item"}, 5)
	if !slices.Equal(ids(hits), []string{"I-1"}) {
		t.Errorf("item text: %v", ids(hits))
	}
	hits, _ = ix.Search(ctx, "p", "", ports.SearchFilters{Kind: "item", Status: "integrated"}, 5)
	if !slices.Equal(ids(hits), []string{"I-2"}) {
		t.Errorf("item status: %v", ids(hits))
	}
	hits, _ = ix.Search(ctx, "p", "slowly", ports.SearchFilters{}, 5)
	if len(hits) == 0 || hits[0].ID != "P-1" || hits[0].Kind != "proposal" {
		t.Errorf("proposal: %+v", hits)
	}
	// upsert replaces links
	items[0].Deps = nil
	ix.IndexItems(ctx, "p", items[:1], nil)
	links, _ = ix.Links(ctx, "p", "I-1")
	if has(links, ports.Link{From: "I-1", To: "I-2", Kind: "depends_on"}) {
		t.Error("stale dependency link kept")
	}
}

func TestRulesAndAreas(t *testing.T) {
	ix, _ := setup(t)
	areas := ix.Areas("p")
	if !slices.Equal(areas["cards"], []string{"app/cards/", "web/"}) || !slices.Equal(areas["payments"], []string{"pay/**"}) {
		t.Errorf("areas: %v", areas)
	}
	rules, err := ix.Rules("p", "app/cards/CardsScreen.kt:88")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range rules {
		got = append(got, d.ID)
	}
	if !slices.Equal(got, []string{"constitution", "standard:cards", "D-12"}) {
		t.Errorf("rules: %v", got)
	}
	rules, _ = ix.Rules("p", "pay/service.go")
	got = nil
	for _, d := range rules {
		got = append(got, d.ID)
	}
	if !slices.Equal(got, []string{"constitution", "D-12"}) { // no standards/payments.md; D-4 superseded
		t.Errorf("rules pay: %v", got)
	}
	if as := ix.AreasOf("p", "app/"); !slices.Equal(as, []string{"cards"}) {
		t.Errorf("folder areas: %v", as)
	}
}

func TestFrontMatterHelpers(t *testing.T) {
	if k, id, _ := docIdentity("decisions/D-7-x.md"); k != "decision" || id != "D-7" {
		t.Errorf("%s %s", k, id)
	}
	if k, id, _ := docIdentity("notes/today.md"); k != "doc" || id != "doc:notes/today" {
		t.Errorf("%s %s", k, id)
	}
	d, err := parseDoc("decisions/D-9.md", "---\nid: d-9\nsuperseded_by: D-10\nareas: A-x, A-y\n---\n\n# Title\n\nAnswer line.\n")
	if err != nil || d.ID != "D-9" || d.SupersededBy != "D-10" || d.Title != "Title" || d.Summary != "Answer line." || len(d.Areas) != 2 {
		t.Errorf("%+v %v", d, err)
	}
	if got := ftsQuery(`a"b c*`, " "); got != `"a" "b" "c"` {
		t.Errorf("fts: %s", got)
	}
	if splitIdent("PaymentService.chargeCard") != "Payment Service charge Card" {
		t.Error(splitIdent("PaymentService.chargeCard"))
	}
}

func BenchmarkSearch(b *testing.B) {
	st, _ := sqlite.Open(":memory:")
	defer st.Close()
	ix, _ := New(st.DB())
	ctx := context.Background()
	root := b.TempDir()
	writeTree(b, root, tree)
	ix.Reindex(ctx, "p", root, "c1", nil)
	words := []string{"cards", "payments", "cartões", "wallet", "emulator", "cache", "ledger", "refund", "screen", "layout", "money", "tablet"}
	items := make([]*domain.Item, 0, 1000)
	for i := 1; i <= 1000; i++ {
		w1, w2, w3 := words[i%len(words)], words[(i*7)%len(words)], words[(i*13)%len(words)]
		items = append(items, &domain.Item{
			ID: fmt.Sprintf("I-%d", i), Title: fmt.Sprintf("Improve %s for %s", w1, w2), Area: []string{"cards", "payments"}[i%2],
			Body:   strings.Repeat(fmt.Sprintf("The %s flow must handle %s and %s without errors. ", w1, w2, w3), 8),
			Status: domain.StatusReady, UpdatedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}
	if err := ix.IndexItems(ctx, "p", items, nil); err != nil {
		b.Fatal(err)
	}
	for _, q := range []struct{ name, q string }{{"text", "cartoes wallet"}, {"exact", "I-500"}, {"mixed", "I-42 payments refund"}, {"symbol", "PaymentService.Charge"}} {
		b.Run(q.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				hits, err := ix.Search(ctx, "p", q.q, ports.SearchFilters{}, 10)
				if err != nil || len(hits) == 0 {
					b.Fatal(err, len(hits))
				}
			}
		})
	}
	b.Run("filter", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := ix.Search(ctx, "p", "money", ports.SearchFilters{Kind: "item", Area: "payments"}, 10); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func TestSharedFileDatabase(t *testing.T) {
	ctx := context.Background()
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "barnraise.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ix, err := New(st.DB())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(st.DB()); err != nil { // migrations are idempotent
		t.Fatal(err)
	}
	root := t.TempDir()
	writeTree(t, root, tree)
	done := make(chan error, 1)
	go func() { // the event store keeps writing while the index is rebuilt
		for i := range 50 {
			if _, err := st.Append(ctx, domain.NewEvent(fmt.Sprint("e", i), domain.EvJournal, "p", "", "m", "a", now, domain.Payload{})); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	if err := ix.Reindex(ctx, "p", root, "c1", nil); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	hits, err := ix.Search(ctx, "p", "cartoes", ports.SearchFilters{}, 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("%v %v", hits, err)
	}
}

// BenchmarkReindex indexes 300 Go files of 20 functions each (full walk),
// then measures a no-op full walk (hashes unchanged).
func BenchmarkReindex(b *testing.B) {
	root := b.TempDir()
	files := map[string]string{}
	for f := range 300 {
		var src strings.Builder
		fmt.Fprintf(&src, "package p%d\n\n", f)
		for i := range 20 {
			fmt.Fprintf(&src, "// F%d_%d does work.\nfunc F%d_%d(x int) int {\n\treturn F%d_%d(x) + 1\n}\n\n", f, i, f, i, f, (i+1)%20)
		}
		files[fmt.Sprintf("pkg%d/f.go", f)] = src.String()
	}
	writeTree(b, root, files)
	ctx := context.Background()
	b.Run("full", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			st, _ := sqlite.Open(":memory:")
			ix, _ := New(st.DB())
			if err := ix.Reindex(ctx, "p", root, "c", nil); err != nil {
				b.Fatal(err)
			}
			st.Close()
		}
	})
	b.Run("unchanged", func(b *testing.B) {
		st, _ := sqlite.Open(":memory:")
		defer st.Close()
		ix, _ := New(st.DB())
		ix.Reindex(ctx, "p", root, "c", nil)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := ix.Reindex(ctx, "p", root, "c", nil); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("one-changed", func(b *testing.B) {
		st, _ := sqlite.Open(":memory:")
		defer st.Close()
		ix, _ := New(st.DB())
		ix.Reindex(ctx, "p", root, "c", nil)
		p := filepath.Join(root, "pkg7", "f.go")
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			os.WriteFile(p, []byte(fmt.Sprintf("%s\nfunc Extra%d() {}\n", files["pkg7/f.go"], i)), 0o644)
			if err := ix.Reindex(ctx, "p", root, "c", []string{"pkg7/f.go"}); err != nil {
				b.Fatal(err)
			}
		}
	})
}
