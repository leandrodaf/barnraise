package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

func readInput(path string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"file_path": path})
	return b
}

// staleMessages returns the item's messages about main having moved.
func (e *env) staleMessages(id string) []string {
	var out []string
	for _, m := range e.p.Snapshot().Messages {
		if m.ItemID == id && strings.Contains(m.Text, "Main moved to") {
			out = append(out, m.Text)
		}
	}
	return out
}

func TestStaleReadWarnsAfterIntegration(t *testing.T) {
	e := newEnv(t, withGit(), withFiles(map[string]string{"api/a.txt": "a\n", "api/b.txt": "b\n", "api/c.txt": "c\n", "CHANGELOG.md": "# Changelog\n"}))
	x := e.ready("simple", "Reader", "x")
	e.start(x.ID)
	dir := e.workspace(x.ID, nil)
	// The session read a.txt (absolute path), changed nothing yet; reads
	// outside the worktree, of the session folder and searches are not noted.
	e.svc.noteToolFiles(e.ctx, e.p, x.ID, dir, "Read", readInput(filepath.Join(dir, "api", "a.txt")))
	e.svc.noteToolFiles(e.ctx, e.p, x.ID, dir, "Read", readInput(filepath.Join(dir, "api", "a.txt"))) // once
	e.svc.noteToolFiles(e.ctx, e.p, x.ID, dir, "Read", readInput("/etc/passwd"))
	e.svc.noteToolFiles(e.ctx, e.p, x.ID, dir, "Read", readInput(".barnraise-session/policy.json"))
	e.svc.noteToolFiles(e.ctx, e.p, x.ID, dir, "Grep", json.RawMessage(`{"path":"api"}`))
	if got, _ := e.store.TouchedFiles(e.ctx, "demo", x.ID); len(got) != 1 || got[0].Path != "api/a.txt" || got[0].Write {
		t.Fatalf("noted %+v", got)
	}

	y := e.toQueue("Writer", map[string]string{"api/a.txt": "a changed\nmore\n", "api/c.txt": "c2\n"})
	e.svc.noteToolFiles(e.ctx, e.p, y.ID, e.svc.WorktreeDir("demo", y.ID), "Edit", readInput("api/a.txt"))
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); err != nil || n != 1 {
		t.Fatalf("process %d %v", n, err)
	}
	eventually(t, "stale-read warning", func() bool { return strings.Contains(e.types(x.ID), domain.EvItemStaleRead) })
	msgs := e.staleMessages(x.ID)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "api/a.txt: +2 -1") || strings.Contains(msgs[0], "api/c.txt") ||
		strings.Contains(msgs[0], "CHANGELOG") || !strings.Contains(msgs[0], y.ID+" integrated") {
		t.Fatalf("messages %q", msgs)
	}
	// The integrated item's notes are dropped.
	if got, _ := e.store.TouchedFiles(e.ctx, "demo", y.ID); len(got) != 0 {
		t.Fatalf("notes of the integrated item %+v", got)
	}
	// The message reaches the next step's prompt too (it is in the inbox).
	inbox, _ := e.svc.Inbox(e.ctx, "demo", "", "", x.ID, false)
	found := false
	for _, m := range inbox {
		found = found || strings.Contains(m.Text, "Main moved to")
	}
	if !found {
		t.Fatalf("inbox %+v", inbox)
	}
	// A second pass against the same main says nothing new.
	ws, err := e.svc.mainMoved(e.ctx, e.p, "", nil, "again")
	if err != nil || len(ws) != 0 {
		t.Fatalf("second pass %+v %v", ws, err)
	}
}

func TestStaleReadBranchChangesAndIngest(t *testing.T) {
	e := newEnv(t, withGit(), withFiles(map[string]string{"lib/x.go": "package lib\n\nfunc X() {}\n"}))
	x := e.ready("simple", "Branch change", "x")
	e.start(x.ID)
	e.workspace(x.ID, map[string]string{"lib/x.go": "package lib\n\nfunc X() { println() }\n"})
	if _, err := e.svc.commitStep(e.ctx, e.p, x.ID, "feat(x): change x"); err != nil {
		t.Fatal(err)
	}
	// Another machine integrates a change of the same file.
	e.repo.PushToMain(t, "other machine", map[string]string{"lib/x.go": "package lib\n\nfunc X() {}\n\nfunc Y() {}\n"})
	other := domain.NewEvent("ev-other-1", domain.EvItemIntegrated, "demo", "I-99", "m2", "merge-queue", e.clock.Now(), domain.Payload{Note: "integrated"})
	if err := e.svc.Ingest(e.ctx, other); err != nil {
		t.Fatal(err)
	}
	eventually(t, "warning after a remote integration", func() bool { return len(e.staleMessages(x.ID)) == 1 })
	msg := e.staleMessages(x.ID)[0]
	if !strings.Contains(msg, "lib/x.go: +2 -0") || !strings.Contains(msg, "I-99 integrated on m2") || !strings.Contains(msg, "merge queue rebases") {
		t.Fatalf("message %q", msg)
	}
	evs, _ := e.store.Query(e.ctx, ports.EventQuery{Project: "demo", Subject: x.ID, Types: []string{domain.EvItemStaleRead}})
	if len(evs) != 1 {
		t.Fatalf("events %d", len(evs))
	}
	pl, _ := evs[0].DecodePayload()
	if pl.Extra["rebased"] != false || pl.Cause == "" {
		t.Fatalf("payload %+v", pl)
	}
}

func TestStaleReadEarlyRebase(t *testing.T) {
	e := newEnv(t, withGit(), withFiles(map[string]string{"a.txt": "a\n", "b.txt": "b\n"}), withCfg(func(c *domain.ProjectConfig) {
		c.StaleReads.Rebase = true
	}))
	clean := e.ready("simple", "Clean", "x")
	e.start(clean.ID)
	cdir := e.workspace(clean.ID, map[string]string{"b.txt": "b mine\n"})
	e.svc.noteToolFiles(e.ctx, e.p, clean.ID, cdir, "read_file", json.RawMessage(`{"absolute_path":"`+filepath.Join(cdir, "a.txt")+`"}`))
	conflict := e.ready("simple", "Conflict", "x")
	e.start(conflict.ID)
	e.workspace(conflict.ID, map[string]string{"a.txt": "a mine\n"})
	if _, err := e.svc.commitStep(e.ctx, e.p, conflict.ID, "feat(x): mine"); err != nil {
		t.Fatal(err)
	}
	main := e.repo.PushToMain(t, "upstream", map[string]string{"a.txt": "a theirs\n"})
	ws, err := e.svc.mainMoved(e.ctx, e.p, "", nil, "test")
	if err != nil || len(ws) != 2 {
		t.Fatalf("warnings %+v %v", ws, err)
	}
	byID := map[string]StaleRead{}
	for _, w := range ws {
		byID[w.ItemID] = w
	}
	if w := byID[clean.ID]; !w.Rebased || w.Conflict != "" {
		t.Fatalf("clean %+v", w)
	}
	if out := testkit.RunGit(t, cdir, "merge-base", "--is-ancestor", main, "HEAD"); out != "" {
		t.Fatalf("not rebased: %s", out)
	}
	if w := byID[conflict.ID]; w.Rebased || !strings.Contains(w.Conflict, "a.txt") {
		t.Fatalf("conflict %+v", w)
	}
	if msgs := e.staleMessages(clean.ID); len(msgs) != 1 || !strings.Contains(msgs[0], "rebased your branch") {
		t.Fatalf("clean message %q", msgs)
	}
	if msgs := e.staleMessages(conflict.ID); len(msgs) != 1 || !strings.Contains(msgs[0], "did not apply") {
		t.Fatalf("conflict message %q", msgs)
	}
}

func TestStaleReadOffAndLimits(t *testing.T) {
	e := newEnv(t, withGit(), withCfg(func(c *domain.ProjectConfig) { c.StaleReads.Enabled = false }))
	x := e.ready("simple", "Off", "x")
	e.start(x.ID)
	dir := e.workspace(x.ID, nil)
	e.svc.noteToolFiles(e.ctx, e.p, x.ID, dir, "Read", readInput("a.txt"))
	if got, _ := e.store.TouchedFiles(e.ctx, "demo", x.ID); len(got) != 0 {
		t.Fatalf("noted while off %+v", got)
	}
	if ws, err := e.svc.mainMoved(e.ctx, e.p, "", nil, "off"); ws != nil || err != nil {
		t.Fatal(ws, err)
	}

	files := map[string]string{}
	for _, n := range []string{"a", "b", "c", "d"} {
		files[n+".txt"] = n + "\n"
	}
	l := newEnv(t, withGit(), withFiles(files), withCfg(func(c *domain.ProjectConfig) { c.StaleReads.MaxFiles = 2 }))
	it := l.ready("simple", "Many", "x")
	l.start(it.ID)
	ldir := l.workspace(it.ID, nil)
	for n := range files {
		l.svc.noteToolFiles(l.ctx, l.p, it.ID, ldir, "Read", readInput(n))
	}
	l.repo.PushToMain(t, "all", map[string]string{"a.txt": "1\n", "b.txt": "1\n", "c.txt": "1\n", "d.txt": "1\n"})
	ws, err := l.svc.mainMoved(l.ctx, l.p, "", nil, "many")
	if err != nil || len(ws) != 1 || len(ws[0].Files) != 2 || ws[0].More != 2 {
		t.Fatalf("limited %+v %v", ws, err)
	}
	if msg := l.staleMessages(it.ID); len(msg) != 1 || !strings.Contains(msg[0], "and 2 more") {
		t.Fatalf("message %q", msg)
	}
	// A closed item's notes go at the next pass.
	if _, err := l.svc.CloseItem(l.ctx, "demo", owner(), it.ID, "obsolete", "", "not needed"); err != nil {
		t.Fatal(err)
	}
	l.repo.PushToMain(t, "more", map[string]string{"a.txt": "2\n"})
	if _, err := l.svc.mainMoved(l.ctx, l.p, "", nil, "after close"); err != nil {
		t.Fatal(err)
	}
	if got, _ := l.store.TouchedFiles(l.ctx, "demo", it.ID); len(got) != 0 {
		t.Fatalf("notes of a closed item %+v", got)
	}
}

// TestStaleReadNotesSessionToolCalls runs a session through the runtime:
// the files of its tool calls are noted from the provider's stream.
func TestStaleReadNotesSessionToolCalls(t *testing.T) {
	e := newEnv(t, withGit(), withFiles(map[string]string{"lib/a.go": "package lib\n"}))
	e.account("fake-1")
	it := e.ready("simple", "Session", "x")
	e.prov.Handle(func(ctx context.Context, spec ports.SessionSpec, emit func(ports.ProviderEvent)) (ports.SessionResult, error) {
		emit(ports.ProviderEvent{Kind: ports.PEToolUse, Tool: "Read", Input: readInput(filepath.Join(spec.Workdir, "lib", "a.go"))})
		emit(ports.ProviderEvent{Kind: ports.PEToolUse, Tool: "Write", Input: readInput("lib/b.go")})
		testkit.WriteFile(t, spec.Workdir, "lib/b.go", "package lib\n")
		return ports.SessionResult{Text: "stopped before delivering"}, nil
	})
	e.fill()
	e.idle()
	got, _ := e.store.TouchedFiles(e.ctx, "demo", it.ID)
	if len(got) != 2 || got[0].Path != "lib/a.go" || got[0].Write || got[1].Path != "lib/b.go" || !got[1].Write {
		t.Fatalf("noted %+v", got)
	}
}
