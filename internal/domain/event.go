package domain

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Event types. Versioned names, the same in the API, CloudEvents and NATS.
const (
	EvItemCreated         = "item.created"
	EvItemUpdated         = "item.updated"
	EvItemClaimed         = "item.claimed"
	EvItemReleased        = "item.released"
	EvItemStepStarted     = "item.step.started"
	EvItemStepFinished    = "item.step.finished"
	EvItemWaitingOwner    = "item.waiting_owner"
	EvItemBlocked         = "item.blocked"
	EvItemQueued          = "item.queued"
	EvItemIntegrated      = "item.integrated"
	EvItemClosed          = "item.closed"
	EvItemPaused          = "item.paused"
	EvItemResumed         = "item.resumed"
	EvItemImpacted        = "item.impacted"
	EvItemRevalidated     = "item.revalidated"
	EvItemUpdatedByChange = "item.updated_by_change"
	EvItemSuperseded      = "item.superseded"
	EvItemObsoleted       = "item.obsoleted"
	EvItemMergedInto      = "item.merged_into"
	EvItemRedo            = "item.redo"
	EvDeliverySubmitted   = "delivery.submitted"
	EvReviewSubmitted     = "review.submitted"
	EvCheckRequested      = "check.requested"
	EvCheckCompleted      = "check.completed"
	EvReworkStarted       = "rework.started"
	EvGoalSet             = "goal.set"
	EvGoalFinished        = "goal.finished"
	EvFocusChanged        = "focus.changed"
	EvProposalCreated     = "proposal.created"
	EvProposalUpdated     = "proposal.updated"
	EvTweakAdded          = "tweak.added"
	EvTweakAttached       = "tweak.attached"
	EvOwnerQuestionAsked  = "owner.question.asked"
	EvOwnerQuestionAnswer = "owner.question.answered"
	EvMessageSent         = "message.sent"
	EvMessageDelivered    = "message.delivered"
	EvAccountUpdated      = "account.updated"
	EvAccountLimitReached = "account.limit.reached"
	EvMachineUpdated      = "machine.updated"
	EvMachineLeft         = "machine.left"
	EvPolicyViolation     = "policy.violation.blocked"
	EvSystemPaused        = "system.paused"
	EvSystemResumed       = "system.resumed"
	EvAgentSession        = "agent.session"
	EvAgentUsage          = "agent.usage"
	EvJournal             = "journal.entry"
	EvSecretBlocked       = "secret.blocked"
)

// Event is one entry of the append-only log. The current state is derived
// from the log, which gives the full history and makes redo possible.
type Event struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Project string          `json:"project"`
	Subject string          `json:"subject,omitempty"` // I-42, P-7, account id…
	Machine string          `json:"machine"`
	Actor   string          `json:"actor,omitempty"` // agent, owner, token name
	Time    time.Time       `json:"time,omitzero"`
	Seq     int64           `json:"seq,omitempty"` // local position in the log
	Data    json.RawMessage `json:"data,omitempty"`
}

// Payload is the data of an event. Entity snapshots let any machine rebuild
// the same state; the specific fields explain what happened.
type Payload struct {
	Item     *Item          `json:"item,omitempty"`
	Proposal *Proposal      `json:"proposal,omitempty"`
	Goal     *Goal          `json:"goal,omitempty"`
	Focus    *Focus         `json:"focus,omitempty"`
	Tweak    *Tweak         `json:"tweak,omitempty"`
	Question *Question      `json:"question,omitempty"`
	Message  *Message       `json:"message,omitempty"`
	Account  *Account       `json:"account,omitempty"`
	MachineI *Machine       `json:"machine,omitempty"`
	Delivery *Delivery      `json:"delivery,omitempty"`
	Review   *Review        `json:"review,omitempty"`
	Check    *CheckResult   `json:"check,omitempty"`
	Rework   *ReworkRecord  `json:"rework,omitempty"`
	Session  *SessionInfo   `json:"session,omitempty"`
	Usage    *TokenUsage    `json:"usage,omitempty"`
	Journal  *JournalEntry  `json:"journal,omitempty"`
	Cause    string         `json:"cause,omitempty"` // link to the change that caused it
	Note     string         `json:"note,omitempty"`
	Extra    map[string]any `json:"extra,omitempty"`
}

// DecodePayload reads the payload of an event.
func (e Event) DecodePayload() (Payload, error) {
	var p Payload
	if len(e.Data) == 0 {
		return p, nil
	}
	err := json.Unmarshal(e.Data, &p)
	return p, err
}

// NewEvent builds an event with a payload.
func NewEvent(id, typ, project, subject, machine, actor string, at time.Time, p Payload) Event {
	data, _ := json.Marshal(p)
	return Event{ID: id, Type: typ, Project: project, Subject: subject, Machine: machine, Actor: actor, Time: at.UTC(), Data: data}
}

// LiveOnly reports whether an event carries live state (a machine's
// heartbeat and readings, an agent session's progress): only its latest
// value matters, so it is never stored and never kept as history.
func (e Event) LiveOnly() bool {
	return e.Type == EvMachineUpdated || e.Type == EvAgentSession
}

// GPUStat is the live state of one GPU of a machine.
type GPUStat struct {
	Name       string  `json:"name"`
	UtilPct    float64 `json:"util_pct"`
	MemUsedGB  float64 `json:"mem_used_gb"`
	MemTotalGB float64 `json:"mem_total_gb"`
	TempC      float64 `json:"temp_c,omitempty"`
}

// MemUsedPct is the share of memory in use (0 when unknown).
func (m Machine) MemUsedPct() float64 {
	if m.MemGB <= 0 || m.FreeMemGB <= 0 {
		return 0
	}
	return (1 - m.FreeMemGB/m.MemGB) * 100
}

// CPUUsedPct is the share of the CPUs in use by the 1-minute load (0 when unknown).
func (m Machine) CPUUsedPct() float64 {
	if m.CPUs <= 0 || m.Load1 <= 0 {
		return 0
	}
	p := m.Load1 / float64(m.CPUs) * 100
	if p > 100 {
		p = 100
	}
	return p
}

// Machine is a computer of the cluster; all are equal, none is central.
type Machine struct {
	Name      string  `json:"name"`
	Version   string  `json:"version"`
	Address   string  `json:"address,omitempty"`
	CPUs      int     `json:"cpus"`
	MemGB     float64 `json:"mem_gb"`
	FreeCPU   float64 `json:"free_cpu"`
	FreeMemGB float64 `json:"free_mem_gb,omitempty"` // memory available right now; 0 = unknown
	// Live readings, refreshed with every heartbeat. Zero means the machine
	// does not report that reading.
	Load1     float64   `json:"load1,omitempty"`  // 1-minute load average
	TempC     float64   `json:"temp_c,omitempty"` // CPU temperature, °C
	GPUs      []GPUStat `json:"gpus,omitempty"`
	Emulators int       `json:"emulators"`
	GPU       bool      `json:"gpu"`
	Labels    []string  `json:"labels,omitempty"`
	Slots     int       `json:"slots"`
	Busy      int       `json:"busy"`
	Accounts  []string  `json:"accounts,omitempty"`
	LastSeen  time.Time `json:"last_seen,omitzero"`
	Leaving   bool      `json:"leaving,omitempty"`
	Platform  string    `json:"platform,omitempty"` // linux/amd64
	Binary    string    `json:"binary,omitempty"`   // sha256 of the barnraise binary, fetchable from the machine's object store
	Left      bool      `json:"left,omitempty"`
}

// Health returns green, yellow or red by heartbeat age.
func (m *Machine) Health(now time.Time, ttl time.Duration) string {
	switch age := now.Sub(m.LastSeen); {
	case m.Left:
		return "gray"
	case age <= ttl/2:
		return "green"
	case age <= ttl:
		return "yellow"
	default:
		return "red"
	}
}

// HasLabel checks a capability label such as "emulators" or "gpu".
func (m *Machine) HasLabel(l string) bool {
	switch l {
	case "emulators", "emulator":
		return m.Emulators > 0
	case "gpu":
		return m.GPU
	}
	return contains(m.Labels, l)
}

// SessionInfo describes an agent session.
type SessionInfo struct {
	Agent     string    `json:"agent"`
	Role      string    `json:"role"`
	Account   string    `json:"account"`
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
	Effort    string    `json:"effort,omitempty"`
	ItemID    string    `json:"item_id,omitempty"`
	Step      string    `json:"step,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	State     string    `json:"state"` // starting, working, waiting, idle, done, failed
	Detail    string    `json:"detail,omitempty"`
	At        time.Time `json:"at,omitzero"`
}

// TokenUsage is counted per step.
type TokenUsage struct {
	Agent        string        `json:"agent"`
	Account      string        `json:"account"`
	Provider     string        `json:"provider"`
	Model        string        `json:"model"`
	Role         string        `json:"role"`
	ItemID       string        `json:"item_id,omitempty"`
	Step         string        `json:"step,omitempty"`
	Input        int64         `json:"input"`
	Output       int64         `json:"output"`
	CacheRead    int64         `json:"cache_read"`
	CacheWrite   int64         `json:"cache_write"`
	CostUSD      float64       `json:"cost_usd,omitempty"`
	Duration     time.Duration `json:"duration"`
	ExploreReads int           `json:"explore_reads,omitempty"` // reads outside the context pack
}

// Total tokens counted against limits.
func (u TokenUsage) Total() int64 { return u.Input + u.Output + u.CacheRead + u.CacheWrite }

// JournalEntry is what an agent writes at the end of a step.
type JournalEntry struct {
	Agent  string    `json:"agent"`
	ItemID string    `json:"item_id,omitempty"`
	Step   string    `json:"step,omitempty"`
	Text   string    `json:"text"`
	At     time.Time `json:"at,omitzero"`
}

// State is the projection of the event log for one project.
type State struct {
	Project   string                  `json:"project"`
	Items     map[string]*Item        `json:"items"`
	Goals     map[string]*Goal        `json:"goals"`
	Focus     *Focus                  `json:"focus,omitempty"`
	Proposals map[string]*Proposal    `json:"proposals"`
	Tweaks    map[string]*Tweak       `json:"tweaks"`
	Questions map[string]*Question    `json:"questions"`
	Messages  []*Message              `json:"messages"`
	Accounts  map[string]*Account     `json:"accounts"`
	Machines  map[string]*Machine     `json:"machines"`
	Sessions  map[string]*SessionInfo `json:"sessions"` // by agent
	Journal   []*JournalEntry         `json:"journal"`
	Paused    bool                    `json:"paused"`
	PausedWhy string                  `json:"paused_why,omitempty"`
	Seen      map[string]bool         `json:"-"` // applied event ids
	LastSeq   int64                   `json:"last_seq"`
	Counters  map[IDKind]int          `json:"counters"`
}

// NewState creates an empty projection.
func NewState(project string) *State {
	return &State{
		Project: project, Items: map[string]*Item{}, Goals: map[string]*Goal{}, Proposals: map[string]*Proposal{},
		Tweaks: map[string]*Tweak{}, Questions: map[string]*Question{}, Accounts: map[string]*Account{},
		Machines: map[string]*Machine{}, Sessions: map[string]*SessionInfo{}, Seen: map[string]bool{}, Counters: map[IDKind]int{},
	}
}

// journalWindow keeps the journal short.
const journalWindow = 200

// messageWindow bounds kept messages.
const messageWindow = 2000

// Apply folds an event into the state. It is idempotent: an event id is
// applied once, and snapshots only replace older versions.
func (s *State) Apply(e Event) error {
	if e.ID != "" {
		if s.Seen[e.ID] {
			return nil
		}
		s.Seen[e.ID] = true
	}
	if e.Seq > s.LastSeq {
		s.LastSeq = e.Seq
	}
	p, err := e.DecodePayload()
	if err != nil {
		return fmt.Errorf("event %s: %w", e.ID, err)
	}
	if p.Item != nil {
		cur := s.Items[p.Item.ID]
		if cur == nil || p.Item.Version > cur.Version || (p.Item.Version == cur.Version && sameVersionWins(p.Item, cur)) {
			s.Items[p.Item.ID] = p.Item
		}
		s.bump(p.Item.ID)
	}
	if p.Proposal != nil {
		cur := s.Proposals[p.Proposal.ID]
		if cur == nil || p.Proposal.Version >= cur.Version {
			s.Proposals[p.Proposal.ID] = p.Proposal
		}
		s.bump(p.Proposal.ID)
	}
	if p.Goal != nil {
		if p.Goal.Active {
			for _, g := range s.Goals {
				if g.ID != p.Goal.ID {
					g.Active = false
				}
			}
		}
		s.Goals[p.Goal.ID] = p.Goal
		s.bump(p.Goal.ID)
	}
	if e.Type == EvFocusChanged {
		s.Focus = p.Focus
	}
	if p.Tweak != nil {
		s.Tweaks[p.Tweak.ID] = p.Tweak
	}
	if p.Question != nil {
		cur := s.Questions[p.Question.ID]
		if cur == nil || p.Question.Version >= cur.Version {
			s.Questions[p.Question.ID] = p.Question
		}
		s.bump(p.Question.ID)
	}
	if p.Message != nil {
		switch e.Type {
		case EvMessageSent:
			s.Messages = append(s.Messages, p.Message)
			if len(s.Messages) > messageWindow {
				s.Messages = s.Messages[len(s.Messages)-messageWindow:]
			}
		case EvMessageDelivered:
			for _, m := range s.Messages {
				if m.ID == p.Message.ID {
					m.Delivered = true
				}
			}
		}
	}
	if p.Account != nil {
		s.Accounts[p.Account.ID] = p.Account
	}
	if p.MachineI != nil {
		cur := s.Machines[p.MachineI.Name]
		if cur == nil || !p.MachineI.LastSeen.Before(cur.LastSeen) || e.Type == EvMachineLeft {
			s.Machines[p.MachineI.Name] = p.MachineI
		}
	}
	if p.Session != nil {
		s.Sessions[p.Session.Agent] = p.Session
	}
	if p.Journal != nil {
		s.Journal = append(s.Journal, p.Journal)
		if len(s.Journal) > journalWindow {
			s.Journal = s.Journal[len(s.Journal)-journalWindow:]
		}
	}
	switch e.Type {
	case EvSystemPaused:
		s.Paused, s.PausedWhy = true, p.Note
	case EvSystemResumed:
		s.Paused, s.PausedWhy = false, ""
	}
	return nil
}

// sameVersionWins settles two snapshots of the same item version, written
// concurrently by two machines: the later update wins, and an exact tie is
// broken by the snapshot bytes so every machine keeps the same one whatever
// the delivery order.
func sameVersionWins(next, cur *Item) bool {
	if !next.UpdatedAt.Equal(cur.UpdatedAt) {
		return next.UpdatedAt.After(cur.UpdatedAt)
	}
	a, _ := json.Marshal(next)
	b, _ := json.Marshal(cur)
	return string(a) >= string(b)
}

func (s *State) bump(id string) {
	k, n, err := ParseID(id)
	if err == nil && n > s.Counters[k] {
		s.Counters[k] = n
	}
}

// NextID returns the next free local id of a kind.
func (s *State) NextID(k IDKind) string { return FormatID(k, s.Counters[k]+1) }

// ItemList returns items sorted by priority.
func (s *State) ItemList() []*Item {
	out := make([]*Item, 0, len(s.Items))
	for _, it := range s.Items {
		out = append(out, it)
	}
	SortItems(out)
	return out
}

// ProposalList returns proposals by id.
func (s *State) ProposalList() []*Proposal {
	out := make([]*Proposal, 0, len(s.Proposals))
	for _, p := range s.Proposals {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return idNum(out[i].ID) < idNum(out[j].ID) })
	return out
}

// ActiveGoal returns the active goal, if any.
func (s *State) ActiveGoal() *Goal {
	var best *Goal
	for _, g := range s.Goals {
		if g.Active && (best == nil || idNum(g.ID) > idNum(best.ID)) {
			best = g
		}
	}
	return best
}

// Inbox returns undelivered messages for an agent, its role or its item.
func (s *State) Inbox(agent, role, itemID string) []*Message {
	var out []*Message
	for _, m := range s.Messages {
		if m.Delivered {
			continue
		}
		if m.To == agent || (role != "" && m.To == "role:"+role) || (itemID != "" && m.To == "item:"+itemID) {
			out = append(out, m)
		}
	}
	return out
}

// OpenQuestions returns unanswered owner questions, oldest first.
func (s *State) OpenQuestions() []*Question {
	var out []*Question
	for _, q := range s.Questions {
		if q.Open() {
			out = append(out, q)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AskedAt.Before(out[j].AskedAt) })
	return out
}

// AccountList returns accounts sorted by id.
func (s *State) AccountList() []*Account {
	out := make([]*Account, 0, len(s.Accounts))
	for _, a := range s.Accounts {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// TweakList returns all tweaks.
func (s *State) TweakList() []*Tweak {
	out := make([]*Tweak, 0, len(s.Tweaks))
	for _, t := range s.Tweaks {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}
