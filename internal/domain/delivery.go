package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// DeliveryStatus is the fixed status an agent returns at the end of a step.
type DeliveryStatus string

const (
	DeliveryDone         DeliveryStatus = "DONE"
	DeliveryConcerns     DeliveryStatus = "DONE_WITH_CONCERNS"
	DeliveryNeedsContext DeliveryStatus = "NEEDS_CONTEXT"
	DeliveryBlocked      DeliveryStatus = "BLOCKED"
)

// Outcome maps the delivery status to a flow outcome.
func (s DeliveryStatus) Outcome() (Outcome, error) {
	switch s {
	case DeliveryDone:
		return OutcomeDone, nil
	case DeliveryConcerns:
		return OutcomeConcerns, nil
	case DeliveryNeedsContext:
		return OutcomeNeedsContext, nil
	case DeliveryBlocked:
		return OutcomeBlocked, nil
	}
	return "", fmt.Errorf("delivery status %q must be DONE, DONE_WITH_CONCERNS, NEEDS_CONTEXT or BLOCKED", s)
}

// CriterionMark is the implementer marking a criterion, never rewriting it.
type CriterionMark struct {
	ID       string `json:"id"`
	Met      bool   `json:"met"`
	Evidence string `json:"evidence"`
}

// Delivery is what the agent returns when a step ends.
type Delivery struct {
	ItemID   string          `json:"item_id"`
	Step     string          `json:"step"`
	Agent    string          `json:"agent"`
	Status   DeliveryStatus  `json:"status"`
	Summary  string          `json:"summary"`
	Criteria []CriterionMark `json:"criteria,omitempty"`
	Files    []string        `json:"files,omitempty"`
	Concerns []string        `json:"concerns,omitempty"`
	Reason   string          `json:"reason,omitempty"` // for NEEDS_CONTEXT and BLOCKED
	Commit   string          `json:"commit,omitempty"`
	At       time.Time       `json:"at"`
}

// Validate enforces the fields: loose text does not move an item.
func (d *Delivery) Validate(it *Item) error {
	if _, err := d.Status.Outcome(); err != nil {
		return err
	}
	if strings.TrimSpace(d.Summary) == "" {
		return errors.New("delivery needs a summary")
	}
	switch d.Status {
	case DeliveryNeedsContext, DeliveryBlocked:
		if strings.TrimSpace(d.Reason) == "" {
			return fmt.Errorf("delivery %s needs a reason", d.Status)
		}
	case DeliveryConcerns:
		if len(d.Concerns) == 0 {
			return errors.New("delivery DONE_WITH_CONCERNS needs at least one concern")
		}
	}
	known := map[string]bool{}
	for _, c := range it.Criteria {
		known[c.ID] = true
	}
	for _, m := range d.Criteria {
		if !known[m.ID] {
			return fmt.Errorf("criterion %s does not exist on %s", m.ID, it.ID)
		}
		if m.Met && strings.TrimSpace(m.Evidence) == "" {
			return fmt.Errorf("criterion %s marked met without evidence", m.ID)
		}
	}
	return nil
}

// Verdict is one of the two separate review verdicts.
type Verdict string

const (
	VerdictPass Verdict = "pass"
	VerdictFail Verdict = "fail"
)

// ReviewDecision is the fixed review status.
type ReviewDecision string

const (
	ReviewApprove ReviewDecision = "APPROVE"
	ReviewRework  ReviewDecision = "REWORK"
	ReviewReject  ReviewDecision = "REJECT"
)

// Finding is a review note.
type Finding struct {
	Area     string `json:"area,omitempty"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Severity string `json:"severity,omitempty"` // blocker, major, minor
	Text     string `json:"text"`
}

// Review carries two separate verdicts: does it meet the criteria, and is the code good.
type Review struct {
	ItemID   string         `json:"item_id"`
	Step     string         `json:"step"`
	Reviewer string         `json:"reviewer"`
	Criteria Verdict        `json:"criteria_verdict"`
	Code     Verdict        `json:"code_verdict"`
	Decision ReviewDecision `json:"decision"`
	Findings []Finding      `json:"findings,omitempty"`
	Summary  string         `json:"summary"`
	Family   string         `json:"family,omitempty"`
	At       time.Time      `json:"at"`
}

// Validate refuses reviews without both verdicts or inconsistent decisions.
func (r *Review) Validate() error {
	if r.Criteria != VerdictPass && r.Criteria != VerdictFail {
		return errors.New("review needs criteria_verdict: pass or fail")
	}
	if r.Code != VerdictPass && r.Code != VerdictFail {
		return errors.New("review needs code_verdict: pass or fail")
	}
	switch r.Decision {
	case ReviewApprove:
		if r.Criteria != VerdictPass || r.Code != VerdictPass {
			return errors.New("APPROVE requires both verdicts to pass")
		}
	case ReviewRework, ReviewReject:
		if len(r.Findings) == 0 {
			return fmt.Errorf("%s needs at least one finding", r.Decision)
		}
	default:
		return errors.New("review decision must be APPROVE, REWORK or REJECT")
	}
	if strings.TrimSpace(r.Summary) == "" {
		return errors.New("review needs a summary")
	}
	return nil
}

// Outcome maps the decision to a flow outcome.
func (r *Review) Outcome() Outcome {
	switch r.Decision {
	case ReviewApprove:
		return OutcomeApprove
	case ReviewRework:
		return OutcomeRework
	}
	return OutcomeReject
}

// SplitFindings separates findings of the item's area (resolved as rework in
// the same round) from findings of other areas (which become proposals).
func SplitFindings(it *Item, fs []Finding, areaOfFile func(string) string) (same, other []Finding) {
	for _, f := range fs {
		area := f.Area
		if area == "" && f.File != "" && areaOfFile != nil {
			area = areaOfFile(f.File)
		}
		if area == "" || it.Area == "" || AreaName(area) == AreaName(it.Area) {
			same = append(same, f)
		} else {
			f.Area = area
			other = append(other, f)
		}
	}
	return same, other
}

// FormatFindings renders findings for a rework prompt.
func FormatFindings(fs []Finding) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString("- ")
		if f.Severity != "" {
			b.WriteString("[" + f.Severity + "] ")
		}
		if f.File != "" {
			b.WriteString(f.File)
			if f.Line > 0 {
				fmt.Fprintf(&b, ":%d", f.Line)
			}
			b.WriteString(": ")
		}
		b.WriteString(f.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

// CheckResult is the structured result of a verification.
type CheckResult struct {
	ID        string        `json:"id"`
	ItemID    string        `json:"item_id"`
	Check     string        `json:"check"`
	Commit    string        `json:"commit,omitempty"`
	Passed    bool          `json:"passed"`
	Flaky     bool          `json:"flaky,omitempty"`
	Summary   string        `json:"summary"`
	LogPath   string        `json:"log_path,omitempty"`
	LogHash   string        `json:"log_hash,omitempty"`
	Artifacts []string      `json:"artifacts,omitempty"`
	Duration  time.Duration `json:"duration"`
	Criteria  []string      `json:"criteria,omitempty"` // criteria ids verified
	RunsOn    string        `json:"runs_on"`
	At        time.Time     `json:"at"`
}

// CriteriaCoverage returns criteria whose check is not among the available checks.
func CriteriaCoverage(it *Item, available func(check string) bool) []string {
	var missing []string
	for _, c := range it.Criteria {
		if strings.TrimSpace(c.Check) == "" || !available(c.Check) {
			missing = append(missing, c.ID)
		}
	}
	return missing
}

// FlakyVerdict implements "fails only if it fails on the branch and passes on main".
// branch and main hold the outcomes of up to 3 runs each.
func FlakyVerdict(branch, main []bool) (passed bool, flaky bool) {
	bPass, bFail := count(branch)
	mPass, mFail := count(main)
	switch {
	case bFail == 0:
		return true, false
	case bPass > 0:
		// Oscillates on the branch: quarantine, do not block.
		return true, true
	case len(main) > 0 && mFail > 0 && mPass == 0:
		// Also red on main: not this item's fault.
		return true, true
	case len(main) > 0 && mFail > 0:
		return true, true
	default:
		return false, false
	}
}

func count(rs []bool) (pass, fail int) {
	for _, r := range rs {
		if r {
			pass++
		} else {
			fail++
		}
	}
	return
}
