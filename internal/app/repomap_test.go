package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

// mapKnowledge is a knowledge index that also renders the repository map
// and takes the symbols mode.
type mapKnowledge struct {
	*testkit.Knowledge
	mu      sync.Mutex
	changed [][]string
	skip    []string
	root    string
	modes   map[string]string
	indexed string
	fail    bool
}

func (k *mapKnowledge) RepoMap(_ context.Context, project, root string, changed, skip []string, maxFiles int) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fail {
		return "", errors.New("index busy")
	}
	k.changed = append(k.changed, changed)
	k.skip, k.root = skip, root
	var b strings.Builder
	b.WriteString("# Repository map\n\n")
	for _, c := range changed {
		if !slices.Contains(skip, c) {
			fmt.Fprintf(&b, "- %s — changed (max %d)\n", c, maxFiles)
		}
	}
	return b.String(), nil
}

func (k *mapKnowledge) IndexedCommit(context.Context, string) (string, time.Time, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.indexed, time.Time{}, nil
}

func (k *mapKnowledge) SetSymbols(project, mode string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.modes == nil {
		k.modes = map[string]string{}
	}
	k.modes[project] = mode
}

func withKnowledgeImpl(k ports.Knowledge) opt { return func(o *envOpts) { o.knowImpl = k } }

func TestRepoMapLandsInTheItemsCommit(t *testing.T) {
	k := &mapKnowledge{Knowledge: &testkit.Knowledge{}}
	e := newEnv(t, withGit(), withKnowledgeImpl(k))
	it := e.toQueue("Cards screen", map[string]string{"app/cards.txt": "cards\n", "plan.md": "# plan\n"})
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	out := testkit.RunGit(t, e.repo.Root, "--git-dir", e.repo.Remote, "show", "main:"+domain.RepoMapFile)
	if !strings.Contains(out, "- app/cards.txt — changed (max 5000)") || strings.Contains(out, "plan.md") {
		t.Fatalf("map on main:\n%s", out)
	}
	if n := testkit.RunGit(t, e.repo.Root, "--git-dir", e.repo.Remote, "rev-list", "--count", "main"); n != "2" {
		t.Fatalf("the map lands inside the item's commit: %s commits on main", n)
	}
	if !slices.Equal(k.skip, ItemFiles) || !strings.HasSuffix(k.root, it.ID) {
		t.Errorf("skip %v root %s", k.skip, k.root)
	}
	// The index already saw main at its head: only what the next item
	// changes is read again.
	k.mu.Lock()
	k.indexed = e.repo.RemoteMain(t)
	k.mu.Unlock()
	next := e.toQueue("Wallet screen", map[string]string{"app/wallet.txt": "w\n"})
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	k.mu.Lock()
	last := k.changed[len(k.changed)-1]
	k.mu.Unlock()
	if !slices.Contains(last, "app/wallet.txt") || slices.Contains(last, "app/cards.txt") {
		t.Errorf("changed since the indexed commit: %v", last)
	}
	e.wantStatus(next.ID, domain.StatusIntegrated, "integrate")

	// A map that cannot be made never holds an item back.
	k.mu.Lock()
	k.fail = true
	k.mu.Unlock()
	third := e.toQueue("Profile screen", map[string]string{"app/profile.txt": "p\n"})
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	e.wantStatus(third.ID, domain.StatusIntegrated, "integrate")
}

func TestRepoMapOff(t *testing.T) {
	k := &mapKnowledge{Knowledge: &testkit.Knowledge{}}
	e := newEnv(t, withGit(), withKnowledgeImpl(k), withCfg(func(c *domain.ProjectConfig) { c.Knowledge.Map.Enabled = false }))
	e.toQueue("Cards screen", map[string]string{"app/cards.txt": "cards\n"})
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	if contains(e.repo.MainFiles(t), domain.RepoMapFile) || len(k.changed) != 0 {
		t.Fatal("map written while off")
	}
	if err := e.svc.Reindex(e.ctx, "demo", nil); err != nil {
		t.Fatal(err)
	}
	if k.modes["demo"] != "auto" {
		t.Errorf("symbols mode handed to the index: %v", k.modes)
	}
}

func TestRepoMapInTheContext(t *testing.T) {
	e := newEnv(t, withCfg(func(c *domain.ProjectConfig) {
		c.Areas = map[string]domain.AreaDef{"web": {Name: "web", Dir: "web"}}
	}))
	var b strings.Builder
	b.WriteString("# Repository map\n\nOne line per file.\n\n")
	for i := range 200 {
		fmt.Fprintf(&b, "- lib/pkg%03d/file.go — package number %d of the library · Thing%d\n", i, i, i)
	}
	b.WriteString("- web/cards/list.ts — the list of cards · CardList\n- web/wallet.ts — wallet · Wallet\n- docs/payments.md — payments refunds\n")
	b.WriteString("\n## Not listed (over knowledge.map.max_files)\n\n- vendor/ — 30 more files\n")
	file := filepath.Join(e.root, filepath.FromSlash(domain.RepoMapFile))
	os.MkdirAll(filepath.Dir(file), 0o755)
	if err := os.WriteFile(file, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	it := e.create(NewItem{Title: "Payments refunds in the card list", Area: "web", Criteria: crit(ears),
		Anchors: domain.Anchors{Files: []string{"web/cards/list.ts"}}})
	pack, err := e.svc.Context(e.ctx, "demo", owner(), it.ID, 100000)
	if err != nil {
		t.Fatal(err)
	}
	var part *domain.ContextPart
	for i := range pack.Parts {
		if pack.Parts[i].ID == "map" {
			part = &pack.Parts[i]
		}
	}
	if part == nil {
		t.Fatalf("no map part: %+v", pack.Parts)
	}
	lines := strings.Split(part.Body, "\n")
	if !strings.Contains(part.Body, "- docs/payments.md") || !strings.Contains(part.Body, "- web/cards/list.ts") || !strings.Contains(part.Body, "- web/wallet.ts") ||
		strings.Count(part.Body, "- lib/") >= 200 {
		t.Errorf("the relevant lines are kept, the rest fills the room:\n%s", part.Body)
	}
	if !strings.HasPrefix(lines[0], "- lib/pkg000/") {
		t.Errorf("the map's order is kept: %q", lines[0])
	}
	if !strings.HasPrefix(lines[len(lines)-1], "(") || !strings.Contains(lines[len(lines)-1], "of 203 files shown") {
		t.Errorf("footer: %q", lines[len(lines)-1])
	}
	if strings.Contains(part.Body, "vendor/") {
		t.Error("the folders over max_files stay out")
	}
	if got := domain.EstimateTokens(part.Body); got > 1200 {
		t.Errorf("over the budget: %d tokens", got)
	}
	// A small map goes whole; prompt_tokens 0 leaves it out.
	os.WriteFile(file, []byte("# Repository map\n\n- a.go · A\n- b.go · B\n"), 0o644)
	if part, ok := e.svc.repoMapPart(e.p, it); !ok || part.Body != "- a.go · A\n- b.go · B" {
		t.Errorf("small map: %+v", part)
	}
	e.p.Cfg().Knowledge.Map.PromptTokens = 0
	if _, ok := e.svc.repoMapPart(e.p, it); ok {
		t.Error("prompt_tokens 0")
	}
	e.p.Cfg().Knowledge.Map.PromptTokens = 1200
	os.Remove(file)
	if _, ok := e.svc.repoMapPart(e.p, it); ok {
		t.Error("no map yet")
	}
	os.WriteFile(file, []byte("# Repository map\n\nnothing listed\n"), 0o644)
	if _, ok := e.svc.repoMapPart(e.p, it); ok {
		t.Error("an empty map")
	}
}

func TestFindRefusesABadRegex(t *testing.T) {
	e := newEnv(t, withKnowledge())
	for _, q := range []string{"(", "file:web/ [a-"} {
		if _, err := e.svc.Find(e.ctx, "demo", owner(), q, ports.SearchFilters{Kind: "regex"}, 10); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: %v", q, err)
		}
	}
	if _, err := e.svc.Find(e.ctx, "demo", owner(), `file:web/ \w+`, ports.SearchFilters{Kind: "regex"}, 10); err != nil {
		t.Errorf("a good one: %v", err)
	}
}

func TestMapPath(t *testing.T) {
	for line, want := range map[string]string{
		"- a/b.go — what · A, B": "a/b.go",
		"- a/b.go · A":           "a/b.go",
		"- a/b.go":               "a/b.go",
		"- a/b.md — x · y — z":   "a/b.md",
	} {
		if got := mapPath(line); got != want {
			t.Errorf("%q: %q", line, got)
		}
	}
}
