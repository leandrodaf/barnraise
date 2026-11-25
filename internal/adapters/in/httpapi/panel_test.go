package httpapi

import (
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/adapters/in/panel"
	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

func panelServer(t *testing.T) *httptest.Server {
	t.Helper()
	svc := app.New(app.Deps{Store: testkit.NewStore(), Clock: testkit.NewClock(), Machine: "m1", Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	srv := httptest.NewServer(New(svc, Options{OwnerToken: "owner-token", Panel: panel.FS()}).Handler())
	t.Cleanup(srv.Close)
	return srv
}

// lazyChunks lists the files the built panel imports on demand (Mermaid and
// its diagrams, through Vite's import() and __vite__mapDeps lists).
func lazyChunks(t *testing.T) []string {
	t.Helper()
	imp := regexp.MustCompile("import\\(`\\./([^`]+\\.js)`\\)")
	deps := regexp.MustCompile(`__vite__mapDeps=\([^)]*\(m\.f=\[([^\]]*)\]`)
	quoted := regexp.MustCompile(`"\./([^"]+)"`)
	seen := map[string]bool{}
	_ = fs.WalkDir(panel.FS(), "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".js") {
			return nil
		}
		b, _ := fs.ReadFile(panel.FS(), p)
		for _, m := range imp.FindAllStringSubmatch(string(b), -1) {
			seen[m[1]] = true
		}
		for _, m := range deps.FindAllStringSubmatch(string(b), -1) {
			for _, q := range quoted.FindAllStringSubmatch(m[1], -1) {
				seen[q[1]] = true
			}
		}
		return nil
	})
	var out []string
	for name := range seen {
		out = append(out, name)
	}
	return out
}

// Every chunk the panel imports later is served as JavaScript (the daemon
// says nosniff, so a wrong type fails the import) and never changes.
func TestPanelServesItsLazyChunks(t *testing.T) {
	srv := panelServer(t)
	chunks := lazyChunks(t)
	var mermaid bool
	for _, c := range chunks {
		mermaid = mermaid || strings.HasPrefix(c, "mermaid.core-")
		res, err := http.Get(srv.URL + "/assets/" + c)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d", c, res.StatusCode)
			continue
		}
		if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") && !strings.HasPrefix(ct, "text/css") {
			t.Errorf("%s served as %q", c, ct)
		}
		if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
			t.Errorf("%s: Cache-Control %q, want immutable", c, cc)
		}
	}
	if !mermaid {
		t.Errorf("no lazy import of Mermaid found among %d chunks: run make web", len(chunks))
	}
}

// index.html is asked for again on every load: a page reloaded after the
// daemon is updated starts the current build, whose chunks all exist.
func TestPanelIndexIsNotCached(t *testing.T) {
	srv := panelServer(t)
	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `type="module"`) {
		t.Fatalf("index: status %d", res.StatusCode)
	}
	if cc := res.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("index.html Cache-Control %q, want no-cache", cc)
	}
	// A chunk of an older build is gone, and the answer is not kept.
	res, err = http.Get(srv.URL + "/assets/mermaid.core-0ld8u1ld.js")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("an old chunk: status %d", res.StatusCode)
	}
	if strings.Contains(res.Header.Get("Cache-Control"), "immutable") {
		t.Error("a missing chunk is cached as immutable")
	}
}
