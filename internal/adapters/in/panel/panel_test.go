package panel

import (
	"io/fs"
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

func TestFSHasTheFiles(t *testing.T) {
	for _, name := range []string{"index.html", "app.js", "style.css", "i18n.js"} {
		if len(read(t, name)) == 0 {
			t.Errorf("%s is empty", name)
		}
	}
}

func TestIndexHasNoExternalURLsOrInlineScripts(t *testing.T) {
	html := read(t, "index.html")
	if m := regexp.MustCompile(`(?i)(https?:)?//[a-z0-9.-]+\.[a-z]{2,}`).FindString(html); m != "" {
		t.Errorf("index.html references an external URL: %s", m)
	}
	scripts := regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`).FindAllStringSubmatch(html, -1)
	if len(scripts) != 1 {
		t.Fatalf("want exactly one script tag, got %d", len(scripts))
	}
	attrs, body := scripts[0][1], strings.TrimSpace(scripts[0][2])
	if body != "" {
		t.Errorf("inline script code: %q", body)
	}
	if !strings.Contains(attrs, `src="app.js"`) || !strings.Contains(attrs, `type="module"`) {
		t.Errorf("script tag must be <script src=\"app.js\" type=\"module\">, got %q", attrs)
	}
	if regexp.MustCompile(`(?i)\son[a-z]+\s*=`).MatchString(html) {
		t.Error("index.html has an inline event handler")
	}
}

func TestAssetsStayOnThisOrigin(t *testing.T) {
	for _, name := range []string{"app.js", "i18n.js", "style.css"} {
		src := read(t, name)
		if m := regexp.MustCompile(`(?i)(https?:)?//[a-z0-9-]+\.[a-z0-9.-]+/`).FindString(src); m != "" {
			t.Errorf("%s references an external URL: %s", name, m)
		}
		if regexp.MustCompile(`@import|url\(\s*['"]?https?:`).MatchString(src) {
			t.Errorf("%s loads something from outside", name)
		}
	}
}

func TestNoBrowserDialogsOrHTMLInjection(t *testing.T) {
	for _, name := range []string{"app.js", "i18n.js"} {
		src := read(t, name)
		for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function"} {
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
	banned := regexp.MustCompile(`(?i)\b(tasks?|tarefas?|tickets?|cards?|nodes?|nó|nós|gates?|port[ãa]o|milestones?|marcos?|sprints?|epics?|cestos?|migalhas?)\b`)
	for _, name := range []string{"index.html", "app.js", "i18n.js", "style.css"} {
		for i, line := range strings.Split(read(t, name), "\n") {
			if m := banned.FindString(line); m != "" {
				t.Errorf("%s:%d uses %q", name, i+1, m)
			}
		}
	}
	src := read(t, "i18n.js")
	for _, want := range []string{"Approve the plan", "Approve the delivery", "Approve the proposal", "Aprovar o plano", "Aprovar a entrega", "Aprovar a proposta"} {
		if !strings.Contains(src, want) {
			t.Errorf("dictionary misses %q", want)
		}
	}
	// approving always says what
	if regexp.MustCompile(`'(Approve|Aprovar)'`).MatchString(src) {
		t.Error("an approval does not say what it approves")
	}
}
