package ports

import (
	"context"
	"time"
)

// FileTouch is a file an item's agent sessions read or changed on this machine.
type FileTouch struct {
	Path  string    `json:"path"` // relative to the worktree root
	Write bool      `json:"write,omitempty"`
	At    time.Time `json:"at,omitzero"`
}

// FileReadLog remembers, per item, the files its agent sessions read or
// changed, across steps and daemon restarts. It is local to the machine
// running the sessions and never replicated. An EventStore may implement
// it; the stale-read warning then uses it.
type FileReadLog interface {
	NoteFiles(ctx context.Context, project, itemID string, files []FileTouch) error
	TouchedFiles(ctx context.Context, project, itemID string) ([]FileTouch, error)
	// ReadItems lists the items that have files noted.
	ReadItems(ctx context.Context, project string) ([]string, error)
	ForgetFiles(ctx context.Context, project, itemID string) error
}
