package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestToolFiles(t *testing.T) {
	cases := []struct {
		tool, input string
		files       []string
		write       bool
	}{
		{"Read", `{"file_path":"/w/a.go"}`, []string{"/w/a.go"}, false},
		{"NotebookRead", `{"notebook_path":"n.ipynb"}`, []string{"n.ipynb"}, false},
		{"read_file", `{"absolute_path":"/w/b.go"}`, []string{"/w/b.go"}, false},
		{"read_many_files", `{"paths":["a.go","b.go",""]}`, []string{"a.go", "b.go"}, false},
		{"Edit", `{"file_path":"a.go"}`, []string{"a.go"}, true},
		{"file_change", `{"changes":[{"path":"x.go","kind":"update"},{"kind":"add"}]}`, []string{"x.go"}, true},
		{"apply_patch", `{"input":"*** Begin Patch\n*** Update File: p.go\n*** Add File: q.go\n"}`, []string{"p.go", "q.go"}, true},
		{"Grep", `{"path":"api"}`, nil, false},
		{"Bash", `{"command":"cat a.go"}`, nil, false},
		{"Read", `not json`, nil, false},
	}
	for _, c := range cases {
		files, write := ToolFiles(c.tool, json.RawMessage(c.input))
		if !reflect.DeepEqual(files, c.files) || write != c.write {
			t.Errorf("%s %s = %v %v, want %v %v", c.tool, c.input, files, write, c.files, c.write)
		}
	}
}

func TestRepoPath(t *testing.T) {
	cases := map[string]string{
		"/w/repo/api/a.go":               "api/a.go",
		"api/../api/b.go":                "api/b.go",
		"./c.go":                         "c.go",
		"/w/repo":                        "",
		"/w/repository/x":                "",
		"/etc/passwd":                    "",
		"../out.go":                      "",
		".git/config":                    "",
		".barnraise-session/policy.json": "",
		"":                               "",
		`api\win.go`:                     "api/win.go",
	}
	for in, want := range cases {
		got, ok := RepoPath("/w/repo", in)
		if got != want || ok != (want != "") {
			t.Errorf("RepoPath(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
	if _, ok := RepoPath("", "/abs/a.go"); ok {
		t.Error("absolute path without a worktree")
	}
}

var sampleDiff = `diff --git a/api/a.go b/api/a.go
index 1..2 100644
--- a/api/a.go
+++ b/api/a.go
@@ -1,3 +1,4 @@ func Load() error {
-old
+new
+more
@@ -10,2 +11,2 @@ func Load() error {
-x
+y
@@ -20 +21 @@ type Card struct {
+z
diff --git a/api/new.go b/api/new.go
new file mode 100644
--- /dev/null
+++ b/api/new.go
@@ -0,0 +1 @@
+package api
diff --git a/old.go b/old.go
deleted file mode 100644
--- a/old.go
+++ /dev/null
@@ -1 +0,0 @@
-package old
diff --git a/skip.go b/skip.go
--- a/skip.go
+++ b/skip.go
@@ -1 +1 @@ ` + strings.Repeat("x", 70) + `
-a
+b
`

func TestSummarizeDiff(t *testing.T) {
	all := SummarizeDiff(sampleDiff, nil, 2)
	if len(all) != 4 {
		t.Fatalf("all %+v", all)
	}
	a := all[0]
	if a.File != "api/a.go" || a.Added != 4 || a.Removed != 2 || !reflect.DeepEqual(a.Hunks, []string{"func Load() error {", "type Card struct {"}) {
		t.Fatalf("a %+v", a)
	}
	if got := a.String(); got != "api/a.go: +4 -2 (func Load() error {; type Card struct {)" {
		t.Fatalf("string %q", got)
	}
	if got := all[1].String(); got != "api/new.go: +1 -0 (new file)" {
		t.Fatalf("new %q", got)
	}
	if got := all[2].String(); got != "old.go: +0 -1 (deleted)" {
		t.Fatalf("deleted %q", got)
	}
	if h := all[3].Hunks; len(h) != 1 || !strings.HasSuffix(h[0], "…") || len(h[0]) > 64 {
		t.Fatalf("long context %q", h)
	}
	some := SummarizeDiff(sampleDiff, []string{"old.go"}, 2)
	if len(some) != 1 || some[0].File != "old.go" {
		t.Fatalf("some %+v", some)
	}
	if got := (FileDiff{File: "x", Added: 1}).String(); got != "x: +1 -0" {
		t.Fatalf("plain %q", got)
	}
	if hunkContext("@@ broken") != "" {
		t.Fatal("broken hunk header")
	}
	if SummarizeDiff("garbage\n+line\n", nil, 1) != nil {
		t.Fatal("lines before any file")
	}
}

func TestStaleFiles(t *testing.T) {
	got := StaleFiles([]string{"b.go", "a.go", "plan.md", "c.go", "a.go"}, []string{"a.go", "plan.md", "b.go"}, []string{"plan.md"})
	if !reflect.DeepEqual(got, []string{"a.go", "b.go"}) {
		t.Fatalf("stale %v", got)
	}
	if DefaultStaleReads().Rebase || !DefaultStaleReads().Enabled {
		t.Fatal("defaults")
	}
}

func TestLessonDistillRender(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	it := &Item{ID: "I-7", Area: "A-wallet", Anchors: Anchors{Areas: []string{"wallet", "ledger"}}}
	rr := ReworkRecord{From: "check", Reason: "check unit failed: TestBalance\n\n```\nlog\n```\n- rounding error", Outcome: "fail", At: at}
	files := []string{"wallet/e.go", "wallet/a.go", "wallet/b.go", "wallet/c.go", "wallet/d.go", "wallet/a.go"}
	l := DistillLesson(it, rr, files, ChecksNamed(rr.Reason, []string{"lint", "unit"}), at)
	if l.Source() != "I-7#check@1767225600" || l.ID != LessonID(l.Source()) || !IsLessonID(l.ID) {
		t.Fatalf("ids %+v", l)
	}
	if !reflect.DeepEqual(l.Areas, []string{"wallet", "ledger"}) || !reflect.DeepEqual(l.Checks, []string{"unit"}) || l.Files[0] != "wallet/a.go" || len(l.Files) != 5 {
		t.Fatalf("tags %+v", l)
	}
	want := "In area wallet, ledger, check unit sent the work back: check unit failed: TestBalance rounding error. The fix changed wallet/a.go, wallet/b.go, wallet/c.go, wallet/d.go and 1 more."
	if l.Text != want {
		t.Fatalf("text\n%q\nwant\n%q", l.Text, want)
	}
	bare := DistillLesson(&Item{ID: "I-8"}, ReworkRecord{From: "review", Reason: "  "}, nil, nil, at)
	if bare.Text != "In this code, step review sent the work back: no reason given." {
		t.Fatalf("bare %q", bare.Text)
	}
	long := shortReason(strings.Repeat("word ", 100))
	if len(long) > 245 || !strings.HasSuffix(long, "…") {
		t.Fatalf("long %q", long)
	}
	many := make([]string, 20)
	for i := range many {
		many[i] = string(rune('a'+i)) + ".go"
	}
	if got := boundFiles(many); len(got) != lessonFiles {
		t.Fatalf("bounded %d", len(got))
	}
	c := l.Clone()
	c.Files[0] = "changed"
	if l.Files[0] == "changed" {
		t.Fatal("clone shares files")
	}
}

func TestLessonIDs(t *testing.T) {
	for in, want := range map[string]bool{"L-0000abcd": true, "l-0000ABCD": true, " L-12345678 ": true, "L-123": false, "I-12345678": false, "L-1234567g": false} {
		if IsLessonID(in) != want {
			t.Errorf("IsLessonID(%q) != %v", in, want)
		}
	}
	if NormalizeLessonID(" l-0000ABCD ") != "L-0000abcd" || NormalizeLessonID("x") != "x" {
		t.Fatal("normalize")
	}
}

func TestLessonSimilarAbsorb(t *testing.T) {
	at := time.Unix(100, 0).UTC()
	a := &Lesson{ID: "L-1", Step: "review", Reason: "balance rounded with floats instead of integer cents", Areas: []string{"wallet"}, Files: []string{"wallet/a.go"}, Sources: []string{"s1"}, Count: 1, Version: 1}
	b := &Lesson{ID: "L-2", Step: "review", Reason: "the balance is rounded with floats, use integer cents", Areas: []string{"wallet"}, Files: []string{"wallet/b.go"}, Sources: []string{"s2"}, Count: 1}
	if !a.Similar(b) {
		t.Fatal("same mistake")
	}
	other := *b
	other.Step = "check"
	if a.Similar(&other) {
		t.Fatal("different step")
	}
	far := *b
	far.Reason = "the screen title is missing"
	if a.Similar(&far) {
		t.Fatal("different reason")
	}
	elsewhere := *b
	elsewhere.Areas, elsewhere.Files = []string{"cards"}, []string{"cards/x.go"}
	if a.Similar(&elsewhere) {
		t.Fatal("different place")
	}
	dir := *b
	dir.Areas = nil
	if !a.Similar(&dir) {
		t.Fatal("same folder")
	}
	nowhere1 := &Lesson{Step: "s", Reason: "same words here"}
	nowhere2 := &Lesson{Step: "s", Reason: "same words here"}
	if !nowhere1.Similar(nowhere2) {
		t.Fatal("untagged twins")
	}
	if (&Lesson{Step: "s", Reason: "a 1"}).Similar(&Lesson{Step: "s", Reason: "b 2"}) {
		t.Fatal("no words")
	}
	a.Absorb(b, at)
	if a.Count != 2 || a.Version != 2 || len(a.Sources) != 2 || len(a.Files) != 2 || !a.UpdatedAt.Equal(at) || !strings.Contains(a.Text, "seen 2 times") {
		t.Fatalf("absorbed %+v", a)
	}
}

func TestRelevantLessonsAndState(t *testing.T) {
	t0 := time.Unix(100, 0)
	ls := []*Lesson{
		{ID: "L-a", Files: []string{"api/a.go"}, Count: 1, UpdatedAt: t0},
		{ID: "L-b", Areas: []string{"api"}, Count: 3, UpdatedAt: t0},
		{ID: "L-c", Files: []string{"api/z.go"}, Count: 1, UpdatedAt: t0.Add(time.Hour)},
		{ID: "L-d", Files: []string{"web/x.go"}, Areas: []string{"web"}, Count: 9},
		{ID: "L-e", Areas: []string{"api"}, Count: 3, UpdatedAt: t0},
	}
	got := RelevantLessons(ls, []string{"api/a.go"}, []string{"A-api", ""}, 10)
	var ids []string
	for _, l := range got {
		ids = append(ids, l.ID)
	}
	if strings.Join(ids, ",") != "L-a,L-b,L-e,L-c" {
		t.Fatalf("order %v", ids)
	}
	if len(RelevantLessons(ls, []string{"api/a.go"}, nil, 1)) != 1 || RelevantLessons(ls, nil, nil, 0) != nil {
		t.Fatal("limits")
	}
	tie := RelevantLessons([]*Lesson{{ID: "L-y", Areas: []string{"x"}, UpdatedAt: t0}, {ID: "L-x", Areas: []string{"x"}, UpdatedAt: t0}}, nil, []string{"x"}, 5)
	if tie[0].ID != "L-x" {
		t.Fatal("tie by id")
	}

	s := NewState("p")
	l := &Lesson{ID: "L-1", Text: "v1", Version: 1, Count: 1}
	for _, ev := range []Event{
		NewEvent("e1", EvLessonLearned, "p", l.ID, "m", "a", t0, Payload{Lesson: l}),
		NewEvent("e2", EvLessonMerged, "p", l.ID, "m", "a", t0, Payload{Lesson: &Lesson{ID: "L-1", Text: "v2", Version: 2, Count: 2}}),
		NewEvent("e3", EvLessonMerged, "p", l.ID, "m", "a", t0, Payload{Lesson: &Lesson{ID: "L-1", Text: "old", Version: 1}}),
		NewEvent("e4", EvLessonLearned, "p", "L-2", "m", "a", t0, Payload{Lesson: &Lesson{ID: "L-2", Version: 1, Count: 1}}),
	} {
		if err := s.Apply(ev); err != nil {
			t.Fatal(err)
		}
	}
	if list := s.LessonList(); len(list) != 2 || list[0].ID != "L-1" || list[0].Text != "v2" {
		t.Fatalf("lessons %+v", list)
	}
	_ = s.Apply(NewEvent("e5", EvLessonDeleted, "p", "L-1", "m", "a", t0, Payload{Lesson: &Lesson{ID: "L-1"}}))
	if len(s.Lessons) != 1 {
		t.Fatal("deleted")
	}
	var empty State
	empty.applyLesson(EvLessonLearned, &Lesson{ID: "L-3"})
	if len(empty.Lessons) != 1 {
		t.Fatal("nil map")
	}
	if !DefaultLessons().Enabled || DefaultLessons().Max != 5 {
		t.Fatal("defaults")
	}
	if l := (&Lesson{ID: "L-z", Count: 1, Sources: nil}); l.Source() != "" {
		t.Fatal("no source")
	}
}
