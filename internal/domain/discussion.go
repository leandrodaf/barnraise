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
}

// Roles in a discussion.
const (
	DiscussOwner   = "owner"
	DiscussAdvisor = "advisor"
)

// discussionWindow keeps the last messages of each conversation.
const discussionWindow = 60
