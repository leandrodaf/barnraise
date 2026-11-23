package domain

import (
	"errors"
	"fmt"
	"strings"
)

// StepType is what executes a step.
type StepType string

const (
	StepAgent      StepType = "agent"
	StepScript     StepType = "script"
	StepApproval   StepType = "approval"
	StepCheck      StepType = "check"
	StepMergeQueue StepType = "merge_queue"
)

// StepDef declares one step of a flow.
type StepDef struct {
	Name        string   `json:"name"`
	Type        StepType `json:"type"`
	Role        string   `json:"role,omitempty"`
	Model       string   `json:"model,omitempty"`        // fixed model, e.g. haiku for a cheap critic
	ModelFamily string   `json:"model_family,omitempty"` // "different_from(implement)"
	Class       string   `json:"class,omitempty"`        // "auto" or a class name
	Output      string   `json:"output,omitempty"`       // plan.md, failing_test
	Approver    string   `json:"approver,omitempty"`     // for approval steps: owner, analyst or a role
	Run         string   `json:"run,omitempty"`          // checks.main, merge_queue, or a script command
	Scope       string   `json:"scope,omitempty"`        // touched or all
	Require     string   `json:"require,omitempty"`      // failing_test_now_passes
	Review      bool     `json:"review,omitempty"`       // agent step that returns a review, not a delivery
}

// DifferentFrom returns the step name inside "different_from(x)".
func (s StepDef) DifferentFrom() string {
	f := strings.TrimSpace(s.ModelFamily)
	if strings.HasPrefix(f, "different_from(") && strings.HasSuffix(f, ")") {
		return strings.TrimSuffix(strings.TrimPrefix(f, "different_from("), ")")
	}
	return ""
}

// ReworkLimit bounds rework: same model, then escalate, then the owner.
type ReworkLimit struct {
	SameModel int    `json:"same_model"`
	Escalate  int    `json:"escalate"`
	Then      string `json:"then"` // owner
}

// DefaultReworkLimit is 3 same-model returns, 2 escalated, then the owner.
var DefaultReworkLimit = ReworkLimit{SameModel: 3, Escalate: 2, Then: "owner"}

// Flow is the compiled sequence of steps of an item kind.
type Flow struct {
	Name   string            `json:"name"`
	Steps  []StepDef         `json:"steps"`
	Rework map[string]string `json:"rework,omitempty"` // "check.fail" -> "implement"
	Limit  ReworkLimit       `json:"limit"`
}

// Validate checks names, references and rework targets.
func (f *Flow) Validate() error {
	if len(f.Steps) == 0 {
		return fmt.Errorf("flow %s has no steps", f.Name)
	}
	seen := map[string]bool{}
	for _, s := range f.Steps {
		if s.Name == "" {
			return fmt.Errorf("flow %s: step without a name", f.Name)
		}
		if seen[s.Name] {
			return fmt.Errorf("flow %s: duplicate step %s", f.Name, s.Name)
		}
		seen[s.Name] = true
		switch s.Type {
		case StepAgent:
			if s.Role == "" {
				return fmt.Errorf("flow %s: agent step %s needs a role", f.Name, s.Name)
			}
		case StepApproval:
			if s.Approver == "" {
				return fmt.Errorf("flow %s: approval step %s needs an approver", f.Name, s.Name)
			}
		case StepScript, StepCheck:
			if s.Run == "" {
				return fmt.Errorf("flow %s: step %s needs run", f.Name, s.Name)
			}
		case StepMergeQueue:
		default:
			return fmt.Errorf("flow %s: step %s has unknown type %q", f.Name, s.Name, s.Type)
		}
		if d := s.DifferentFrom(); d != "" && !seen[d] {
			return fmt.Errorf("flow %s: step %s compares with %s, which is not an earlier step", f.Name, s.Name, d)
		}
	}
	for k, v := range f.Rework {
		step, outcome, ok := strings.Cut(k, ".")
		if !ok || outcome == "" {
			return fmt.Errorf("flow %s: rework key %q must be <step>.<outcome>", f.Name, k)
		}
		if !seen[step] {
			return fmt.Errorf("flow %s: rework key %q names unknown step", f.Name, k)
		}
		if !seen[v] {
			return fmt.Errorf("flow %s: rework target %q is not a step", f.Name, v)
		}
		if f.Index(v) > f.Index(step) {
			return fmt.Errorf("flow %s: rework %q goes forward to %s", f.Name, k, v)
		}
	}
	if f.Limit.SameModel < 0 || f.Limit.Escalate < 0 {
		return fmt.Errorf("flow %s: rework limits cannot be negative", f.Name)
	}
	return nil
}

// Index returns the position of a step or -1.
func (f *Flow) Index(name string) int {
	for i, s := range f.Steps {
		if s.Name == name {
			return i
		}
	}
	return -1
}

// Step returns a step by name.
func (f *Flow) Step(name string) (StepDef, bool) {
	if i := f.Index(name); i >= 0 {
		return f.Steps[i], true
	}
	return StepDef{}, false
}

// First returns the first step name.
func (f *Flow) First() string { return f.Steps[0].Name }

// Outcome of a finished step.
type Outcome string

const (
	OutcomeDone         Outcome = "done"
	OutcomeConcerns     Outcome = "concerns"
	OutcomeNeedsContext Outcome = "needs_context"
	OutcomeBlocked      Outcome = "blocked"
	OutcomePass         Outcome = "pass"
	OutcomeFail         Outcome = "fail"
	OutcomeApprove      Outcome = "approve"
	OutcomeRework       Outcome = "rework"
	OutcomeReject       Outcome = "reject"
)

// Positive reports whether the outcome lets the item advance.
func (o Outcome) Positive() bool {
	return o == OutcomeDone || o == OutcomeConcerns || o == OutcomePass || o == OutcomeApprove
}

// TransitionKind is what the flow engine decides.
type TransitionKind string

const (
	TransAdvance  TransitionKind = "advance"  // go to NextStep
	TransRework   TransitionKind = "rework"   // back to NextStep with a reason
	TransOwner    TransitionKind = "owner"    // rework exhausted or the agent needs a person
	TransBlocked  TransitionKind = "blocked"  // agent says it cannot go on
	TransComplete TransitionKind = "complete" // past the last step
)

// Transition is the engine's decision after a step.
type Transition struct {
	Kind     TransitionKind
	NextStep string
	Tier     int // model tier for the next attempt
	Reason   string
	Rounds   int // rework rounds after this transition
}

// ErrUnknownStep is returned for steps missing from the flow.
var ErrUnknownStep = errors.New("unknown step")

// Decide computes the transition for item after step finished with outcome.
// The flow is code: the model decides only the content of the work.
func (f *Flow) Decide(it *Item, step string, outcome Outcome, reason string) (Transition, error) {
	idx := f.Index(step)
	if idx < 0 {
		return Transition{}, fmt.Errorf("%w %q in flow %s", ErrUnknownStep, step, f.Name)
	}
	rounds := it.ReworkCount()
	switch {
	case outcome.Positive():
		if idx == len(f.Steps)-1 {
			return Transition{Kind: TransComplete, Tier: it.Tier, Rounds: rounds}, nil
		}
		return Transition{Kind: TransAdvance, NextStep: f.Steps[idx+1].Name, Tier: it.Tier, Rounds: rounds}, nil
	case outcome == OutcomeNeedsContext:
		return Transition{Kind: TransOwner, NextStep: step, Tier: it.Tier, Reason: reason, Rounds: rounds}, nil
	case outcome == OutcomeBlocked:
		return Transition{Kind: TransBlocked, NextStep: step, Tier: it.Tier, Reason: reason, Rounds: rounds}, nil
	}
	target := f.ReworkTarget(step, outcome)
	if target == "" {
		// Nothing earlier to return to: a person decides.
		return Transition{Kind: TransOwner, NextStep: step, Tier: it.Tier, Reason: reason, Rounds: rounds}, nil
	}
	rounds++
	lim := f.Limit
	switch {
	case rounds <= lim.SameModel:
		return Transition{Kind: TransRework, NextStep: target, Tier: it.Tier, Reason: reason, Rounds: rounds}, nil
	case rounds <= lim.SameModel+lim.Escalate:
		tier := it.Tier
		if rounds == lim.SameModel+1 {
			tier++ // one model level up for the escalated rounds
		}
		return Transition{Kind: TransRework, NextStep: target, Tier: tier, Reason: reason, Rounds: rounds}, nil
	default:
		return Transition{Kind: TransOwner, NextStep: target, Tier: it.Tier, Reason: fmt.Sprintf("rework limit reached after %d rounds: %s", rounds-1, reason), Rounds: rounds - 1}, nil
	}
}

// ReworkTarget returns where a failed step sends the item: the explicit
// rework rule, or the nearest earlier agent step that is not a review.
func (f *Flow) ReworkTarget(step string, outcome Outcome) string {
	if t, ok := f.Rework[step+"."+string(outcome)]; ok {
		return t
	}
	// "reject" and "rework" share a rule when only one is declared.
	if outcome == OutcomeReject {
		if t, ok := f.Rework[step+".rework"]; ok {
			return t
		}
	}
	if outcome == OutcomeRework {
		if t, ok := f.Rework[step+".reject"]; ok {
			return t
		}
	}
	idx := f.Index(step)
	for i := idx - 1; i >= 0; i-- {
		s := f.Steps[i]
		if s.Type == StepAgent && !s.Review {
			return s.Name
		}
	}
	return ""
}

// RedoFrom validates a "redo from this step" request.
func (f *Flow) RedoFrom(step string) error {
	if f.Index(step) < 0 {
		return fmt.Errorf("%w %q in flow %s", ErrUnknownStep, step, f.Name)
	}
	return nil
}
