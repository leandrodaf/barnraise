package domain

import (
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"
)

// Status of an item. The daemon owns these transitions.
type Status string

const (
	StatusDraft        Status = "draft"         // missing criteria or has open clarifications
	StatusReady        Status = "ready"         // can be claimed
	StatusInProgress   Status = "in_progress"   // claimed, a step is running or about to
	StatusWaitingOwner Status = "waiting_owner" // a decision only a person can take
	StatusBlocked      Status = "blocked"       // depends on another item
	StatusQueued       Status = "queued"        // approved, in the merge queue
	StatusIntegrated   Status = "integrated"    // merged into main
	StatusClosed       Status = "closed"        // obsolete, duplicate, done by another item, abandoned
	StatusPaused       Status = "paused"        // taken over by a person or emergency stop
)

// Terminal reports whether no more work happens on the item.
func (s Status) Terminal() bool { return s == StatusIntegrated || s == StatusClosed }

// Priority P0 (highest) to P3.
type Priority int

const (
	P0 Priority = 0
	P1 Priority = 1
	P2 Priority = 2
	P3 Priority = 3
)

func (p Priority) String() string { return fmt.Sprintf("P%d", int(p)) }

// ParsePriority accepts "P1" or "1".
func ParsePriority(s string) (Priority, error) {
	s = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(s)), "P")
	switch s {
	case "0":
		return P0, nil
	case "1":
		return P1, nil
	case "2", "":
		return P2, nil
	case "3":
		return P3, nil
	}
	return P2, fmt.Errorf("invalid priority %q", s)
}

// Lane is the capacity band an item belongs to.
type Lane string

const (
	LaneGoal        Lane = "goal"
	LaneBugs        Lane = "bugs"
	LaneImprovement Lane = "improvements"
	LaneSelf        Lane = "self_improvement"
)

// Criterion is one EARS acceptance condition tied to a check.
type Criterion struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Check    string `json:"check"` // test, Maestro flow or named check that verifies it
	Met      bool   `json:"met"`
	Evidence string `json:"evidence,omitempty"`
}

// Anchors are what an item assumes to be true.
type Anchors struct {
	Files     []string `json:"files,omitempty"`
	Symbols   []string `json:"symbols,omitempty"`
	Areas     []string `json:"areas,omitempty"`
	Screens   []string `json:"screens,omitempty"`
	Contracts []string `json:"contracts,omitempty"`
	Decisions []string `json:"decisions,omitempty"`
	Specs     []string `json:"specs,omitempty"`
	Base      string   `json:"base,omitempty"` // main commit the item was last validated against
}

// Empty reports whether no anchors are set besides the base.
func (a Anchors) Empty() bool {
	return len(a.Files)+len(a.Symbols)+len(a.Areas)+len(a.Screens)+len(a.Contracts)+len(a.Decisions)+len(a.Specs) == 0
}

// ReworkRecord tracks one return to an earlier step.
type ReworkRecord struct {
	From    string    `json:"from"`    // step that sent it back
	To      string    `json:"to"`      // step it went back to
	By      string    `json:"by"`      // agent or person
	Reason  string    `json:"reason"`  // why, in the reader's words
	Outcome string    `json:"outcome"` // fail, reject, rework
	Tier    int       `json:"tier"`    // model escalation tier used for the next attempt
	At      time.Time `json:"at,omitzero"`
	// Commit is the item's head when the work was sent back: the fix is
	// what changed from there.
	Commit string `json:"commit,omitempty"`
	// Resolved is set when the step that sent the work back passed again.
	Resolved bool `json:"resolved,omitempty"`
}

// Claim records who holds the item.
type Claim struct {
	Machine string    `json:"machine"`
	Agent   string    `json:"agent,omitempty"`
	Since   time.Time `json:"since,omitzero"`
}

// Item is a deliverable unit of work of 2 to 6 agent hours.
type Item struct {
	ID             string      `json:"id"`
	Version        int         `json:"version"`
	Title          string      `json:"title"`
	Body           string      `json:"body,omitempty"`
	Kind           string      `json:"kind"` // flow name: feature, bug, blog-post…
	Priority       Priority    `json:"priority"`
	Lane           Lane        `json:"lane"`
	Area           string      `json:"area,omitempty"`
	GoalID         string      `json:"goal_id,omitempty"`
	Size           string      `json:"size,omitempty"`  // "2h-6h", estimate
	Class          string      `json:"class,omitempty"` // simple, normal, complex, native
	Status         Status      `json:"status"`
	Step           string      `json:"step,omitempty"`
	Criteria       []Criterion `json:"criteria,omitempty"`
	Preserve       []string    `json:"preserve,omitempty"` // bugs: what must not change
	Clarifications []string    `json:"clarifications,omitempty"`
	Anchors        Anchors     `json:"anchors"`
	// Unlocked are existing files the policy locks (tests, references, check
	// scripts) that the owner, or the autopilot for the owner, let this item
	// change.
	Unlocked   []string       `json:"unlocked,omitempty"`
	Deps       []string       `json:"deps,omitempty"`
	Tweaks     []string       `json:"tweaks,omitempty"`
	Checklist  []string       `json:"checklist,omitempty"`
	PastRework []ReworkRecord `json:"past_rework,omitempty"` // out of the rework budget (owner corrections, reset rounds); lessons still to learn
	Rework     []ReworkRecord `json:"rework,omitempty"`
	Tier       int            `json:"tier"` // 0 = class default; +1 per escalation
	Claim      *Claim         `json:"claim,omitempty"`
	Branch     string         `json:"branch,omitempty"`
	PR         string         `json:"pr,omitempty"`
	TrackerRef string         `json:"tracker_ref,omitempty"`
	// TrackerURL is the entry's web page, when the tracker said it.
	TrackerURL string `json:"tracker_url,omitempty"`
	// Adopted marks an item that came from an entry people wrote in the
	// tracker. Barnraise keeps its state and labels current there, and never
	// rewrites its title or text: they are the author's.
	Adopted         bool               `json:"adopted,omitempty"`
	Risky           bool               `json:"risky,omitempty"`
	Parked          bool               `json:"parked,omitempty"` // waits for a focus to end
	ClosedReason    string             `json:"closed_reason,omitempty"`
	ClosedBy        string             `json:"closed_by,omitempty"`
	WaitingQuestion string             `json:"waiting_question,omitempty"`
	ValidatedAt     time.Time          `json:"validated_at,omitzero"`
	CreatedAt       time.Time          `json:"created_at,omitzero"`
	UpdatedAt       time.Time          `json:"updated_at,omitzero"`
	StepStartedAt   time.Time          `json:"step_started_at,omitzero"`
	IntegratedAt    time.Time          `json:"integrated_at,omitzero"`
	MergeCommit     string             `json:"merge_commit,omitempty"`
	FilesTouched    []string           `json:"files_touched,omitempty"`
	Runs            map[string]StepRun `json:"runs,omitempty"`     // last run per step
	Attempts        int                `json:"attempts,omitempty"` // runs of the current step without a delivery
	Alt             *AltAttempt        `json:"alt,omitempty"`
	// Change is how the item's work is described on main: the header of its
	// commit and its line in the changelog. Set by the delivery.
	Change *ChangeNote `json:"change,omitempty"`
}

// AltAttempt is the second, parallel attempt of a risky item's step: the
// checks and the reviewers choose; never by default, because it doubles usage.
type AltAttempt struct {
	Step   string    `json:"step"`
	Branch string    `json:"branch"`
	A      *Delivery `json:"a,omitempty"` // first attempt's delivery, held until B ends
	B      *Delivery `json:"b,omitempty"`
	BEnded bool      `json:"b_ended"`
	Winner string    `json:"winner,omitempty"`
}

// StepRun records who ran a step last, used for model_family rules.
type StepRun struct {
	Agent    string `json:"agent"`
	Account  string `json:"account,omitempty"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Family   string `json:"family"`
}

// Errors returned by item rules.
var (
	ErrNotClaimable     = errors.New("item is not ready to be claimed")
	ErrAlreadyClaimed   = errors.New("item already has an owner")
	ErrOpenClarifying   = errors.New("item has open [NEEDS CLARIFICATION] markers")
	ErrNoCriteria       = errors.New("item has no criteria")
	ErrCriterionNoCheck = errors.New("criterion has no check that verifies it")
	ErrTerminal         = errors.New("item is already integrated or closed")
)

// ClarificationMarker marks an open question in item text.
const ClarificationMarker = "[NEEDS CLARIFICATION"

// ResolveClarifications marks every open [NEEDS CLARIFICATION: …] in text
// answered: it becomes "(clarified: …)", so the question stays readable but
// no longer holds the item in draft. An unclosed marker runs to the end of
// the text, as FindClarifications reads it.
func ResolveClarifications(text string) string {
	var b strings.Builder
	for {
		i := strings.Index(text, ClarificationMarker)
		if i < 0 {
			b.WriteString(text)
			return b.String()
		}
		b.WriteString(text[:i])
		rest := text[i+len(ClarificationMarker):]
		end := strings.IndexByte(rest, ']')
		q := rest
		if end >= 0 {
			q, text = rest[:end], rest[end+1:]
		} else {
			text = ""
		}
		q = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(q), ":"))
		if q == "" {
			b.WriteString("(clarified)")
		} else {
			b.WriteString("(clarified: " + q + ")")
		}
	}
}

// FindClarifications returns the open [NEEDS CLARIFICATION: …] markers in text.
func FindClarifications(text string) []string {
	var out []string
	for {
		i := strings.Index(text, ClarificationMarker)
		if i < 0 {
			return out
		}
		j := strings.IndexByte(text[i:], ']')
		if j < 0 {
			out = append(out, strings.TrimSpace(text[i:]))
			return out
		}
		out = append(out, text[i:i+j+1])
		text = text[i+j+1:]
	}
}

// Readiness explains why an item cannot start, or nil if it can.
func (it *Item) Readiness(requireCriteria bool) error {
	if it.Status.Terminal() {
		return ErrTerminal
	}
	if len(it.Clarifications) > 0 {
		return ErrOpenClarifying
	}
	for _, c := range it.Criteria {
		if len(FindClarifications(c.Text)) > 0 {
			return ErrOpenClarifying
		}
	}
	if len(FindClarifications(it.Body)) > 0 {
		return ErrOpenClarifying
	}
	if requireCriteria && len(it.Criteria) == 0 {
		return ErrNoCriteria
	}
	for _, c := range it.Criteria {
		if strings.TrimSpace(c.Check) == "" {
			return fmt.Errorf("%w: %s", ErrCriterionNoCheck, c.ID)
		}
	}
	return nil
}

// RefreshReadiness moves an item between draft and ready.
func (it *Item) RefreshReadiness(requireCriteria bool) {
	if it.Status != StatusDraft && it.Status != StatusReady {
		return
	}
	if it.Readiness(requireCriteria) == nil {
		it.Status = StatusReady
	} else {
		it.Status = StatusDraft
	}
}

// ReworkCount is the number of returns so far.
func (it *Item) ReworkCount() int { return len(it.Rework) }

// AllCriteriaMet reports whether every criterion is marked with evidence.
func (it *Item) AllCriteriaMet() bool {
	for _, c := range it.Criteria {
		if !c.Met {
			return false
		}
	}
	return true
}

// CriteriaChecklist renders criteria as a markdown checklist for the PR body.
func (it *Item) CriteriaChecklist() string {
	var b strings.Builder
	for _, c := range it.Criteria {
		mark := " "
		if c.Met {
			mark = "x"
		}
		fmt.Fprintf(&b, "- [%s] %s: %s (check: %s)\n", mark, c.ID, c.Text, c.Check)
	}
	return b.String()
}

// Clone returns a deep copy.
func (it *Item) Clone() *Item {
	c := *it
	c.Criteria = append([]Criterion(nil), it.Criteria...)
	c.Preserve = append([]string(nil), it.Preserve...)
	c.Clarifications = append([]string(nil), it.Clarifications...)
	c.Deps = append([]string(nil), it.Deps...)
	c.Tweaks = append([]string(nil), it.Tweaks...)
	c.Checklist = append([]string(nil), it.Checklist...)
	c.Rework = append([]ReworkRecord(nil), it.Rework...)
	c.PastRework = append([]ReworkRecord(nil), it.PastRework...)
	c.FilesTouched = append([]string(nil), it.FilesTouched...)
	c.Anchors.Files = append([]string(nil), it.Anchors.Files...)
	c.Anchors.Symbols = append([]string(nil), it.Anchors.Symbols...)
	c.Anchors.Areas = append([]string(nil), it.Anchors.Areas...)
	c.Anchors.Screens = append([]string(nil), it.Anchors.Screens...)
	c.Anchors.Contracts = append([]string(nil), it.Anchors.Contracts...)
	c.Anchors.Decisions = append([]string(nil), it.Anchors.Decisions...)
	c.Anchors.Specs = append([]string(nil), it.Anchors.Specs...)
	if it.Runs != nil {
		c.Runs = make(map[string]StepRun, len(it.Runs))
		maps.Copy(c.Runs, it.Runs)
	}
	if it.Alt != nil {
		alt := *it.Alt
		c.Alt = &alt
	}
	if it.Claim != nil {
		cl := *it.Claim
		c.Claim = &cl
	}
	return &c
}

// MarkCriterion marks a criterion as met with evidence. The text can never be
// changed by the implementer; only analysts rewrite criteria.
func (it *Item) MarkCriterion(id string, met bool, evidence string) error {
	for i := range it.Criteria {
		if it.Criteria[i].ID == id {
			if met && strings.TrimSpace(evidence) == "" {
				return fmt.Errorf("criterion %s: evidence is required to mark it met", id)
			}
			it.Criteria[i].Met = met
			it.Criteria[i].Evidence = evidence
			return nil
		}
	}
	return fmt.Errorf("criterion %s does not exist on %s", id, it.ID)
}

// DepsSatisfied reports whether all dependencies are integrated or closed.
func (it *Item) DepsSatisfied(lookup func(id string) *Item) bool {
	for _, d := range it.Deps {
		dep := lookup(d)
		if dep == nil {
			continue // unknown dependency does not block forever
		}
		if dep.Status != StatusIntegrated && !(dep.Status == StatusClosed && dep.ClosedReason != "abandoned") {
			return false
		}
	}
	return true
}

// SortItems orders by priority, then by creation, then id.
func SortItems(items []*Item) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority < items[j].Priority
		}
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		}
		return idNum(items[i].ID) < idNum(items[j].ID)
	})
}

func idNum(id string) int {
	_, n, err := ParseID(id)
	if err != nil {
		return 0
	}
	return n
}

// LaneFor decides the lane of a new item. P0/P1 bugs go straight to the bug
// lane; P2/P3 bugs become proposals (the caller handles that).
func LaneFor(kind string, prio Priority, inGoal bool, selfImprovement bool) Lane {
	switch {
	case selfImprovement:
		return LaneSelf
	case kind == "bug":
		return LaneBugs
	case inGoal:
		return LaneGoal
	default:
		return LaneImprovement
	}
}

// BugNeedsProposal reports whether a bug should wait as a proposal.
func BugNeedsProposal(prio Priority) bool { return prio >= P2 }

// ValidateEARS checks a criterion follows the EARS shape ("WHEN x, THE SYSTEM
// SHALL y" and variants, or the pt-BR "QUANDO x, O SISTEMA DEVE y").
func ValidateEARS(text string) error {
	t := strings.ToUpper(strings.TrimSpace(text))
	if t == "" {
		return errors.New("criterion text is empty")
	}
	shall := strings.Contains(t, " SHALL ") || strings.Contains(t, " DEVE ")
	subject := strings.Contains(t, "THE SYSTEM") || strings.Contains(t, "O SISTEMA") || strings.Contains(t, "A TELA") || strings.Contains(t, "THE ")
	if !shall || !subject {
		return fmt.Errorf("criterion %q is not in EARS form: use \"WHEN <trigger>, THE SYSTEM SHALL <response>\"", text)
	}
	return nil
}
