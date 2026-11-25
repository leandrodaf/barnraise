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

// RebaseSides names the paths a stopped rebase resolves without anyone:
// files under Onto take the side of the commit the branch is rebased on
// (Barnraise's own files, which main owns), files under Own keep the
// branch's side (the item's memory). A path ending in "/" is a folder.
type RebaseSides struct {
	Onto []string
	Own  []string
}

// ConflictRebaser leaves a rebase stopped at its conflicts, so an agent
// that may not run the rebase itself (its sandbox keeps some files read
// only) only has to edit the conflicted files. Barnraise starts and
// finishes the rebase outside the sandbox.
type ConflictRebaser interface {
	// StartRebase rebases the branch in dir onto onto. Conflicts git
	// resolved from recorded resolutions and conflicts under sides are
	// resolved and continued; at any other conflict the rebase stays
	// stopped and the conflicted files are returned. No files: the rebase
	// finished.
	StartRebase(ctx context.Context, dir, onto string, sides RebaseSides) (conflicts []string, err error)
	// ContinueRebase stages the conflicted files that no longer hold
	// conflict markers and continues like StartRebase. The files it
	// returns still conflict, and the rebase stays stopped.
	ContinueRebase(ctx context.Context, dir string, sides RebaseSides) (conflicts []string, err error)
	// Unfinished reports whether a rebase, merge, cherry-pick or revert is
	// in progress in dir.
	Unfinished(ctx context.Context, dir string) bool
	// AbortUnfinished aborts whatever Unfinished found, leaving dir on
	// the branch as it was before.
	AbortUnfinished(ctx context.Context, dir string) error
}
