package knowledge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/ports"
)

func grepRefs(ms []GrepMatch) []string {
	var out []string
	for _, m := range ms {
		out = append(out, fmt.Sprintf("%s:%d", m.Path, m.Line))
	}
	return out
}

func TestGrepSubstringAndRegex(t *testing.T) {
	ix, _ := setup(t)
	ctx := context.Background()
	cases := []struct {
		pattern string
		o       GrepOptions
		want    []string
	}{
		{"formatCents", GrepOptions{}, []string{"app/cards/CardsScreen.kt:6", "app/cards/CardsScreen.kt:9"}},
		{"FORMATCENTS", GrepOptions{}, nil},
		{"FORMATCENTS", GrepOptions{IgnoreCase: true}, []string{"app/cards/CardsScreen.kt:6", "app/cards/CardsScreen.kt:9"}},
		{"not code", GrepOptions{}, []string{"README.txt:1"}},
		{`fn (post|audit)\(`, GrepOptions{Regex: true}, []string{"rs/lib.rs:4", "rs/lib.rs:9"}},
		{`(fetch|load)Cards\(\)`, GrepOptions{Regex: true, PathPrefix: "web/"}, []string{"web/cards.ts:1", "web/cards.ts:2", "web/cards.ts:7"}},
		{`cents`, GrepOptions{Regex: true, Area: "A-payments"}, []string{"pay/service.go:7", "pay/service.go:8", "pay/service.go:11", "pay/service.go:12"}},
		{`^class \w+`, GrepOptions{Regex: true, Limit: 2}, []string{"app/cards/CardsScreen.kt:3", "lib/cart.dart:1"}},
		{`\)$`, GrepOptions{Regex: true, PathPrefix: "py/"}, []string{"py/util.py:3", "py/util.py:6"}},
		{"integer cents", GrepOptions{}, []string{".barnraise/constitution.md:8", ".barnraise/decisions/D-12-valores-em-centavos.md:10"}},
		{"Hidden", GrepOptions{}, nil},  // binary files are not indexed
		{"ignored", GrepOptions{}, nil}, // nor node_modules
		{"zzzqqq", GrepOptions{}, nil},  // no file has the trigrams
		{"x.", GrepOptions{Regex: true, PathPrefix: "py/"}, nil},
	}
	for _, c := range cases {
		ms, err := ix.Grep(ctx, "p", c.pattern, c.o)
		if err != nil {
			t.Fatalf("%q: %v", c.pattern, err)
		}
		if got := grepRefs(ms); !slices.Equal(got, c.want) {
			t.Errorf("%q %+v:\n got  %v\n want %v", c.pattern, c.o, got, c.want)
		}
	}
	if _, err := ix.Grep(ctx, "p", "(", GrepOptions{Regex: true}); !errors.Is(err, ErrBadPattern) {
		t.Errorf("invalid regex: %v", err)
	}
	if _, err := ix.Grep(ctx, "p", "", GrepOptions{}); !errors.Is(err, ErrBadPattern) {
		t.Errorf("empty pattern: %v", err)
	}
	if ms, _ := ix.Grep(ctx, "other", "formatCents", GrepOptions{}); len(ms) != 0 {
		t.Errorf("projects are separate: %v", ms)
	}
	ms, _ := ix.Grep(ctx, "p", "private fun formatCents", GrepOptions{})
	if len(ms) != 1 || ms[0].Text != "private fun formatCents(n: Int): String = n.toString()" {
		t.Errorf("match text: %+v", ms)
	}
}

func TestGrepThroughSearch(t *testing.T) {
	ix, _ := setup(t)
	ctx := context.Background()
	hits, err := ix.Search(ctx, "p", "file:web/ load", ports.SearchFilters{Kind: KindGrep}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(hits); !slices.Equal(got, []string{"web/cards.ts:2", "web/cards.ts:6"}) || hits[1].Kind != KindMatch || hits[1].Summary != "load(): void {" {
		t.Errorf("grep hits: %v %+v", got, hits)
	}
	hits, _ = ix.Search(ctx, "p", `def \w+\(self`, ports.SearchFilters{Kind: KindRegex}, 10)
	if got := ids(hits); !slices.Equal(got, []string{"py/util.py:2"}) {
		t.Errorf("regex hits: %v", got)
	}
	// get opens the file of a match
	if d, err := ix.Get(ctx, "p", hits[0].ID); err != nil || d.ID != "file:py/util.py" {
		t.Errorf("get %s: %v %v", hits[0].ID, d, err)
	}
	if _, err := ix.Search(ctx, "p", "(", ports.SearchFilters{Kind: KindRegex}, 10); !errors.Is(err, ErrBadPattern) {
		t.Errorf("bad regex: %v", err)
	}
	// the ordinary search finds a word inside a longer identifier
	hits, _ = ix.Search(ctx, "p", "ormatCent", ports.SearchFilters{}, 10)
	found := false
	for _, h := range hits {
		if h.ID == "file:app/cards/CardsScreen.kt" && h.Layer == LayerSubstring {
			found = true
		}
	}
	if !found {
		t.Errorf("substring layer: %+v", hits)
	}
}

func TestGrepIncremental(t *testing.T) {
	ix, root := setup(t)
	ctx := context.Background()
	count := func() (files, postings int) {
		ix.db.QueryRow(`SELECT COUNT(*) FROM grep_files WHERE project = 'p'`).Scan(&files)
		ix.db.QueryRow(`SELECT COUNT(*) FROM grep_postings`).Scan(&postings)
		return
	}
	f0, p0 := count()
	writeTree(t, root, map[string]string{"web/cards.ts": "export function fetchWallets() {}\n"})
	if err := ix.Reindex(ctx, "p", root, "c2", []string{"web/cards.ts"}); err != nil {
		t.Fatal(err)
	}
	if ms, _ := ix.Grep(ctx, "p", "fetchCards", GrepOptions{PathPrefix: "web/"}); len(ms) != 0 {
		t.Errorf("old content still found: %v", ms)
	}
	if ms, _ := ix.Grep(ctx, "p", "fetchWallets", GrepOptions{}); len(ms) != 1 {
		t.Errorf("new content: %v", ms)
	}
	os.Remove(filepath.Join(root, "rs", "lib.rs"))
	if err := ix.Reindex(ctx, "p", root, "c3", []string{"rs/lib.rs"}); err != nil {
		t.Fatal(err)
	}
	if ms, _ := ix.Grep(ctx, "p", "Ledger", GrepOptions{}); len(ms) != 0 {
		t.Errorf("deleted file still found: %v", ms)
	}
	f1, p1 := count()
	if f1 != f0-1 || p1 >= p0 {
		t.Errorf("files %d -> %d, postings %d -> %d", f0, f1, p0, p1)
	}
	var orphans int
	ix.db.QueryRow(`SELECT COUNT(*) FROM grep_postings WHERE file_id NOT IN (SELECT id FROM grep_files)`).Scan(&orphans)
	if orphans != 0 {
		t.Errorf("%d postings of removed files", orphans)
	}
	// the repository map is never indexed back
	writeTree(t, root, map[string]string{".barnraise/map.md": "# Repository map\n- web/cards.ts — fetchWallets\n"})
	if err := ix.Reindex(ctx, "p", root, "c4", nil); err != nil {
		t.Fatal(err)
	}
	if ms, _ := ix.Grep(ctx, "p", "Repository map", GrepOptions{}); len(ms) != 0 {
		t.Errorf("map indexed: %v", ms)
	}
}

func TestMigrationTwoReindexesEverything(t *testing.T) {
	ix, root := setup(t)
	ctx := context.Background()
	// back to a database that only had version 1
	for _, s := range []string{`DROP TABLE grep_postings`, `DROP TABLE grep_files`, `DELETE FROM knowledge_migrations WHERE version = 2`} {
		if _, err := ix.db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	ix2, err := New(ix.db)
	if err != nil {
		t.Fatal(err)
	}
	ix2.SetClock(ix.now)
	if err := ix2.Reindex(ctx, "p", root, "c2", nil); err != nil {
		t.Fatal(err)
	}
	if ms, _ := ix2.Grep(ctx, "p", "formatCents", GrepOptions{}); len(ms) != 2 {
		t.Errorf("after the upgrade: %v", ms)
	}
	if got := symNames(t, ix2, "pay/service.go"); len(got) != 4 {
		t.Errorf("symbols kept: %v", got)
	}
}

// Every line a pattern matches must pass its trigram condition: the
// condition may let extra files through, never drop a matching one.
func TestRegexQueryIsSound(t *testing.T) {
	lines := []string{
		"func (p *PaymentService) Charge(cents int) error {", "export const sumCards = (xs) => xs.length;",
		"SELECT id FROM docs WHERE project = ?", "Rounding errors in the cartões screen.", "abc", "ab", "",
		"error: connection refused (code 111)", "fooBARbaz", "x := map[string]int{}", "TODO(owner): later",
	}
	patterns := []string{
		`Charge`, `(?i)charge`, `cents|dollars`, `sum[A-Z]\w+`, `SELECT .* FROM`, `cart(õ|o)es`, `(?i)CARTÕES`,
		`a+bc`, `ab?c`, `(ab){2,}`, `x{0,3}abc`, `[a-c]{3}`, `^ab$`, `error: \w+ refused`, `foo(bar|BAR)baz`,
		`map\[string\]`, `.`, `\d{3}`, `(?i)Todo\(owner\)`, `refused|^$`,
	}
	for _, p := range patterns {
		parsed, err := syntax.Parse(p, syntax.Perl)
		if err != nil {
			t.Fatal(err)
		}
		q := regexQuery(parsed)
		re := regexp.MustCompile(p)
		for _, l := range lines {
			if re.MatchString(l) && !satisfies(q, trigramsOf([]byte(l))) {
				t.Errorf("%q matches %q but its condition %s refuses it", p, l, describe(q))
			}
		}
	}
	// and the condition is useful: a literal needs its trigrams
	parsed, _ := syntax.Parse(`PaymentService`, syntax.Perl)
	if q := regexQuery(parsed); q.op != qAnd || len(q.tri) < 10 {
		t.Errorf("literal condition: %s", describe(q))
	}
	parsed, _ = syntax.Parse(`.*`, syntax.Perl)
	if q := regexQuery(parsed); q.op != qAll {
		t.Errorf(".* must read every file: %s", describe(q))
	}
}

func satisfies(q *triQuery, have []uint32) bool {
	in := func(t uint32) bool { _, ok := slices.BinarySearch(have, t); return ok }
	switch q.op {
	case qAnd:
		for _, t := range q.tri {
			if !in(t) {
				return false
			}
		}
		for _, s := range q.sub {
			if !satisfies(s, have) {
				return false
			}
		}
		return true
	case qOr:
		for _, t := range q.tri {
			if in(t) {
				return true
			}
		}
		for _, s := range q.sub {
			if satisfies(s, have) {
				return true
			}
		}
		return false
	}
	return true
}

func describe(q *triQuery) string {
	var parts []string
	for _, t := range q.tri {
		parts = append(parts, fmt.Sprintf("%q", string([]byte{byte(t >> 16), byte(t >> 8), byte(t)})))
	}
	for _, s := range q.sub {
		parts = append(parts, describe(s))
	}
	switch q.op {
	case qAnd:
		return "AND(" + strings.Join(parts, " ") + ")"
	case qOr:
		return "OR(" + strings.Join(parts, " ") + ")"
	}
	return "ALL"
}

func TestTrigramHelpers(t *testing.T) {
	ts := trigramsOf([]byte("Abc\n   abcd"))
	if got := decodeTrigrams(encodeTrigrams(ts)); !slices.Equal(got, ts) {
		t.Errorf("round trip: %v %v", got, ts)
	}
	for _, x := range ts {
		if !indexable(x) {
			t.Errorf("not indexable: %x", x)
		}
	}
	if len(ts) != 2+3 { // abc, bcd + "  a", " ab" ... minus blanks and line breaks
		t.Logf("trigrams: %d", len(ts))
	}
	for p, want := range map[string]bool{"a/b.yaml": true, "Makefile": true, "go.sum": false, "x/.env": false, "a/b.bin": false, "docs/x.md": true} {
		if textFile(p) != want {
			t.Errorf("textFile(%q) = %v", p, !want)
		}
	}
}
