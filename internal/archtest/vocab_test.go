package archtest

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// vocabForbidden is the "Palavras proibidas" table of the design doc: each
// entry is a forbidden word (with its plural) and the glossary word to use.
// "nós" is left out on purpose: in Portuguese it is the pronoun "we".
var vocabForbidden = map[string]string{
	"task": "item", "tasks": "items", "tarefa": "item", "tarefas": "itens",
	"ticket": "item", "tickets": "items", "card": "item", "cards": "items",
	"gate": "check", "gates": "checks", "portão": "verificação", "portões": "verificações",
	"node": "machine", "nodes": "machines", "nó": "máquina",
	"milestone": "goal/focus", "milestones": "goals", "marco": "objetivo/foco", "marcos": "objetivos",
	"sprint": "goal/focus", "sprints": "goals", "epic": "goal/focus", "epics": "goals",
	"cesto": "ajuste rápido", "cestos": "ajustes rápidos", "migalha": "ajuste rápido", "migalhas": "ajustes rápidos",
}

// vocabAllowed lists exact phrases (case-insensitive) that contain a
// forbidden word but are not Barnraise vocabulary. Keep it small; every
// entry says why it is allowed.
// A non-empty file limits the entry to that file (slash path from the root).
var vocabAllowed = []struct{ file, phrase, why string }{
	// Example area/screen names from the design doc ("A-cards", "Cards screen").
	{"", "A-cards", "example area id from the design doc"},
	{"", "cards screen", "example screen name from the design doc"},
	{"", "CardsScreen", "example screen symbol from the design doc"},
	// Third-party API shapes and tool output we must speak verbatim.
	{"", "nodes {", "GraphQL connection field of the GitHub and Linear APIs"},
	{"", "nodes{", "GraphQL connection field of the GitHub and Linear APIs"},
	{"", `"nodes"`, "GraphQL connection field of the GitHub and Linear APIs"},
	{"", "node_modules", "npm folder name"},
	{"", "Node.js", "runtime name"},
	{"internal/app/local.go", "node", "the Node.js program, as its process is named"},
	{"internal/adapters/out/jira/jira.go", "task", `Jira's own issue type name ("Task")`},
	{"internal/adapters/out/outfilter/outfilter.go", "> Task ", "Gradle prints failed tasks as \"> Task :app:test FAILED\""},
	{"internal/adapters/out/outfilter/filters.go", "|Node|", "kubectl describe prints the Kubernetes field \"Node:\""},
}

func vocabRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

var vocabWordRe = func() *regexp.Regexp {
	var words []string
	for w := range vocabForbidden {
		words = append(words, regexp.QuoteMeta(w))
	}
	return regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}_])(` + strings.Join(words, "|") + `)(?:[^\p{L}\p{N}_]|$)`)
}()

// vocabFind returns the forbidden words in s, after removing allowed phrases.
func vocabFind(file, s string) []string {
	low := strings.ToLower(s)
	for _, a := range vocabAllowed {
		if a.file == "" || a.file == file {
			low = strings.ReplaceAll(low, strings.ToLower(a.phrase), " ")
		}
	}
	var out []string
	// Matches can share a boundary character; scan by advancing one rune.
	for i := 0; i < len(low); {
		loc := vocabWordRe.FindStringSubmatchIndex(low[i:])
		if loc == nil {
			break
		}
		out = append(out, low[i+loc[2]:i+loc[3]])
		i += loc[3]
	}
	return out
}

type vocabHit struct {
	where string
	word  string
	text  string
}

// vocabScanText scans text of file whose first line is line number first.
func vocabScanText(file string, first int, text string, hits *[]vocabHit) {
	for n, line := range strings.Split(text, "\n") {
		for _, w := range vocabFind(file, line) {
			*hits = append(*hits, vocabHit{where: file + ":" + strconv.Itoa(first+n), word: w, text: strings.TrimSpace(line)})
		}
	}
}

// vocabGoFiles scans string literals and comments of non-test Go files.
func vocabGoFiles(t *testing.T, root string, hits *[]vocabHit) {
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" || d.Name() == "archtest" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || strings.HasSuffix(p, ".pb.go") {
				return nil
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, p, nil, parser.ParseComments)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if ast.IsGenerated(f) {
				return nil
			}
			for _, cg := range f.Comments {
				for _, c := range cg.List {
					vocabScanText(rel, fset.Position(c.Pos()).Line, c.Text, hits)
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					s = lit.Value
				}
				vocabScanText(rel, fset.Position(lit.Pos()).Line, s, hits)
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func vocabTextFiles(t *testing.T, root, dir string, match func(string) bool, hits *[]vocabHit) {
	base := filepath.Join(root, dir)
	if _, err := os.Stat(base); err != nil {
		return
	}
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !match(p) {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		vocabScanText(filepath.ToSlash(rel), 1, string(b), hits)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func vocabExt(exts ...string) func(string) bool {
	return func(p string) bool {
		for _, e := range exts {
			if strings.HasSuffix(p, e) {
				return true
			}
		}
		return false
	}
}

func TestVocabFind(t *testing.T) {
	for in, want := range map[string]int{
		"a task here":           1,
		"Tasks, tickets":        2,
		"multitask":             0,
		"task_id":               0,
		"o nó caiu":             1,
		"nós vamos":             0,
		"the A-cards area":      0,
		"nodes { id }":          0,
		"gatekeeper":            0,
		"Epic and sprint":       2,
		"migalha":               1,
		"fmt.Sprintf is a call": 0,
	} {
		if got := len(vocabFind("x.go", in)); got != want {
			t.Errorf("vocabFind(%q) = %d words, want %d", in, got, want)
		}
	}
}

func TestVocabForbiddenWords(t *testing.T) {
	root := vocabRoot(t)
	var hits []vocabHit
	vocabGoFiles(t, root, &hits)
	vocabTextFiles(t, root, "templates", vocabExt(".md", ".yaml", ".yml"), &hits)
	vocabTextFiles(t, root, "internal/adapters/in/panel/static", func(string) bool { return true }, &hits)
	vocabTextFiles(t, root, "docs", vocabExt(".md"), &hits)
	if b, err := os.ReadFile(filepath.Join(root, "README.md")); err == nil {
		vocabScanText("README.md", 1, string(b), &hits)
	}
	for _, h := range hits {
		t.Errorf("%s: %q is not Barnraise vocabulary, use %q: %s", h.where, h.word, vocabForbidden[h.word], h.text)
	}
}

// vocabGlossary is the en -> pt-BR glossary table of the design doc, with
// the plural forms used by the panel.
var vocabGlossary = []struct{ en, pt, enPlural, ptPlural string }{
	{"Project", "Projeto", "Projects", "Projetos"},
	{"Goal", "Objetivo", "Goals", "Objetivos"},
	{"Focus", "Foco", "", ""},
	{"Item", "Item", "Items", "Itens"},
	{"Criterion", "Critério", "Criteria", "Critérios"},
	{"Proposal", "Proposta", "Proposals", "Propostas"},
	{"Tweak", "Ajuste rápido", "Tweaks", "Ajustes rápidos"},
	{"Area", "Área", "Areas", "Áreas"},
	{"Flow", "Fluxo", "Flows", "Fluxos"},
	{"Step", "Etapa", "Steps", "Etapas"},
	{"Check", "Verificação", "Checks", "Verificações"},
	{"Review", "Revisão", "Reviews", "Revisões"},
	{"Rework", "Retrabalho", "", ""},
	{"Delivery", "Entrega", "Deliveries", "Entregas"},
	{"Role", "Papel", "Roles", "Papéis"},
	{"Agent", "Agente", "Agents", "Agentes"},
	{"Account", "Conta", "Accounts", "Contas"},
	{"Provider", "Provedor", "Providers", "Provedores"},
	{"Machine", "Máquina", "Machines", "Máquinas"},
	{"Tracker", "Rastreador", "Trackers", "Rastreadores"},
	{"Template", "Modelo de projeto", "Templates", "Modelos de projeto"},
	{"Owner", "Dono", "Owners", "Donos"},
	{"Merge queue", "Fila de integração", "", ""},
	{"Anchor", "Âncora", "Anchors", "Âncoras"},
	{"Revalidation", "Revalidação", "", ""},
}

var vocabEntryRe = regexp.MustCompile(`(?m)([A-Za-z_][A-Za-z0-9_]*)\s*:\s*'((?:[^'\\]|\\.)*)'`)

// vocabDict reads the key: 'value' pairs of one dictionary of the panel
// (web/src/lib/i18n.ts): its object literal and every Object.assign(name, {…}).
func vocabDict(t *testing.T, src []byte, name string) map[string]string {
	t.Helper()
	var blocks [][]byte
	for _, open := range []string{"const " + name + ": Dict = {", "Object.assign(" + name + ", {"} {
		for rest := src; ; {
			start := bytes.Index(rest, []byte(open))
			if start < 0 {
				break
			}
			end := bytes.Index(rest[start:], []byte("\n}"))
			if end < 0 {
				t.Fatalf("i18n.ts: %s is not closed", open)
			}
			blocks = append(blocks, rest[start:start+end])
			rest = rest[start+end:]
		}
	}
	if len(blocks) == 0 {
		t.Fatalf("i18n.ts: dictionary %s not found", name)
	}
	out := map[string]string{}
	for _, b := range blocks {
		for _, m := range vocabEntryRe.FindAllSubmatch(b, -1) {
			out[string(m[1])] = string(m[2])
		}
	}
	return out
}

// TestVocabPanelTranslations checks that panel entries whose English text is
// exactly a glossary term translate exactly as the glossary says.
func TestVocabPanelTranslations(t *testing.T) {
	root := vocabRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "web/src/lib/i18n.ts"))
	if err != nil {
		t.Fatal(err)
	}
	en, pt := vocabDict(t, src, "en"), vocabDict(t, src, "pt")
	if len(en) < 50 || len(pt) < 50 {
		t.Fatalf("i18n.ts parsed too few entries (en %d, pt %d)", len(en), len(pt))
	}
	checked := 0
	for key, ev := range en {
		pv, ok := pt[key]
		if !ok {
			t.Errorf("i18n.ts: key %s has no pt-BR translation", key)
			continue
		}
		for _, g := range vocabGlossary {
			var want string
			switch {
			case strings.EqualFold(ev, g.en):
				want = g.pt
			case g.enPlural != "" && strings.EqualFold(ev, g.enPlural):
				want = g.ptPlural
			default:
				continue
			}
			checked++
			if !strings.EqualFold(pv, want) {
				t.Errorf("i18n.ts: %s is %q in English and must be %q in pt-BR, not %q", key, ev, want, pv)
			}
		}
	}
	for key := range pt {
		if _, ok := en[key]; !ok {
			t.Errorf("i18n.ts: pt-BR key %s has no English entry", key)
		}
	}
	if checked < 5 {
		t.Fatalf("only %d panel entries matched glossary terms; the parser may be broken", checked)
	}
}
