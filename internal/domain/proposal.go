package domain

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// ProposalStatus tracks a suggested item waiting for approval.
type ProposalStatus string

const (
	ProposalOpen     ProposalStatus = "open"
	ProposalApproved ProposalStatus = "approved"
	ProposalRejected ProposalStatus = "rejected"
	ProposalLater    ProposalStatus = "later"
	ProposalExpired  ProposalStatus = "expired"
	ProposalMerged   ProposalStatus = "merged" // became a comment on an equal proposal or item
)

// Proposal is an item suggested by an agent or a person, in a fixed shape.
type Proposal struct {
	ID         string         `json:"id"`
	Version    int            `json:"version"`
	Title      string         `json:"title"`
	Problem    string         `json:"problem"`
	Evidence   string         `json:"evidence"` // print, log, line
	Impact     string         `json:"impact"`
	Size       string         `json:"size"`
	Area       string         `json:"area"`
	GoalLink   string         `json:"goal_link"` // relation to the goal
	Kind       string         `json:"kind"`      // feature, improvement, bug, self_improvement
	Priority   Priority       `json:"priority"`
	By         string         `json:"by"`
	Status     ProposalStatus `json:"status"`
	Comments   []string       `json:"comments,omitempty"`
	Reason     string         `json:"reason,omitempty"`
	ItemID     string         `json:"item_id,omitempty"` // item created on approval
	CreatedAt  time.Time      `json:"created_at,omitzero"`
	DecidedAt  time.Time      `json:"decided_at,omitzero"`
	DecidedBy  string         `json:"decided_by,omitempty"`
	LaterUntil time.Time      `json:"later_until,omitzero"`
}

// ProposalRules come from `proposals:` in barnraise.yaml.
type ProposalRules struct {
	PerAgentPerDay int           `json:"per_agent_per_day"`
	Expire         time.Duration `json:"expire"`
	Duplicate      string        `json:"duplicate"` // comment
}

// DefaultProposalRules: 3 per agent per day, expire in 14 days, duplicates become comments.
var DefaultProposalRules = ProposalRules{PerAgentPerDay: 3, Expire: 14 * 24 * time.Hour, Duplicate: "comment"}

// Proposal validation errors.
var (
	ErrProposalLimit   = errors.New("daily proposal limit reached for this agent")
	ErrProposalInvalid = errors.New("proposal is missing required fields")
)

// Validate enforces the mandatory fields.
func (p *Proposal) Validate() error {
	var missing []string
	if strings.TrimSpace(p.Title) == "" {
		missing = append(missing, "title")
	}
	if strings.TrimSpace(p.Problem) == "" {
		missing = append(missing, "problem")
	}
	if strings.TrimSpace(p.Evidence) == "" {
		missing = append(missing, "evidence")
	}
	if strings.TrimSpace(p.Impact) == "" {
		missing = append(missing, "impact")
	}
	if strings.TrimSpace(p.Size) == "" {
		missing = append(missing, "size")
	}
	if strings.TrimSpace(p.Area) == "" {
		missing = append(missing, "area")
	}
	if strings.TrimSpace(p.GoalLink) == "" {
		missing = append(missing, "goal_link")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s", ErrProposalInvalid, strings.Join(missing, ", "))
	}
	switch p.Kind {
	case "", "feature", "improvement", "bug", "self_improvement":
	default:
		return fmt.Errorf("%w: kind must be feature, improvement, bug or self_improvement", ErrProposalInvalid)
	}
	return nil
}

// CheckDailyLimit counts proposals by the same author in the last 24 hours.
func CheckDailyLimit(existing []*Proposal, by string, now time.Time, limit int) error {
	if limit <= 0 || by == "" || by == "owner" {
		return nil
	}
	n := 0
	for _, p := range existing {
		if p.By == by && now.Sub(p.CreatedAt) < 24*time.Hour {
			n++
		}
	}
	if n >= limit {
		return fmt.Errorf("%w (%d in the last 24 hours)", ErrProposalLimit, n)
	}
	return nil
}

// Tokens splits text into lowercase words for similarity.
func Tokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(w) > 2 && !stopword[w] {
			out[w] = true
		}
	}
	return out
}

var stopword = map[string]bool{"the": true, "and": true, "for": true, "with": true, "from": true, "that": true, "this": true,
	"uma": true, "que": true, "para": true, "com": true, "dos": true, "das": true, "não": true, "nao": true, "por": true}

// Similarity is the Jaccard index of two texts' words.
func Similarity(a, b string) float64 {
	ta, tb := Tokens(a), Tokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	inter := 0
	for w := range ta {
		if tb[w] {
			inter++
		}
	}
	return float64(inter) / float64(len(ta)+len(tb)-inter)
}

// DuplicateThreshold is the similarity above which two texts in the same area are the same thing.
const DuplicateThreshold = 0.5

// Match is a duplicate found for a proposal.
type Match struct {
	ID    string
	Score float64
}

// FindDuplicate looks for an open proposal or a live item in the same area
// that says the same thing.
func FindDuplicate(p *Proposal, proposals []*Proposal, items []*Item) (Match, bool) {
	text := p.Title + " " + p.Problem
	best := Match{}
	for _, o := range proposals {
		if o.ID == p.ID || AreaName(o.Area) != AreaName(p.Area) {
			continue
		}
		if o.Status != ProposalOpen && o.Status != ProposalLater && o.Status != ProposalApproved {
			continue
		}
		if s := Similarity(text, o.Title+" "+o.Problem); s > best.Score {
			best = Match{ID: o.ID, Score: s}
		}
	}
	for _, it := range items {
		if it.Status.Terminal() || AreaName(it.Area) != AreaName(p.Area) {
			continue
		}
		if s := Similarity(text, it.Title+" "+it.Body); s > best.Score {
			best = Match{ID: it.ID, Score: s}
		}
	}
	return best, best.Score >= DuplicateThreshold
}

// ExpireProposals closes open proposals older than the limit.
func ExpireProposals(ps []*Proposal, now time.Time, after time.Duration) []*Proposal {
	var out []*Proposal
	for _, p := range ps {
		if p.Status == ProposalOpen && after > 0 && now.Sub(p.CreatedAt) >= after {
			out = append(out, p)
		}
		if p.Status == ProposalLater && !p.LaterUntil.IsZero() && now.After(p.LaterUntil) && now.Sub(p.CreatedAt) >= after {
			out = append(out, p)
		}
	}
	return out
}

// ProposalGroup bundles proposals of one area: four for the same screen show as one.
type ProposalGroup struct {
	Area      string      `json:"area"`
	Proposals []*Proposal `json:"proposals"`
}

// GroupProposals groups open proposals by area, larger groups first.
func GroupProposals(ps []*Proposal) []ProposalGroup {
	m := map[string][]*Proposal{}
	for _, p := range ps {
		if p.Status != ProposalOpen && p.Status != ProposalLater {
			continue
		}
		a := AreaName(p.Area)
		m[a] = append(m[a], p)
	}
	out := make([]ProposalGroup, 0, len(m))
	for a, list := range m {
		out = append(out, ProposalGroup{Area: a, Proposals: list})
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Proposals) != len(out[j].Proposals) {
			return len(out[i].Proposals) > len(out[j].Proposals)
		}
		return out[i].Area < out[j].Area
	})
	return out
}

// Tweak is a small fix that rides with the next item of the same area.
type Tweak struct {
	ID        string    `json:"id"`
	Area      string    `json:"area"`
	Text      string    `json:"text"`
	By        string    `json:"by"`
	CreatedAt time.Time `json:"created_at,omitzero"`
	ItemID    string    `json:"item_id,omitempty"` // item it was attached to
}

// TweakRules come from `items.tweaks`.
type TweakRules struct {
	BecomeItemAt  int  `json:"become_item_at"`
	AttachOnClaim bool `json:"attach_on_claim"`
}

// DefaultTweakRules turn 5 pending tweaks into an item and attach on claim.
var DefaultTweakRules = TweakRules{BecomeItemAt: 5, AttachOnClaim: true}

// PendingTweaks returns unattached tweaks of an area.
func PendingTweaks(ts []*Tweak, area string) []*Tweak {
	var out []*Tweak
	for _, t := range ts {
		if t.ItemID == "" && AreaName(t.Area) == AreaName(area) {
			out = append(out, t)
		}
	}
	return out
}

// Question is an owner question in the fixed shape.
type Question struct {
	ID         string    `json:"id"`
	Version    int       `json:"version"`
	ItemID     string    `json:"item_id,omitempty"`
	Context    string    `json:"context"`  // one sentence
	Decision   string    `json:"decision"` // what is asked
	Options    []Option  `json:"options"`  // 2 to 4, one recommended
	IfNoAnswer string    `json:"if_no_answer"`
	AskedBy    string    `json:"asked_by"`
	AskedAt    time.Time `json:"asked_at,omitzero"`
	Deadline   time.Time `json:"deadline,omitzero"`
	Answer     string    `json:"answer,omitempty"`
	AnsweredBy string    `json:"answered_by,omitempty"`
	AnsweredAt time.Time `json:"answered_at,omitzero"`
	Urgent     bool      `json:"urgent,omitempty"`
}

// Option is one answer choice.
type Option struct {
	Label       string `json:"label"`
	Recommended bool   `json:"recommended,omitempty"`
	// Action is what choosing the option does, for options Barnraise itself
	// offers (retry, abandon, approve…): the label is in the owner's
	// language, the action is not. Empty for options an agent wrote.
	Action string `json:"action,omitempty"`
}

// ActionOf is the action of the option with this label, or "".
func (q *Question) ActionOf(label string) string {
	for _, o := range q.Options {
		if o.Label == label {
			return o.Action
		}
	}
	return ""
}

// Open reports whether nobody answered yet.
func (q *Question) Open() bool { return q.Answer == "" }

// Validate enforces the question template.
func (q *Question) Validate() error {
	if strings.TrimSpace(q.Context) == "" || strings.TrimSpace(q.Decision) == "" {
		return errors.New("question needs context and the decision asked")
	}
	if len(q.Options) < 2 || len(q.Options) > 4 {
		return errors.New("question needs 2 to 4 options")
	}
	rec := 0
	for _, o := range q.Options {
		if strings.TrimSpace(o.Label) == "" {
			return errors.New("question option without a label")
		}
		if o.Recommended {
			rec++
		}
	}
	if rec != 1 {
		return errors.New("question needs exactly one recommended option")
	}
	if strings.TrimSpace(q.IfNoAnswer) == "" {
		return errors.New("question must say what happens if nobody answers")
	}
	return nil
}

// Recommended returns the recommended option label.
func (q *Question) Recommended() string {
	for _, o := range q.Options {
		if o.Recommended {
			return o.Label
		}
	}
	return ""
}

// MatchAnswer resolves an answer to one of the options (by label, prefix or 1-based index).
func (q *Question) MatchAnswer(a string) (string, error) {
	a = strings.TrimSpace(a)
	for i, o := range q.Options {
		if strings.EqualFold(o.Label, a) || a == fmt.Sprint(i+1) {
			return o.Label, nil
		}
	}
	for _, o := range q.Options {
		if a != "" && strings.HasPrefix(strings.ToLower(o.Label), strings.ToLower(a)) {
			return o.Label, nil
		}
	}
	return "", fmt.Errorf("answer %q is not one of the options", a)
}

// Message is a short note between agents, always through the daemon and tied to an item.
type Message struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`
	To        string    `json:"to"` // agent name, role:<role> or item:<id> (whoever holds it)
	ItemID    string    `json:"item_id,omitempty"`
	Text      string    `json:"text"`
	At        time.Time `json:"at,omitzero"`
	Delivered bool      `json:"delivered,omitempty"`
}

// MaxMessageLen keeps messages short.
const MaxMessageLen = 2000
