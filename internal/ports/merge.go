package ports

import "context"

// RebaseOutcome says how a rebase went.
type RebaseOutcome struct {
	// Reapplied lists the files whose conflicts git resolved from a
	// resolution it recorded earlier (rerere), so no one had to resolve
	// them again. The result still has to pass the checks.
	Reapplied []string
}

// ReusingRebaser is a Git that reports the conflicts a rebase resolved from
// recorded resolutions. A Git without it is rebased with Rebase, and any
// conflict sends the item back.
type ReusingRebaser interface {
	// RebaseReusing rebases like Rebase; a stop whose conflicts git resolved
	// entirely from recorded resolutions is continued instead of aborted.
	RebaseReusing(ctx context.Context, dir, onto string) (RebaseOutcome, error)
}
