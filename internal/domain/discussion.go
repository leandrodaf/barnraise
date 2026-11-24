package domain

import "time"

// EvDiscussion is one message of a conversation between the owner and the
// advisor about a decision that waits for the owner (a question or an item).
const EvDiscussion = "owner.discussion"

// DiscussionMessage is one turn of a conversation. Thread is the question
// (Q-7) or the item (I-43) the conversation is about.
type DiscussionMessage struct {
	Thread string    `json:"thread"`
	ItemID string    `json:"item_id,omitempty"`
	Role   string    `json:"role"` // owner or advisor
	Text   string    `json:"text"`
	By     string    `json:"by,omitempty"`    // who wrote it (the owner's name, the advisor's model)
	Model  string    `json:"model,omitempty"` // the advisor's model
	Tokens int64     `json:"tokens,omitempty"`
	At     time.Time `json:"at"`
	// SuggestOption and SuggestNote are the answer the advisor proposes:
	// one of the question's options (approve, reject or later for a
	// proposal) and what the owner could say with it.
	SuggestOption string `json:"suggest_option,omitempty"`
	SuggestNote   string `json:"suggest_note,omitempty"`
	// Delegated: the owner let the advisor decide, and this answer was applied.
	Delegated bool `json:"delegated,omitempty"`
	// LeftToOwner: the autopilot could not decide this one; Text says what
	// only the owner can do. It is not asked again until the decision changes.
	LeftToOwner bool `json:"left_to_owner,omitempty"`
}

// EvDiscussionStep is live only: what the advisor is doing while it
// prepares an answer (reading a file, searching), for the owner to follow.
const EvDiscussionStep = "owner.discussion.step"

// DiscussionStep is one action of the advisor: the tool (read, search,
// list) and what it acts on.
type DiscussionStep struct {
	Thread string    `json:"thread"`
	Tool   string    `json:"tool"`
	Target string    `json:"target,omitempty"`
	At     time.Time `json:"at"`
}

// Roles in a discussion.
const (
	DiscussOwner   = "owner"
	DiscussAdvisor = "advisor"
)

// discussionWindow keeps the last messages of each conversation.
const discussionWindow = 60
