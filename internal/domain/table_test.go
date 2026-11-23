package domain

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestShellWritesAndReads(t *testing.T) {
	cases := []struct {
		cmd           string
		writes, reads []string
	}{
		{cmd: "ls -la"},
		{cmd: "echo x > out.txt", writes: []string{"out.txt"}},
		{cmd: "echo x>out.txt", writes: []string{"out.txt"}},
		{cmd: "echo x >> log/a.log", writes: []string{"log/a.log"}},
		{cmd: "echo x >| .env", writes: []string{".env"}},
		{cmd: "go test ./... 2> err.txt", writes: []string{"err.txt"}},
		{cmd: "go test ./... &> all.txt", writes: []string{"all.txt"}},
		{cmd: "go test ./... 2>&1 | tail -20"},
		{cmd: "echo x > /dev/null 2> /dev/stderr"},
		{cmd: "tee -a a.txt b.txt", writes: []string{"a.txt", "b.txt"}},
		{cmd: "rm -rf build dist", writes: []string{"build", "dist"}},
		{cmd: "mv a.go b.go", writes: []string{"a.go", "b.go"}},
		{cmd: "cp src/a.go ./b.go", writes: []string{"src/a.go", "./b.go"}, reads: []string{"src/a.go", "./b.go"}},
		{cmd: "sed -i s/a/b/ x_test.go", writes: []string{"x_test.go"}},
		{cmd: "sed -i.bak -e s/a/b/ x_test.go", writes: []string{"x_test.go"}},
		{cmd: "sed -ni s/a/b/p x_test.go", writes: []string{"x_test.go"}},
		{cmd: "sed --in-place s/a/b/ x_test.go", writes: []string{"x_test.go"}},
		{cmd: "sed -n s/a/b/p x_test.go"},
		{cmd: "sed -e s/i/j/ x_test.go"},
		{cmd: "perl -pi -e s/a/b/ x_test.go", writes: []string{"x_test.go"}},
		{cmd: "perl -Mstrict -e print x_test.go"},
		{cmd: "/usr/bin/touch new.txt", writes: []string{"new.txt"}},
		{cmd: "true && touch a ; touch b || touch c | touch d", writes: []string{"a", "b", "c", "d"}},
		{cmd: "sleep 1 & rm x", writes: []string{"x"}},
		{cmd: "(rm x)", writes: []string{"x"}},
		{cmd: "echo $(rm y)", writes: []string{"y"}},
		{cmd: "env FOO=1 rm z", writes: []string{"z"}},
		{cmd: "FOO=1 BAR=2 nohup rm z", writes: []string{"z"}},
		{cmd: "cat ~/.ssh/id_rsa", reads: []string{"~/.ssh/id_rsa"}},
		{cmd: "cat README", reads: nil},
		{cmd: "grep -r key ./src /etc/hosts", reads: []string{"./src", "/etc/hosts"}},
		{cmd: "sort < ~/.aws/credentials", reads: []string{"~/.aws/credentials"}},
		{cmd: "cat <<EOF"},
		{cmd: "diff <(ls) <(ls -a)"},
		{cmd: "command cat .env", reads: []string{".env"}},
		{cmd: "", reads: nil},
	}
	for _, c := range cases {
		cmd := normalizeCommand(c.cmd)
		if got := shellWrites(cmd); !reflect.DeepEqual(got, c.writes) {
			t.Errorf("shellWrites(%q) = %q, want %q", c.cmd, got, c.writes)
		}
		if got := shellReads(cmd); !reflect.DeepEqual(got, c.reads) {
			t.Errorf("shellReads(%q) = %q, want %q", c.cmd, got, c.reads)
		}
	}
}

func TestPolicyDecideRules(t *testing.T) {
	p := DefaultPolicy()
	p.Home, p.Worktree = "/home/u", "/w/I-1"
	exists := func(rel string) bool { return rel == "app/x_test.go" }
	cases := []struct {
		c    ToolCall
		rule string
	}{
		{ToolCall{Tool: "Write", Paths: []string{"~/.config/barnraise/config.yaml"}, Write: true}, "self"},
		{ToolCall{Tool: "Read", Paths: []string{"~/.ssh/id_rsa"}}, "deny_read"},
		{ToolCall{Tool: "Read", Paths: []string{"keys/release.jks"}}, "deny_read"},
		{ToolCall{Tool: "Write", Paths: []string{"/tmp/x"}, Write: true}, "outside_worktree"},
		{ToolCall{Tool: "Write", Paths: []string{"barnraise.yaml"}, Write: true}, "deny_write"},
		{ToolCall{Tool: "Edit", Paths: []string{"app/x_test.go"}, Write: true}, "locked_existing"},
		{ToolCall{Tool: "Bash", Command: "sudo ls"}, "deny_command"},
		{ToolCall{Tool: "Bash", Command: "git push --force origin main"}, "deny_command"},
		{ToolCall{Tool: "Bash", Command: "echo x 2> .env"}, "deny_write"},
		{ToolCall{Tool: "Bash", Command: "echo x &> .barnraise/state"}, "deny_write"},
		{ToolCall{Tool: "Bash", Command: "perl -pi -e s/a/b/ app/x_test.go"}, "locked_existing"},
		{ToolCall{Tool: "Bash", Command: "true & rm -rf .git"}, "deny_write"},
		{ToolCall{Tool: "Bash", Command: "env A=1 rm .gitlab-ci.yml"}, "deny_write"},
		{ToolCall{Tool: "Bash", Command: "sort < ~/.aws/credentials"}, "deny_read"},
		{ToolCall{Tool: "Bash", Command: "cat\t~/.netrc"}, "deny_read"},
		{ToolCall{Tool: "Bash", Command: "go test 2> /tmp/log"}, "outside_worktree"},
		{ToolCall{Tool: "Read"}, ""},
		{ToolCall{Tool: "Write", Paths: []string{"app/x_test.go"}, Write: true}, "locked_existing"},
		{ToolCall{Tool: "Write", Paths: []string{"app/y_test.go"}, Write: true}, ""},
		{ToolCall{Tool: "Write", Paths: []string{"/w/I-1"}, Write: true}, ""},
		{ToolCall{Tool: "Bash", Command: "go test 2>&1 >/dev/null"}, ""},
		{ToolCall{Tool: "Bash", Command: "cat <<EOF"}, ""},
	}
	for _, c := range cases {
		d := p.Decide(c.c, exists)
		if c.rule == "" {
			if !d.Allow {
				t.Errorf("%+v denied by %s: %s", c.c, d.Rule, d.Reason)
			}
			continue
		}
		if d.Allow || d.Rule != c.rule {
			t.Errorf("%+v: got allow=%v rule=%q, want rule %q", c.c, d.Allow, d.Rule, c.rule)
		}
	}
	// Without a worktree paths are taken as relative to it; nil exists never locks.
	bare := DefaultPolicy()
	if d := bare.Decide(ToolCall{Tool: "Write", Paths: []string{"./app/x_test.go"}, Write: true}, nil); !d.Allow {
		t.Fatal(d.Reason)
	}
	if d := bare.Decide(ToolCall{Tool: "Write", Paths: []string{"./.github/workflows/ci.yml"}, Write: true}, nil); d.Allow {
		t.Fatal("relative workflow write")
	}
	// Invalid patterns are skipped, not fatal.
	bad := Policy{DenyCommands: []string{"(", "rm"}}
	if bad.Decide(ToolCall{Command: "ls"}, nil).Allow != true || bad.Decide(ToolCall{Command: "rm x"}, nil).Allow {
		t.Fatal("invalid regex handling")
	}
	if bad.Decide(ToolCall{Command: "ls"}, nil).Allow != true {
		t.Fatal("cached invalid regex")
	}
	if got := (&Policy{Home: "/h"}).expand("~"); got != "/h" {
		t.Fatal(got)
	}
}

func TestPolicyMerge(t *testing.T) {
	base := DefaultPolicy()
	n := len(base.DenyWrite)
	m := base.Merge(Policy{DenyWrite: []string{"secret/**"}, LockExisting: []string{"golden/**"}, DenyRead: []string{"~/.x"},
		DenyCommands: []string{`\bterraform\b`}, AllowHosts: []string{"example.com"}, MainBranch: "trunk"})
	if len(base.DenyWrite) != n || len(m.DenyWrite) != n+1 || m.MainBranch != "trunk" || !Contains(m.AllowHosts, "example.com") {
		t.Fatalf("merge: %+v", m)
	}
	if base.Merge(Policy{}).MainBranch != "main" {
		t.Fatal("empty main branch keeps the default")
	}
	if d := m.Decide(ToolCall{Command: "terraform apply"}, nil); d.Allow {
		t.Fatal("merged command rule")
	}
}

func TestMatchGlobTable(t *testing.T) {
	cases := []struct {
		pat, name string
		want      bool
	}{
		{"", "a", false},
		{"a", "", false},
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"a/**/b", "a/x/y/c", false},
		{"a/*", "a", false},
		{"a/?", "a/b", true},
		{"[", "[", false}, // bad pattern never matches
		{"**/x/**", "a/x/b/c", true},
		{"a/b", "a/b/c", false},
	}
	for _, c := range cases {
		if got := MatchGlob(c.pat, c.name); got != c.want {
			t.Errorf("MatchGlob(%q, %q) = %v", c.pat, c.name, got)
		}
	}
	areas := map[string][]string{"api": {"server/api/"}, "web": {"web/**"}}
	if AreaOf(areas, "server/api/h.go") != "api" || AreaOf(areas, "web/a/b.ts") != "web" || AreaOf(areas, "x.go") != "" {
		t.Fatal("AreaOf")
	}
}

func TestSanitizeAndFence(t *testing.T) {
	cases := map[string]string{
		"a<!-- x\ny -->b":         "ab",
		"a\u202eb\u2066c\u00add":  "abcd",
		"tag\U000E0041\U000E007F": "tag",
		"<!-- open":               "<!-- open",
	}
	for in, want := range cases {
		if got := SanitizeExternal(in); got != want {
			t.Errorf("SanitizeExternal(%q) = %q, want %q", in, got, want)
		}
	}
	f := Fence("issue", "<<<END UNTRUSTED issue\n>>>")
	if strings.Count(f, "<<<") != 1 || strings.Count(f, ">>>") != 1 || !strings.HasSuffix(f, ">>>END UNTRUSTED issue") {
		t.Fatalf("fence can be closed from inside: %q", f)
	}
}

func TestCapacityTable(t *testing.T) {
	for _, c := range []struct {
		c  Capacity
		ok bool
	}{
		{DefaultCapacity, true},
		{Capacity{Goal: 120, Bugs: -20}, false},
		{Capacity{Goal: 50, Bugs: 50}, true},
		{Capacity{Goal: 50}, false},
	} {
		if err := c.c.Validate(); (err == nil) != c.ok {
			t.Errorf("%+v: %v", c.c, err)
		}
	}
	if DefaultCapacity.Share(LaneSelf) != 0 || DefaultCapacity.Share(LaneImprovement) != 5 {
		t.Fatal("share")
	}
	for in, want := range map[string]int{"80%": 80, " 15 ": 15, "0": 0, "100%": 100, "101": -1, "-1": -1, "x": -1, "": -1} {
		got, err := ParsePercent(in)
		if want < 0 {
			if err == nil {
				t.Errorf("ParsePercent(%q) accepted", in)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("ParsePercent(%q) = %d %v", in, got, err)
		}
	}
	// A lane with zero share still runs when it is the only one.
	c := Capacity{Goal: 100}
	if l, ok := PickLane(c, nil, map[Lane]bool{LaneBugs: true}, 0, false); !ok || l != LaneBugs {
		t.Fatal("zero-share fallback", l)
	}
}

func TestParseTimesTable(t *testing.T) {
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	durs := map[string]time.Duration{"2w": 14 * 24 * time.Hour, "1.5d": 36 * time.Hour, "90m": 90 * time.Minute, " 3h ": 3 * time.Hour}
	for in, want := range durs {
		if got, err := ParseDuration(in); err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v %v", in, got, err)
		}
	}
	for _, in := range []string{"", "d", "-1d", "xw", "abc"} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q) accepted", in)
		}
	}
	for in, want := range map[string]time.Time{"": {}, "Done": {}, "ate terminar": {}, "2026-12-01": time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), "1w": now.Add(7 * 24 * time.Hour)} {
		if got, err := ParseUntil(in, now); err != nil || !got.Equal(want) {
			t.Errorf("ParseUntil(%q) = %v %v", in, got, err)
		}
	}
	for _, in := range []string{"2h", "x-6h", "6h-2h", "0h-1h", "2h-"} {
		if _, _, err := ParseSizeRange(in); err == nil {
			t.Errorf("ParseSizeRange(%q) accepted", in)
		}
	}
	if JudgeSize(-1, 2, 6) != SizeUnknown || JudgeSize(1, 2, 6) != SizeOK || JudgeSize(9, 2, 6) != SizeOK {
		t.Fatal("JudgeSize bounds")
	}
}

func TestFocusTable(t *testing.T) {
	now := time.Now()
	var nilFocus *Focus
	it := &Item{ID: "I-1", Area: "cards"}
	if nilFocus.Includes(it) || nilFocus.Active(nil, now) {
		t.Fatal("nil focus")
	}
	f := &Focus{Items: []string{"I-1"}}
	if !f.Includes(it) || f.Includes(&Item{ID: "I-2"}) {
		t.Fatal("focus items")
	}
	if (&Focus{Until: now.Add(time.Hour)}).Active(nil, now) != true || (&Focus{Until: now.Add(-time.Hour)}).Active(nil, now) {
		t.Fatal("until")
	}
	if (&Focus{Items: []string{"I-1"}, Cancelled: true}).Active([]*Item{it}, now) {
		t.Fatal("cancelled")
	}
	p := ComputeProgress([]*Item{{GoalID: "G-1", Status: StatusClosed, ClosedReason: "done_by", IntegratedAt: now}, {GoalID: "G-1"}, {GoalID: "G-2"}}, "G-1", now, 24*time.Hour)
	if p.Total != 2 || p.Done != 1 || p.PerDay != 1 || p.Forecast.IsZero() {
		t.Fatalf("%+v", p)
	}
	if p := ComputeProgress(nil, "G-1", now, 0); p.Total != 0 || !p.Forecast.IsZero() {
		t.Fatalf("%+v", p)
	}
}

func testConfig() *ProjectConfig {
	return &ProjectConfig{
		Name:     "app",
		Approves: "owner",
		Tracker:  TrackerDef{Type: "local"},
		Capacity: DefaultCapacity,
		Roles:    map[string]RoleDef{"implementer": {}, "critic": {}, "designer": {}, "analyst": {}},
		Flows:    map[string]*Flow{"feature": featureFlow(), "bug": {Name: "bug", Steps: []StepDef{{Name: "fix", Type: StepAgent, Role: "implementer"}}}},
		Checks:   map[string]CheckDef{"main": {RunsOn: "local", Run: "make test"}},
		Areas:    map[string]AreaDef{"cards": {Paths: []string{"app/cards/**"}}},
		KindFlows: map[string]string{
			"hotfix": "bug",
		},
	}
}

func TestProjectConfig(t *testing.T) {
	c := testConfig()
	if errs := c.Validate(); len(errs) != 0 {
		t.Fatalf("valid config: %v", errs)
	}
	for kind, want := range map[string]string{"feature": "feature-mobile", "hotfix": "bug", "": "feature-mobile"} {
		f, err := c.FlowFor(kind)
		if err != nil || f.Name != want {
			t.Errorf("FlowFor(%q) = %v %v", kind, f, err)
		}
	}
	if _, err := c.FlowFor("blog"); err == nil || !strings.Contains(err.Error(), "[bug feature]") {
		t.Fatalf("unknown flow: %v", err)
	}
	if c.AreaOfFile("app/cards/X.kt") != "cards" || c.AreaOfFile("README.md") != "" {
		t.Fatal("AreaOfFile")
	}
	if TrimChecks("checks.main") != "main" || TrimChecks("main") != "main" {
		t.Fatal("TrimChecks")
	}

	bad := testConfig()
	bad.Name, bad.Approves, bad.Tracker.Type = "", "nobody", "trello"
	bad.Capacity = Capacity{Goal: 10}
	bad.Lease = LeaseDef{Renew: time.Minute, TTL: time.Minute}
	bad.Roles = map[string]RoleDef{}
	bad.Flows["broken"] = &Flow{Name: "broken"}
	bad.Flows["refs"] = &Flow{Name: "refs", Steps: []StepDef{
		{Name: "ok", Type: StepApproval, Approver: "ghost"},
		{Name: "c", Type: StepCheck, Run: "checks.lint"},
		{Name: "all", Type: StepCheck, Run: "checks.all"},
	}}
	bad.Checks = map[string]CheckDef{
		"a": {RunsOn: "local"}, "b": {RunsOn: "machine:gpu"}, "c": {RunsOn: "github-actions"},
		"d": {RunsOn: "webhook"}, "e": {RunsOn: "cloud"},
	}
	errs := bad.Validate()
	text := ""
	for _, e := range errs {
		text += e.Error() + "\n"
	}
	for _, want := range []string{"no name", "add up to 10%", "has no steps", "not declared in roles", "neither owner", "check \"lint\"",
		"check a runs on local", "check b runs on machine:gpu", "names no workflow", "has no url", "runs_on \"cloud\"", "approves must be", "tracker type", "lease renew"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if errs := (&ProjectConfig{Approves: "analyst", Tracker: TrackerDef{Type: "github"}, Capacity: DefaultCapacity, Name: "x"}).Validate(); len(errs) != 1 || !strings.Contains(errs[0].Error(), "no flows") {
		t.Fatalf("no flows: %v", errs)
	}
}

func TestFlowTable(t *testing.T) {
	f := featureFlow()
	if f.First() != "plan" {
		t.Fatal("first")
	}
	if s, ok := f.Step("check"); !ok || s.Type != StepCheck {
		t.Fatal("step")
	}
	if _, ok := f.Step("nope"); ok {
		t.Fatal("missing step")
	}
	if f.RedoFrom("implement") != nil || !errors.Is(f.RedoFrom("nope"), ErrUnknownStep) {
		t.Fatal("redo")
	}
	if _, err := f.Decide(&Item{}, "nope", OutcomeDone, ""); !errors.Is(err, ErrUnknownStep) {
		t.Fatal("decide unknown")
	}
	steps := func(s ...StepDef) []StepDef { return s }
	agent := StepDef{Name: "a", Type: StepAgent, Role: "r"}
	bad := []*Flow{
		{Name: "x", Steps: steps(StepDef{Type: StepAgent, Role: "r"})},
		{Name: "x", Steps: steps(agent, agent)},
		{Name: "x", Steps: steps(StepDef{Name: "a", Type: StepAgent})},
		{Name: "x", Steps: steps(StepDef{Name: "a", Type: StepApproval})},
		{Name: "x", Steps: steps(StepDef{Name: "a", Type: StepScript})},
		{Name: "x", Steps: steps(StepDef{Name: "a", Type: "robot"})},
		{Name: "x", Steps: steps(StepDef{Name: "a", Type: StepAgent, Role: "r", ModelFamily: "different_from(b)"})},
		{Name: "x", Steps: steps(agent), Rework: map[string]string{"a": "a"}},
		{Name: "x", Steps: steps(agent), Rework: map[string]string{"z.fail": "a"}},
		{Name: "x", Steps: steps(agent), Rework: map[string]string{"a.fail": "z"}},
		{Name: "x", Steps: steps(agent, StepDef{Name: "b", Type: StepMergeQueue}), Rework: map[string]string{"a.fail": "b"}},
		{Name: "x", Steps: steps(agent), Limit: ReworkLimit{SameModel: -1}},
	}
	for i, b := range bad {
		if b.Validate() == nil {
			t.Errorf("bad flow %d accepted", i)
		}
	}
	// reject and rework share a rule; "needs context" and "blocked" stay on the step.
	g := &Flow{Name: "g", Steps: steps(agent, StepDef{Name: "r", Type: StepAgent, Role: "c", Review: true}), Rework: map[string]string{"r.rework": "a"}, Limit: DefaultReworkLimit}
	if g.ReworkTarget("r", OutcomeReject) != "a" {
		t.Fatal("reject uses rework rule")
	}
	g.Rework = map[string]string{"r.reject": "a"}
	if g.ReworkTarget("r", OutcomeRework) != "a" {
		t.Fatal("rework uses reject rule")
	}
	if tr, _ := g.Decide(&Item{}, "a", OutcomeFail, "x"); tr.Kind != TransOwner {
		t.Fatal("nothing earlier goes to the owner")
	}
	if tr, _ := g.Decide(&Item{}, "r", OutcomeBlocked, "x"); tr.Kind != TransBlocked || tr.NextStep != "r" {
		t.Fatal("blocked")
	}
	if tr, _ := g.Decide(&Item{}, "r", OutcomeNeedsContext, "x"); tr.Kind != TransOwner {
		t.Fatal("needs context")
	}
}

func TestDeliveryReviewTable(t *testing.T) {
	it := &Item{ID: "I-1", Criteria: []Criterion{{ID: "C1"}}}
	cases := []struct {
		d  Delivery
		ok bool
	}{
		{Delivery{Status: "WHATEVER", Summary: "s"}, false},
		{Delivery{Status: DeliveryDone}, false},
		{Delivery{Status: DeliveryBlocked, Summary: "s"}, false},
		{Delivery{Status: DeliveryNeedsContext, Summary: "s", Reason: "r"}, true},
		{Delivery{Status: DeliveryConcerns, Summary: "s"}, false},
		{Delivery{Status: DeliveryConcerns, Summary: "s", Concerns: []string{"c"}}, true},
		{Delivery{Status: DeliveryDone, Summary: "s", Criteria: []CriterionMark{{ID: "C9"}}}, false},
		{Delivery{Status: DeliveryDone, Summary: "s", Criteria: []CriterionMark{{ID: "C1", Met: true}}}, false},
		{Delivery{Status: DeliveryDone, Summary: "s", Criteria: []CriterionMark{{ID: "C1", Met: true, Evidence: "test"}}}, true},
	}
	for i, c := range cases {
		if err := c.d.Validate(it); (err == nil) != c.ok {
			t.Errorf("delivery %d: %v", i, err)
		}
	}
	for s, o := range map[DeliveryStatus]Outcome{DeliveryDone: OutcomeDone, DeliveryConcerns: OutcomeConcerns, DeliveryNeedsContext: OutcomeNeedsContext, DeliveryBlocked: OutcomeBlocked} {
		if got, err := s.Outcome(); err != nil || got != o {
			t.Errorf("%s -> %s %v", s, got, err)
		}
	}
	f := []Finding{{Text: "x"}}
	reviews := []struct {
		r  Review
		ok bool
	}{
		{Review{Criteria: "maybe"}, false},
		{Review{Criteria: VerdictPass, Code: "maybe"}, false},
		{Review{Criteria: VerdictPass, Code: VerdictFail, Decision: ReviewApprove, Summary: "s"}, false},
		{Review{Criteria: VerdictPass, Code: VerdictPass, Decision: ReviewApprove, Summary: "s"}, true},
		{Review{Criteria: VerdictPass, Code: VerdictPass, Decision: ReviewApprove}, false},
		{Review{Criteria: VerdictFail, Code: VerdictPass, Decision: ReviewRework, Summary: "s"}, false},
		{Review{Criteria: VerdictFail, Code: VerdictPass, Decision: ReviewReject, Summary: "s", Findings: f}, true},
		{Review{Criteria: VerdictFail, Code: VerdictPass, Decision: "MAYBE", Summary: "s"}, false},
	}
	for i, c := range reviews {
		if err := c.r.Validate(); (err == nil) != c.ok {
			t.Errorf("review %d: %v", i, err)
		}
	}
	for d, o := range map[ReviewDecision]Outcome{ReviewApprove: OutcomeApprove, ReviewRework: OutcomeRework, ReviewReject: OutcomeReject} {
		if (&Review{Decision: d}).Outcome() != o {
			t.Errorf("review outcome %s", d)
		}
	}
	got := FormatFindings([]Finding{{Severity: "major", File: "a.go", Line: 3, Text: "bad"}, {File: "b.go", Text: "meh"}, {Text: "plain"}})
	if got != "- [major] a.go:3: bad\n- b.go: meh\n- plain\n" {
		t.Fatalf("%q", got)
	}
	it.Criteria = append(it.Criteria, Criterion{ID: "C2", Check: "e2e"}, Criterion{ID: "C3", Check: "unit"})
	if m := CriteriaCoverage(it, func(c string) bool { return c == "unit" }); !reflect.DeepEqual(m, []string{"C1", "C2"}) {
		t.Fatal(m)
	}
	same, other := SplitFindings(&Item{Area: "cards"}, []Finding{{File: "app/home/x"}, {Area: "A-cards"}, {}}, func(string) string { return "home" })
	if len(same) != 2 || len(other) != 1 || other[0].Area != "home" {
		t.Fatal("split")
	}
	if p, fl := FlakyVerdict([]bool{false}, nil); p || fl {
		t.Fatal("red only on the branch fails")
	}
	if p, fl := FlakyVerdict([]bool{false}, []bool{true, false}); !p || !fl {
		t.Fatal("main oscillates")
	}
}

func TestItemTable(t *testing.T) {
	for in, want := range map[string]Priority{"P0": P0, "p1": P1, " 2 ": P2, "": P2, "3": P3} {
		if got, err := ParsePriority(in); err != nil || got != want {
			t.Errorf("ParsePriority(%q) = %v %v", in, got, err)
		}
	}
	if _, err := ParsePriority("P9"); err == nil || P1.String() != "P1" {
		t.Fatal("priority")
	}
	if !(Anchors{Base: "abc"}).Empty() || (Anchors{Files: []string{"a"}}).Empty() {
		t.Fatal("anchors empty")
	}
	items := map[string]*Item{"I-1": {Status: StatusIntegrated}, "I-2": {Status: StatusClosed, ClosedReason: "obsolete"}, "I-3": {Status: StatusClosed, ClosedReason: "abandoned"}, "I-4": {Status: StatusReady}}
	look := func(id string) *Item { return items[id] }
	for deps, want := range map[string]bool{"": true, "I-1,I-2,I-9": true, "I-3": false, "I-4": false} {
		it := &Item{}
		if deps != "" {
			it.Deps = strings.Split(deps, ",")
		}
		if it.DepsSatisfied(look) != want {
			t.Errorf("deps %q", deps)
		}
	}
	now := time.Now()
	list := []*Item{{ID: "I-10", Priority: P2, CreatedAt: now}, {ID: "I-2", Priority: P2, CreatedAt: now}, {ID: "I-3", Priority: P0, CreatedAt: now.Add(time.Hour)}, {ID: "I-1", Priority: P2, CreatedAt: now.Add(-time.Hour)}}
	SortItems(list)
	var ids []string
	for _, it := range list {
		ids = append(ids, it.ID)
	}
	if strings.Join(ids, ",") != "I-3,I-1,I-2,I-10" {
		t.Fatal(ids)
	}
	if LaneFor("bug", P0, true, true) != LaneSelf || LaneFor("bug", P0, true, false) != LaneBugs || LaneFor("feature", P2, true, false) != LaneGoal || LaneFor("feature", P2, false, false) != LaneImprovement {
		t.Fatal("lanes")
	}
	if BugNeedsProposal(P1) || !BugNeedsProposal(P2) {
		t.Fatal("bug proposal")
	}
	src := &Item{ID: "I-1", Claim: &Claim{Machine: "m"}, Runs: map[string]StepRun{"a": {Model: "x"}}, Anchors: Anchors{Files: []string{"f"}}}
	c := src.Clone()
	c.Claim.Machine, c.Runs["a"], c.Anchors.Files[0] = "n", StepRun{Model: "y"}, "g"
	if src.Claim.Machine != "m" || src.Runs["a"].Model != "x" || src.Anchors.Files[0] != "f" {
		t.Fatal("clone shares memory")
	}
	r := &Item{Status: StatusDraft, Criteria: []Criterion{{ID: "C1", Text: "WHEN x, THE SYSTEM SHALL y", Check: "t"}}}
	r.RefreshReadiness(true)
	if r.Status != StatusReady {
		t.Fatal("ready")
	}
	r.Body = "[NEEDS CLARIFICATION: which screen?"
	r.RefreshReadiness(true)
	if r.Status != StatusDraft || len(FindClarifications(r.Body)) != 1 {
		t.Fatal("draft again")
	}
	r.Status = StatusInProgress
	r.RefreshReadiness(true)
	if r.Status != StatusInProgress {
		t.Fatal("refresh only moves draft and ready")
	}
	if (&Item{Status: StatusClosed}).Readiness(false) != ErrTerminal {
		t.Fatal("terminal")
	}
	if !errors.Is((&Item{Criteria: []Criterion{{ID: "C1"}}}).Readiness(false), ErrCriterionNoCheck) {
		t.Fatal("no check")
	}
	if (&Item{Criteria: []Criterion{{Text: "[NEEDS CLARIFICATION: x]"}}}).Readiness(false) != ErrOpenClarifying {
		t.Fatal("criterion clarification")
	}
	if (&Item{Criteria: []Criterion{{Met: true}, {}}}).AllCriteriaMet() {
		t.Fatal("all met")
	}
	if got := PredictedFiles(&Item{FilesTouched: []string{"a"}, Anchors: Anchors{Files: []string{"b"}}}); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatal(got)
	}
	if got := PredictedFiles(&Item{Anchors: Anchors{Files: []string{"b"}}}); !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatal(got)
	}
}

func TestIDsTable(t *testing.T) {
	for _, bad := range []string{"", "-1", "X-1", "I-0", "I-x", "I1", "G-1"} {
		if _, err := NormalizeItemID(bad); err == nil {
			t.Errorf("NormalizeItemID(%q) accepted", bad)
		}
	}
	if AreaID("cards") != "A-cards" || AreaID("A-cards") != "A-cards" || AreaName("A-cards") != "cards" {
		t.Fatal("area id")
	}
	if got := Slug("Ação rápida: Ñandú ÉÈ ÕÜÇ!", 100); got != "acao-rapida-nandu-ee-ouc" {
		t.Fatal(got)
	}
	if got := Slug("abcdefghij", 4); got != "abcd" {
		t.Fatal(got)
	}
	if BranchName("I-1", "!!!") != "br/I-1" || BranchName("I-1", "Fix it") != "br/I-1-fix-it" {
		t.Fatal("branch")
	}
	if CommitTrailer("I-7") != "Barnraise-Item: I-7" {
		t.Fatal("trailer")
	}
}

func TestStateProjections(t *testing.T) {
	s := NewState("p")
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	apply := func(id, typ string, p Payload) {
		t.Helper()
		if err := s.Apply(NewEvent(id, typ, "p", "", "m", "", now, p)); err != nil {
			t.Fatal(err)
		}
	}
	apply("1", EvItemCreated, Payload{Item: &Item{ID: "I-2", Priority: P1}})
	apply("2", EvItemCreated, Payload{Item: &Item{ID: "I-1", Priority: P0}})
	apply("3", EvProposalCreated, Payload{Proposal: &Proposal{ID: "P-10", Version: 2}})
	apply("4", EvProposalUpdated, Payload{Proposal: &Proposal{ID: "P-10", Version: 1, Title: "old"}})
	apply("5", EvProposalCreated, Payload{Proposal: &Proposal{ID: "P-9"}})
	apply("6", EvGoalSet, Payload{Goal: &Goal{ID: "G-1", Active: true}})
	apply("7", EvGoalSet, Payload{Goal: &Goal{ID: "G-2", Active: true}})
	apply("8", EvFocusChanged, Payload{Focus: &Focus{Name: "f"}})
	apply("9", EvTweakAdded, Payload{Tweak: &Tweak{ID: "t2", CreatedAt: now.Add(time.Second)}})
	apply("10", EvTweakAdded, Payload{Tweak: &Tweak{ID: "t1", CreatedAt: now}})
	apply("11", EvOwnerQuestionAsked, Payload{Question: &Question{ID: "Q-2", AskedAt: now.Add(time.Second)}})
	apply("12", EvOwnerQuestionAsked, Payload{Question: &Question{ID: "Q-1", AskedAt: now}})
	apply("13", EvOwnerQuestionAnswer, Payload{Question: &Question{ID: "Q-3", Answer: "yes"}})
	apply("14", EvAccountUpdated, Payload{Account: &Account{ID: "codex-1"}})
	apply("15", EvAccountUpdated, Payload{Account: &Account{ID: "claude-1"}})
	apply("16", EvMachineUpdated, Payload{MachineI: &Machine{Name: "m1", LastSeen: now}})
	apply("17", EvMachineUpdated, Payload{MachineI: &Machine{Name: "m1", LastSeen: now.Add(-time.Hour), Version: "old"}})
	apply("18", EvMachineLeft, Payload{MachineI: &Machine{Name: "m1", LastSeen: now.Add(-time.Hour), Left: true}})
	apply("19", EvAgentSession, Payload{Session: &SessionInfo{Agent: "a1", State: "working"}})
	apply("20", EvSystemResumed, Payload{})
	for i := range journalWindow + 5 {
		apply("j"+FormatID(KindItem, i+1), EvJournal, Payload{Journal: &JournalEntry{Text: "x"}})
	}
	if err := s.Apply(Event{ID: "bad", Data: json.RawMessage("{")}); err == nil {
		t.Fatal("bad payload must fail")
	}
	if l := s.ItemList(); l[0].ID != "I-1" || len(l) != 2 {
		t.Fatal("ItemList")
	}
	if l := s.ProposalList(); l[0].ID != "P-9" || l[1].Title != "" {
		t.Fatal("ProposalList")
	}
	if g := s.ActiveGoal(); g == nil || g.ID != "G-2" || s.Goals["G-1"].Active {
		t.Fatal("ActiveGoal")
	}
	if s.Focus == nil || s.Focus.Name != "f" {
		t.Fatal("focus")
	}
	if l := s.TweakList(); l[0].ID != "t1" {
		t.Fatal("TweakList")
	}
	if q := s.OpenQuestions(); len(q) != 2 || q[0].ID != "Q-1" {
		t.Fatal("OpenQuestions")
	}
	if a := s.AccountList(); a[0].ID != "claude-1" {
		t.Fatal("AccountList")
	}
	if m := s.Machines["m1"]; !m.Left || m.Health(now, time.Minute) != "gray" {
		t.Fatal("machine left")
	}
	if len(s.Journal) != journalWindow || s.Sessions["a1"].State != "working" || s.Paused {
		t.Fatal("journal, sessions, pause")
	}
	if s.NextID(KindQuestion) != "Q-4" || s.NextID(KindProposal) != "P-11" {
		t.Fatal(s.NextID(KindQuestion), s.NextID(KindProposal))
	}
	for i := range messageWindow + 3 {
		apply("msg"+FormatID(KindItem, i+1), EvMessageSent, Payload{Message: &Message{ID: FormatID(KindItem, i+1), To: "item:I-1"}})
	}
	if len(s.Messages) != messageWindow || len(s.Inbox("x", "", "I-1")) != messageWindow {
		t.Fatal("messages window")
	}
	if p, err := (Event{}).DecodePayload(); err != nil || p.Item != nil {
		t.Fatal("empty payload")
	}
	if (TokenUsage{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4}).Total() != 10 {
		t.Fatal("total")
	}
	m := &Machine{GPU: true, Labels: []string{"arm"}}
	if !m.HasLabel("gpu") || !m.HasLabel("arm") || m.HasLabel("emulator") {
		t.Fatal("labels")
	}
}

// Same version, same time, different content: every order keeps the same snapshot.
func TestStateTieBreakIsOrderIndependent(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	a := NewEvent("a", EvItemClaimed, "p", "I-1", "m1", "", now, Payload{Item: &Item{ID: "I-1", Version: 2, UpdatedAt: now, Claim: &Claim{Machine: "m1"}}})
	b := NewEvent("b", EvItemClaimed, "p", "I-1", "m2", "", now, Payload{Item: &Item{ID: "I-1", Version: 2, UpdatedAt: now, Claim: &Claim{Machine: "m2"}}})
	s1, s2 := NewState("p"), NewState("p")
	_, _ = s1.Apply(a), s1.Apply(b)
	_, _ = s2.Apply(b), s2.Apply(a)
	if s1.Items["I-1"].Claim.Machine != s2.Items["I-1"].Claim.Machine {
		t.Fatal("tie depends on delivery order")
	}
	later := NewEvent("c", EvItemUpdated, "p", "I-1", "m1", "", now, Payload{Item: &Item{ID: "I-1", Version: 2, UpdatedAt: now.Add(time.Second), Claim: &Claim{Machine: "m3"}}})
	_ = s1.Apply(later)
	if s1.Items["I-1"].Claim.Machine != "m3" {
		t.Fatal("later update of the same version wins")
	}
}

func TestImpactTable(t *testing.T) {
	items := []*Item{
		{ID: "I-1", Area: "cards", Anchors: Anchors{Files: []string{"app/cards/"}, Contracts: []string{"api.yaml"}, Decisions: []string{"D-1"}, Specs: []string{"S-1"}}, Deps: []string{"I-9"}},
		{ID: "I-2", Anchors: Anchors{Areas: []string{"A-home"}}},
		{ID: "I-3", Status: StatusIntegrated, Anchors: Anchors{Files: []string{"x"}}},
	}
	got := FindImpacted(Change{Kind: "decision", Files: []string{"app/cards/a.kt"}, Contracts: []string{"api.yaml"}, Decisions: []string{"D-1"}, Specs: []string{"S-1"}, Items: []string{"I-9"}, Areas: []string{"home"}}, items, nil)
	if len(got) != 2 || got[0].ItemID != "I-1" || got[0].Score != 12 || got[1].ItemID != "I-2" {
		t.Fatalf("%+v", got)
	}
	tr := []struct {
		r  TriageResult
		ok bool
	}{
		{TriageResult{Verdict: TriageStillValid, Reason: "r"}, true},
		{TriageResult{Verdict: TriageStillValid}, false},
		{TriageResult{Verdict: TriageNeedsUpdate, Reason: "r"}, false},
		{TriageResult{Verdict: TriageNeedsUpdate, Reason: "r", Anchors: &Anchors{}}, true},
		{TriageResult{Verdict: TriageDuplicate, Reason: "r"}, false},
		{TriageResult{Verdict: TriageNewDependency, Reason: "r", Target: "I-2"}, true},
		{TriageResult{Verdict: "maybe", Reason: "r"}, false},
	}
	for i, c := range tr {
		if err := c.r.Validate(); (err == nil) != c.ok {
			t.Errorf("triage %d: %v", i, err)
		}
	}
	if !reflect.DeepEqual(Dedupe([]string{"a", "b", "a"}), []string{"a", "b"}) {
		t.Fatal("dedupe")
	}
	if RecallAtK(nil, nil, 3) != 1 || RecallAtK([]string{"a"}, []string{"a"}, 5) != 1 {
		t.Fatal("recall")
	}
	kept, cut := FitBudget([]ContextPart{{Order: 1, Tokens: 100}, {Order: 2, Tokens: 100, Relevance: 0.5}, {Order: 3, Tokens: 100, Relevance: 0.5}}, 150)
	if len(kept) != 1 || len(cut) != 2 {
		t.Fatal("budget smaller than order 1 keeps only order 1")
	}
}

func TestProposalTable(t *testing.T) {
	full := Proposal{Title: "t", Problem: "p", Evidence: "e", Impact: "i", Size: "s", Area: "a", GoalLink: "g"}
	if err := (&Proposal{}).Validate(); err == nil || !strings.Contains(err.Error(), "title, problem, evidence, impact, size, area, goal_link") {
		t.Fatal(err)
	}
	bad := full
	bad.Kind = "epic"
	if bad.Validate() == nil {
		t.Fatal("kind")
	}
	now := time.Now()
	if CheckDailyLimit([]*Proposal{{By: "owner"}}, "owner", now, 1) != nil || CheckDailyLimit(nil, "a", now, 0) != nil {
		t.Fatal("limit exemptions")
	}
	ps := []*Proposal{
		{ID: "P-1", Area: "cards", Status: ProposalOpen, Title: "cache quotes screen", CreatedAt: now.Add(-20 * 24 * time.Hour)},
		{ID: "P-2", Area: "A-cards", Status: ProposalLater, LaterUntil: now.Add(-time.Hour), Title: "x", CreatedAt: now.Add(-20 * 24 * time.Hour)},
		{ID: "P-3", Area: "home", Status: ProposalRejected, Title: "cache quotes screen"},
		{ID: "P-4", Area: "home", Status: ProposalOpen, CreatedAt: now},
	}
	if exp := ExpireProposals(ps, now, 14*24*time.Hour); len(exp) != 2 {
		t.Fatal("expire", len(exp))
	}
	if g := GroupProposals(ps); len(g) != 2 || g[0].Area != "cards" || len(g[0].Proposals) != 2 {
		t.Fatalf("%+v", g)
	}
	p := &Proposal{ID: "P-5", Area: "home", Title: "cache quotes screen"}
	if _, dup := FindDuplicate(p, ps, []*Item{{ID: "I-1", Area: "home", Title: "cache quotes screen"}, {ID: "I-2", Area: "home", Status: StatusClosed, Title: "cache quotes screen"}}); !dup {
		t.Fatal("item duplicate")
	}
	ts := []*Tweak{{ID: "t1", Area: "cards"}, {ID: "t2", Area: "A-cards", ItemID: "I-1"}, {ID: "t3", Area: "home"}}
	if pt := PendingTweaks(ts, "A-cards"); len(pt) != 1 || pt[0].ID != "t1" {
		t.Fatal("pending tweaks")
	}
	q := &Question{Context: "c", Decision: "d", IfNoAnswer: "wait", Options: []Option{{Label: "Yes", Recommended: true}, {Label: "No"}}}
	if q.Validate() != nil || !q.Open() || q.Recommended() != "Yes" {
		t.Fatal("question")
	}
	if (&Question{Options: []Option{{Label: "a"}}}).Recommended() != "" {
		t.Fatal("no recommended")
	}
	for _, bad := range []*Question{
		{Context: "c", Decision: "d", Options: []Option{{Label: "a"}}},
		{Context: "c", Decision: "d", Options: []Option{{Label: ""}, {Label: "b"}}},
		{Context: "c", Decision: "d", Options: []Option{{Label: "a"}, {Label: "b"}}},
		{Context: "c", Decision: "d", Options: []Option{{Label: "a", Recommended: true}, {Label: "b"}}},
	} {
		if bad.Validate() == nil {
			t.Errorf("bad question accepted: %+v", bad)
		}
	}
}

func TestModelsTable(t *testing.T) {
	r := DefaultModelRouter()
	if r.Family("unknown") != "unknown" || r.Family("codex") != "openai" {
		t.Fatal("family")
	}
	if c := r.Escalate(ModelChoice{Provider: "claude", Model: "sonnet", Effort: "low"}, 9); c.Model != "fable" || c.Effort != "high" {
		t.Fatal(c)
	}
	if c := r.Escalate(ModelChoice{Provider: "x", Model: "y"}, 1); c.Model != "y" {
		t.Fatal("unknown ladder")
	}
	if (ModelChoice{Provider: "a", Model: "b", Effort: "c"}).String() != "a/b@c" {
		t.Fatal("string")
	}
	a := &Account{Usage: Usage{FiveHourPct: 10, WeeklyPct: 40}}
	if a.Load() != 40 {
		t.Fatal("load")
	}
	if !PredictExhaustion(Usage{}, Usage{}).IsZero() {
		t.Fatal("no readings")
	}
	if Contains([]string{"a"}, "b") || !Contains([]string{"a"}, "a") {
		t.Fatal("contains")
	}
}
