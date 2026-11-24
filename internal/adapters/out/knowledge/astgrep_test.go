package knowledge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/execx"
	"github.com/leandrodaf/barnraise/internal/adapters/out/sqlite"
)

// agFixture builds one line of ast-grep's --json=stream output for the
// first occurrence of text in src.
func agFixture(t *testing.T, rel, src string, rule int, text string) string {
	t.Helper()
	s := strings.Index(src, text)
	if s < 0 {
		t.Fatalf("%q not in %s", text, rel)
	}
	var m agMatch
	m.Text, m.File, m.Rule = text, rel, "barnraise-"+strconv.Itoa(rule)
	m.Range.Bytes.Start, m.Range.Bytes.End = s, s+len(text)
	m.Range.Start.Line = strings.Count(src[:s], "\n")
	m.Range.End.Line = strings.Count(src[:s+len(text)], "\n")
	b, _ := json.Marshal(m)
	return string(b)
}

// fakeAstGrep writes a stand-in for the ast-grep CLI: --version prints
// version, scan prints the fixture (and records its arguments and folder).
func fakeAstGrep(t *testing.T, version string, versionExit int, fixture string, scanExit int) (bin, record string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	dir := t.TempDir()
	fx := filepath.Join(dir, "fixture.jsonl")
	record = filepath.Join(dir, "calls.txt")
	if err := os.WriteFile(fx, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo '" + version + "'; exit " + strconv.Itoa(versionExit) + "; fi\n" +
		"echo \"CALL $(pwd) $1 $4 $5\" >> '" + record + "'\n" +
		"cat '" + fx + "'\n" +
		"exit " + strconv.Itoa(scanExit) + "\n"
	bin = filepath.Join(dir, "sg")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, record
}

func newIndex(t *testing.T) *Index {
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
	return ix
}

const agTS = `export class Store {
  load(): number {
    return count();
  }
}

export function count(): number {
  return 1;
}
`

const agPy = `class Money:
    def cents(self):
        return 1
`

func extractorOf(t *testing.T, ix *Index, file string) string {
	t.Helper()
	d, err := ix.Get(context.Background(), "p", "file:"+file)
	if err != nil {
		t.Fatal(err)
	}
	return d.Meta["extractor"]
}

func TestAstGrepUsedWhenInstalled(t *testing.T) {
	fixture := strings.Join([]string{
		agFixture(t, "web/store.ts", agTS, 2, agTS[:strings.Index(agTS, "\n}\n")+2]),        // class_declaration
		agFixture(t, "web/store.ts", agTS, 7, "load(): number {\n    return count();\n  }"), // method_definition
		agFixture(t, "web/store.ts", agTS, 0, "function count(): number {\n  return 1;\n}"), // function_declaration
		`not json`,
		// a match whose text does not agree with the file: py/money.py falls back
		`{"text":"class Other:","file":"py/money.py","ruleId":"barnraise-1","range":{"byteOffset":{"start":0,"end":12},"start":{"line":0},"end":{"line":0}}}`,
	}, "\n") + "\n"
	bin, record := fakeAstGrep(t, "ast-grep 0.39.6", 0, fixture, 0)
	ix := newIndex(t)
	ix.UseAstGrep(execx.New(), bin)
	root := t.TempDir()
	writeTree(t, root, map[string]string{"web/store.ts": agTS, "py/money.py": agPy, "main.go": "package main\nfunc main() {}\n"})
	if err := ix.Reindex(context.Background(), "p", root, "c1", nil); err != nil {
		t.Fatal(err)
	}
	if got := symNames(t, ix, "web/store.ts"); !slices.Equal(got, []string{"Store", "Store.load", "count"}) {
		t.Errorf("ts symbols: %v", got)
	}
	if e := extractorOf(t, ix, "web/store.ts"); e != extractorAstGrep {
		t.Errorf("ts extractor: %q", e)
	}
	if e := extractorOf(t, ix, "py/money.py"); e != extractorNative {
		t.Errorf("python must fall back when ast-grep disagrees with the file: %q", e)
	}
	if got := symNames(t, ix, "py/money.py"); !slices.Equal(got, []string{"Money", "Money.cents"}) {
		t.Errorf("py symbols: %v", got)
	}
	if e := extractorOf(t, ix, "main.go"); e != extractorGo {
		t.Errorf("go stays on go/ast: %q", e)
	}
	calls, _ := os.ReadFile(record)
	if n := strings.Count(string(calls), "CALL "+root+" scan "); n != 2 {
		t.Errorf("one scan per language, in the project folder: %q", calls)
	}
	if !strings.Contains(string(calls), "scan --json=stream web/store.ts") {
		t.Errorf("json output: %q", calls)
	}
	// native mode never runs it
	ix.SetSymbols("p", SymbolsNative)
	writeTree(t, root, map[string]string{"web/store.ts": agTS + "\nexport function more() {}\n"})
	if err := ix.Reindex(context.Background(), "p", root, "c2", nil); err != nil {
		t.Fatal(err)
	}
	if e := extractorOf(t, ix, "web/store.ts"); e != extractorNative {
		t.Errorf("native mode: %q", e)
	}
	calls2, _ := os.ReadFile(record)
	if len(calls2) != len(calls) {
		t.Errorf("native mode ran ast-grep: %q", calls2)
	}
}

func TestAstGrepShadowSgIsIgnored(t *testing.T) {
	bin, record := fakeAstGrep(t, "Usage: sg group [[-c] command]", 1, "", 0)
	ix := newIndex(t)
	ix.UseAstGrep(execx.New(), bin)
	root := t.TempDir()
	writeTree(t, root, map[string]string{"web/store.ts": agTS})
	if err := ix.Reindex(context.Background(), "p", root, "c1", nil); err != nil {
		t.Fatal(err)
	}
	if e := extractorOf(t, ix, "web/store.ts"); e != extractorNative {
		t.Errorf("extractor: %q", e)
	}
	if _, err := os.Stat(record); err == nil {
		t.Error("the shadow-utils sg must never be asked to scan")
	}
}

func TestAstGrepBrokenLanguageFallsBack(t *testing.T) {
	bin, record := fakeAstGrep(t, "ast-grep 0.1.0", 0, "", 3)
	ix := newIndex(t)
	ix.UseAstGrep(execx.New(), bin)
	root := t.TempDir()
	writeTree(t, root, map[string]string{"web/store.ts": agTS})
	ctx := context.Background()
	if err := ix.Reindex(ctx, "p", root, "c1", nil); err != nil {
		t.Fatal(err)
	}
	if got := symNames(t, ix, "web/store.ts"); !slices.Equal(got, []string{"Store", "Store.load", "count"}) {
		t.Errorf("native symbols: %v", got)
	}
	writeTree(t, root, map[string]string{"web/store.ts": agTS + "\nexport function more() {}\n"})
	if err := ix.Reindex(ctx, "p", root, "c2", nil); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(record)
	if n := strings.Count(string(calls), "CALL "); n != 1 {
		t.Errorf("a language that failed is not tried again: %d calls", n)
	}
}

func TestInlineRulesAndNames(t *testing.T) {
	r := inlineRules("Python")
	for _, want := range []string{"id: barnraise-0\nlanguage: Python\nseverity: hint\nrule:\n  kind: function_definition\n", "---\n", "  inside:\n    kind: module\n"} {
		if !strings.Contains(r, want) {
			t.Errorf("rules miss %q:\n%s", want, r)
		}
	}
	for lang, rules := range agRules {
		if len(rules) == 0 || !strings.Contains(inlineRules(lang), "language: "+lang) {
			t.Errorf("%s rules", lang)
		}
	}
	cases := []struct {
		how            int
		header, n, kwd string
	}{
		{nameClass, "enum class Color {", "Color", "enum"},
		{nameClass, "public sealed interface Shape", "Shape", "interface"},
		{nameFunc, "suspend fun String.slug(): String", "slug", ""},
		{nameFunc, "pub fn open<'a>(x: &'a str)", "open", ""},
		{nameMethod, "public <T> List<T> find(int id) {", "find", ""},
		{nameMethod, "async get value(): number {", "value", ""},
		{nameVar, "const LIMIT = 3", "LIMIT", ""},
		{nameVar, "MAX_SIZE: int = 3", "MAX_SIZE", ""},
		{nameImpl, "impl<T> Display for Wrapper<T> {", "Wrapper", ""},
		{nameFunc, "nothing here", "", ""},
	}
	for _, c := range cases {
		if n, k := declName(c.how, c.header); n != c.n || k != c.kwd {
			t.Errorf("%q: got %q %q", c.header, n, k)
		}
	}
	if got := agLangOf("a/b.TSX"); got != "Tsx" {
		t.Errorf("lang: %q", got)
	}
	if a := newAstGrep(execx.New()); !slices.Equal(a.candidates, []string{"ast-grep", "sg"}) {
		t.Errorf("default candidates: %v", a.candidates)
	}
}

// TestAstGrepReal runs the real ast-grep when it is installed.
func TestAstGrepReal(t *testing.T) {
	ex := execx.New()
	a := newAstGrep(ex)
	if a.binary(context.Background()) == "" {
		t.Skip("ast-grep is not installed")
	}
	ix := newIndex(t)
	ix.UseAstGrep(ex)
	root := t.TempDir()
	writeTree(t, root, tree)
	if err := ix.Reindex(context.Background(), "p", root, "c1", nil); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"web/cards.ts":     {"fetchCards", "CardStore", "CardStore.load", "sumCards"},
		"py/util.py":       {"Money", "Money.cents", "to_cents"},
		"rs/lib.rs":        {"Ledger", "Ledger.post", "audit"},
		"java/Bank.java":   {"Bank", "Bank.deposit", "Bank.log"},
		"ios/Wallet.swift": {"Wallet", "Wallet.pay", "charge"},
	}
	for f, w := range want {
		got := symNames(t, ix, f)
		slices.Sort(got)
		slices.Sort(w)
		if !slices.Equal(got, w) {
			t.Errorf("%s (%s): got %v want %v", f, extractorOf(t, ix, f), got, w)
		}
	}
}
