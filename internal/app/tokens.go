package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// filterSavings reads what `barnraise filter` recorded for one session run
// in its outputs folder: how many outputs it shortened and the tokens it
// kept out of the agent's context. Nothing recorded is zero.
func filterSavings(dir, run string) (int, int64) {
	if dir == "" || run == "" {
		return 0, 0
	}
	data, err := os.ReadFile(filepath.Join(dir, domain.OutputIndex))
	if err != nil {
		return 0, 0
	}
	return domain.FilterSavings(domain.ParseOutputIndex(data), run)
}

// stepClass is the model class a step runs with: the step's own, else the
// item's, else the role's, normal when none says.
func stepClass(p *Project, it *domain.Item, st domain.StepDef, role string) string {
	class := st.Class
	if class == "" || class == "auto" {
		class = def(it.Class, def(p.Cfg().Roles[role].Class, "normal"))
	}
	if class == "auto" {
		class = "normal"
	}
	return class
}

// terseAnswers is the instruction that cuts the agent's own words in
// mechanical steps (terse.mode). The idea comes from caveman (ADR-0036).
const terseAnswers = `
## Answer style
This step is mechanical: keep your own words to a minimum. No preamble, no restating the item or these
instructions, no recap of what you did, no closing remarks; short fragments are fine. Code, commands,
paths, error messages, criteria ids and the fields of your deliver or review stay exact and complete.
`

// addFilterSavings adds a step's output-filter savings to the metrics.
func (m *Metrics) addFilterSavings(u *domain.TokenUsage) {
	if u.FilteredRuns == 0 && u.FilterSaved == 0 {
		return
	}
	m.FilteredRuns += u.FilteredRuns
	m.FilterSaved += u.FilterSaved
	if m.FilterSavedByRole == nil {
		m.FilterSavedByRole = map[string]int64{}
	}
	m.FilterSavedByRole[u.Role] += u.FilterSaved
}

// filterSavingsLine is the digest's line about the output filter, or "".
func (m Metrics) filterSavingsLine() string {
	if m.FilteredRuns == 0 {
		return ""
	}
	return fmt.Sprintf("%d tokens kept out of the agents' context, on %d outputs (barnraise output show <id> reads one in full)", m.FilterSaved, m.FilteredRuns)
}

// terseFor returns the answer-style section of a step's prompt, or "".
func terseFor(p *Project, it *domain.Item, st domain.StepDef, role string) string {
	cfg := p.Cfg()
	if !cfg.Terse.Applies(st, stepClass(p, it, st, role), cfg.Models.Ladders) {
		return ""
	}
	return terseAnswers
}
