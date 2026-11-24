package domain

import (
	"strings"
	"testing"
	"time"
)

func TestChainHashCanonical(t *testing.T) {
	e := Event{Seq: 3, ID: "e", Project: "p", Type: EvJournal, Subject: "I-1", Machine: "m", Actor: "a", Time: time.Unix(10, 5), Data: []byte(`{"note":"x"}`)}
	h := ChainHash("prev", e)
	if len(h) != 64 || h != ChainHash("prev", e) {
		t.Fatalf("hash %q", h)
	}
	// The same instant in another zone is the same event.
	z := e
	z.Time = e.Time.In(time.FixedZone("x", 3600))
	if ChainHash("prev", z) != h {
		t.Fatal("zone changed the hash")
	}
	// Every field counts, and moving bytes between fields is not the same.
	for name, f := range map[string]func(*Event){
		"seq": func(e *Event) { e.Seq++ }, "id": func(e *Event) { e.ID = "f" }, "project": func(e *Event) { e.Project = "q" },
		"type": func(e *Event) { e.Type = EvItemCreated }, "subject": func(e *Event) { e.Subject = "I-2" },
		"machine": func(e *Event) { e.Machine = "n" }, "actor": func(e *Event) { e.Actor = "b" },
		"time": func(e *Event) { e.Time = e.Time.Add(1) }, "data": func(e *Event) { e.Data = []byte(`{}`) },
		"shift": func(e *Event) { e.Project, e.Type = "p"+EvJournal[:1], EvJournal[1:] },
	} {
		c := e
		f(&c)
		if ChainHash("prev", c) == h {
			t.Fatalf("%s does not change the hash", name)
		}
	}
	if ChainHash("other", e) == h {
		t.Fatal("prev does not change the hash")
	}
	a := ChainAnchor{Seq: 1, Hash: "h", Time: time.Unix(1, 0), Machine: "m"}
	b := a
	b.Seq = 2
	if string(a.SignedBytes()) == string(b.SignedBytes()) {
		t.Fatal("anchor bytes ignore seq")
	}
	if !(ChainReport{}).OK() || (ChainReport{Break: &ChainBreak{}}).OK() || (ChainReport{Anchor: &ChainAnchorCheck{}}).OK() {
		t.Fatal("report OK")
	}
}

func TestManifestDiff(t *testing.T) {
	m := Manifest{"a": "1", "b": "2", "c": "3"}
	d := m.Diff(Manifest{"a": "1", "b": "9", "d": "4"})
	if len(d) != 3 || d[0].Path != "b" || d[0].Kind != "changed" || d[1].Kind != "removed" || d[2].Kind != "added" {
		t.Fatalf("diff %+v", d)
	}
	if len(m.Diff(Manifest{"a": "1", "b": "2", "c": "3"})) != 0 {
		t.Fatal("equal manifests differ")
	}
	if s := TrustPaths(d, 2); !strings.Contains(s, "b (changed)") || !strings.HasSuffix(s, "…") {
		t.Fatalf("paths %q", s)
	}
}

func TestInstructionFile(t *testing.T) {
	yes := []string{"CLAUDE.md", "docs/CLAUDE.md", "./AGENTS.md", "web/GEMINI.md", "CLAUDE.local.md", ".mcp.json", "barnraise.yaml",
		".claude/settings.json", ".claude/commands/x.md", ".barnraise/constitution.md", ".codex/config.toml", ".gemini/settings.json"}
	no := []string{"README.md", "src/claude.md", "sub/.mcp.json", "sub/barnraise.yaml", ".claude", ".github/workflows/ci.yml", "x/.claude/settings.json"}
	for _, p := range yes {
		if !InstructionFile(p) {
			t.Errorf("%s should be an instruction file", p)
		}
	}
	for _, p := range no {
		if InstructionFile(p) {
			t.Errorf("%s should not be an instruction file", p)
		}
	}
	if !InstructionDir(".claude") || InstructionDir("src") {
		t.Fatal("dirs")
	}
}
