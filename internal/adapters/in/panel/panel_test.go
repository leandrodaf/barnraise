package panel

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := fs.ReadFile(FS(), name)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return string(b)
}

// built lists the built JavaScript and CSS files.
func built(t *testing.T) []string {
	t.Helper()
	var out []string
	_ = fs.WalkDir(FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".css")) {
			out = append(out, p)
		}
		return nil
	})
	if len(out) == 0 {
		t.Fatal("dist has no JavaScript or CSS: run make web")
	}
	return out
}

// The daemon's CSP is default-src 'self': the page must run with one
// external module and no inline code.
func TestIndexHasNoExternalURLsOrInlineScripts(t *testing.T) {
	html := read(t, "index.html")
	if m := regexp.MustCompile(`(?i)(https?:)?//[a-z0-9.-]+\.[a-z]{2,}`).FindString(html); m != "" {
		t.Errorf("index.html references an external URL: %s", m)
	}
	scripts := regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`).FindAllStringSubmatch(html, -1)
	if len(scripts) != 1 {
		t.Fatalf("want exactly one script tag, got %d", len(scripts))
	}
	if body := strings.TrimSpace(scripts[0][2]); body != "" {
		t.Errorf("inline script code: %q", body)
	}
	if !regexp.MustCompile(`src="\./assets/[^"]+\.js"`).MatchString(scripts[0][1]) || !strings.Contains(scripts[0][1], `type="module"`) {
		t.Errorf("the script must be a module from ./assets, got %q", scripts[0][1])
	}
	if regexp.MustCompile(`(?i)\son[a-z]+\s*=`).MatchString(html) {
		t.Error("index.html has an inline event handler")
	}
}

func TestAssetsStayOnThisOrigin(t *testing.T) {
	for _, name := range built(t) {
		src := read(t, name)
		if regexp.MustCompile(`@import\s+url\(\s*['"]?https?:|url\(\s*['"]?https?:`).MatchString(src) {
			t.Errorf("%s loads something from outside", name)
		}
		if m := regexp.MustCompile(`(?i)(fetch|EventSource|import)\(\s*['"]https?://`).FindString(src); m != "" {
			t.Errorf("%s calls outside: %s", name, m)
		}
	}
}

// web/src is the panel's source: checked here so `go test ./...` covers it.
func sources(t *testing.T) map[string]string {
	t.Helper()
	root := filepath.Join("..", "..", "..", "..", "web")
	out := map[string]string{}
	for _, dir := range []string{"src", "desktop"} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.Contains(p, string(filepath.Separator)+"gen"+string(filepath.Separator)) || strings.Contains(p, "wailsjs") {
				return nil
			}
			if ext := filepath.Ext(p); ext == ".ts" || ext == ".svelte" {
				b, _ := os.ReadFile(p)
				out[p] = string(b)
			}
			return nil
		})
	}
	if len(out) == 0 {
		t.Skip("web/ is not here")
	}
	return out
}

func TestNoBrowserDialogsOrHTMLInjection(t *testing.T) {
	for name, src := range sources(t) {
		for _, bad := range []string{"{@html", "innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function"} {
			if strings.Contains(src, bad) {
				t.Errorf("%s uses %s", name, bad)
			}
		}
		if m := regexp.MustCompile(`\b(window\.)?(alert|confirm|prompt)\(`).FindString(src); m != "" {
			t.Errorf("%s opens a browser dialog: %s", name, m)
		}
	}
}

// The panel speaks the glossary: no word outside it, in any language.
func TestGlossary(t *testing.T) {
	src := sources(t)
	dict := src[filepath.Join("..", "..", "..", "..", "web", "src", "lib", "i18n.ts")]
	if dict == "" {
		t.Fatal("web/src/lib/i18n.ts is missing")
	}
	banned := regexp.MustCompile(`(?i)\b(tasks?|tarefas?|tickets?|nodes?|nó|nós|gates?|port[ãa]o|milestones?|marcos?|sprints?|epics?|cestos?|migalhas?|cards?|cartões|cartão)\b`)
	for i, line := range strings.Split(dict, "\n") {
		if m := banned.FindString(line); m != "" {
			t.Errorf("i18n.ts:%d uses %q", i+1, m)
		}
	}
	for _, want := range []string{"Approve the plan", "Approve the delivery", "Approve the proposal", "Aprovar o plano", "Aprovar a entrega", "Aprovar a proposta"} {
		if !strings.Contains(dict, want) {
			t.Errorf("dictionary misses %q", want)
		}
	}
	if regexp.MustCompile(`'(Approve|Aprovar)'`).MatchString(dict) {
		t.Error("an approval does not say what it approves")
	}
}

// The daemon decides about items (internal/app/board.go): the column, the
// signal, the flow, what the owner approves. The panel only shows it, so no
// step name is matched and no metric limit is written in its source.
func TestNoBusinessRulesInThePanel(t *testing.T) {
	rules := regexp.MustCompile(`/\^?\(?(approv|review|plan|impl|integr|merge|check|deliver)[^/]*/[gimsuy]*\.test\(|\.(reworkAvg|violations|flakyQuarantined|proposalsMade)\s*[<>]=?\s*\d`)
	for name, src := range sources(t) {
		if strings.HasSuffix(name, "i18n.ts") {
			continue
		}
		for i, line := range strings.Split(src, "\n") {
			if m := rules.FindString(line); m != "" {
				t.Errorf("%s:%d decides what the daemon should: %q", name, i+1, m)
			}
		}
	}
}
