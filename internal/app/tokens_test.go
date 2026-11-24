package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// The session's filter records what it saved; the step's usage carries it
// into the event log, the metrics and the timeline.
func TestFilterSavingsReachTheLedger(t *testing.T) {
	e := newEnv(t, withCfg(func(c *domain.ProjectConfig) {
		c.Outfilter = domain.OutfilterDef{Delegate: domain.DelegateOff, Keep: time.Hour, KeepBytes: 1 << 20}
	}))
	e.account("fake-1")
	it := e.ready("simple", "noisy build", "x")
	var envSeen map[string]string
	e.prov.Handle(func(ctx context.Context, spec ports.SessionSpec, _ func(ports.ProviderEvent)) (ports.SessionResult, error) {
		envSeen = spec.Env
		dir := filepath.Join(spec.Env["BARNRAISE_WORKTREE"], ".barnraise-session", domain.OutputsDir)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return ports.SessionResult{}, err
		}
		var lines []string
		for _, r := range []domain.OutputRecord{
			{ID: "o00000001", Run: spec.Env[domain.EnvRun], Tool: "go", RawTokens: 5000, ShownTokens: 200},
			{ID: "o00000002", Run: spec.Env[domain.EnvRun], Tool: "git-diff", RawTokens: 900, ShownTokens: 300},
			{ID: "o00000003", Run: "an-earlier-session", Tool: "go", RawTokens: 7000, ShownTokens: 100},
		} {
			b, _ := json.Marshal(r)
			lines = append(lines, string(b))
		}
		err := os.WriteFile(filepath.Join(dir, domain.OutputIndex), []byte(strings.Join(lines, "\n")+"\n"), 0o600)
		return ports.SessionResult{Text: "ok"}, err
	})
	e.fill()
	e.idle()
	if envSeen[domain.EnvOutfilterDelegate] != domain.DelegateOff || envSeen[domain.EnvOutputKeep] != "1h0m0s" ||
		envSeen[domain.EnvOutputKeepBytes] != "1048576" || envSeen[domain.EnvRun] == "" {
		t.Fatalf("session env %v", envSeen)
	}
	m, err := e.svc.Metrics(e.ctx, "demo", owner(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if m.FilteredRuns != 2 || m.FilterSaved != 5400 || m.FilterSavedByRole["implementer"] != 5400 {
		t.Fatalf("metrics %d %d %v", m.FilteredRuns, m.FilterSaved, m.FilterSavedByRole)
	}
	tl, _ := e.svc.Timeline(e.ctx, "demo", owner(), it.ID)
	found := false
	for _, te := range tl {
		if te.Type == domain.EvAgentUsage && strings.Contains(te.Summary, "output filter saved 5400 tokens on 2 outputs") {
			found = true
		}
	}
	if !found {
		t.Fatalf("timeline %+v", tl)
	}
	d, err := e.svc.BuildDigest(e.ctx, "demo")
	if err != nil || !strings.Contains(d.Text(), "Output filter: 5400 tokens kept out of the agents' context, on 2 outputs") {
		t.Fatalf("digest %v:\n%s", err, d.Text())
	}
	if (Metrics{}).filterSavingsLine() != "" {
		t.Fatal("no savings, no line")
	}
	if n, saved := filterSavings("", "r"); n != 0 || saved != 0 {
		t.Fatal("no folder, nothing saved")
	}
	if n, _ := filterSavings(t.TempDir(), "r"); n != 0 {
		t.Fatal("no index, nothing saved")
	}
}

func TestTerseAnswersInMechanicalSteps(t *testing.T) {
	e := newEnv(t)
	it := e.item(e.ready("simple", "Rename a field", "docs").ID)
	it.Class = "simple"
	impl := domain.StepDef{Name: "implement", Role: "implementer"}
	sp, err := e.svc.BuildStepPrompt(e.ctx, e.p, it, impl, "implementer", "implementer-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sp.Prompt, "## Answer style") {
		t.Fatalf("a simple item is mechanical:\n%s", sp.Prompt)
	}
	critic := domain.StepDef{Name: "quick-look", Role: "critic", Model: "haiku"}
	sp, _ = e.svc.BuildStepPrompt(e.ctx, e.p, it, critic, "critic", "critic-1")
	if !strings.Contains(sp.Prompt, "## Answer style") {
		t.Fatal("the cheapest pinned model is mechanical")
	}
	hard := e.item(e.ready("feature", "Redesign the sync", "sync").ID)
	hard.Class = "complex"
	sp, _ = e.svc.BuildStepPrompt(e.ctx, e.p, hard, impl, "implementer", "implementer-1")
	if strings.Contains(sp.Prompt, "## Answer style") {
		t.Fatal("a complex item keeps full answers")
	}
	e.p.Cfg().Terse = domain.TerseDef{Mode: domain.TerseOff}
	sp, _ = e.svc.BuildStepPrompt(e.ctx, e.p, it, critic, "critic", "critic-1")
	if strings.Contains(sp.Prompt, "## Answer style") {
		t.Fatal("terse: off")
	}
}

func TestStepClass(t *testing.T) {
	e := newEnv(t)
	it := &domain.Item{Class: ""}
	if got := stepClass(e.p, it, domain.StepDef{Class: "cheap"}, "implementer"); got != "cheap" {
		t.Fatal(got)
	}
	it.Class = "simple"
	if got := stepClass(e.p, it, domain.StepDef{Class: "auto"}, "implementer"); got != "simple" {
		t.Fatal(got)
	}
	it.Class = "auto"
	if got := stepClass(e.p, it, domain.StepDef{}, "nobody"); got != "normal" {
		t.Fatal(got)
	}
}
