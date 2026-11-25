package git

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/leandrodaf/barnraise/internal/ports"
)

var _ ports.ConflictRebaser = (*Repo)(nil)

// StartRebase rebases the branch in dir onto onto and stops at the first
// conflict nobody but a person or an agent can resolve.
func (r *Repo) StartRebase(ctx context.Context, dir, onto string, sides ports.RebaseSides) ([]string, error) {
	_, err := r.git(ctx, dir, r.withIdentity(ctx, "rebase", "--quiet", "--no-autostash", onto)...)
	return r.driveRebase(ctx, dir, err, sides)
}

// ContinueRebase picks up a stopped rebase after the conflicted files were edited.
func (r *Repo) ContinueRebase(ctx context.Context, dir string, sides ports.RebaseSides) ([]string, error) {
	if !r.rebaseInProgress(ctx, dir) {
		return nil, nil
	}
	left := r.stageResolved(ctx, dir, r.unmerged(ctx, dir), sides)
	if len(left) > 0 {
		return left, nil
	}
	return r.driveRebase(ctx, dir, r.continueOnce(ctx, dir), sides)
}

// driveRebase continues a rebase as long as every conflict of a stop is
// resolved without anyone, and returns the files of the first stop that
// is not. err is the result of the last rebase command.
func (r *Repo) driveRebase(ctx context.Context, dir string, err error, sides ports.RebaseSides) ([]string, error) {
	for stop := 0; err != nil; stop++ {
		if !r.rebaseInProgress(ctx, dir) {
			return nil, err
		}
		reused := reusedFiles(err)
		var left []string
		for _, f := range r.stageResolved(ctx, dir, r.unmerged(ctx, dir), sides) {
			if contains(reused, f) {
				if _, aerr := r.git(ctx, dir, "add", "--", f); aerr == nil {
					continue
				}
			}
			left = append(left, f)
		}
		if len(left) > 0 {
			return left, nil
		}
		if stop >= maxRebaseStops {
			return nil, err
		}
		err = r.continueOnce(ctx, dir)
	}
	return nil, nil
}

// continueOnce runs rebase --continue, skipping a commit the resolution
// left without a change of its own.
func (r *Repo) continueOnce(ctx context.Context, dir string) error {
	_, err := r.git(ctx, dir, r.withIdentity(ctx, "rebase", "--continue")...)
	if err != nil && r.rebaseInProgress(ctx, dir) && r.nothingStaged(ctx, dir) {
		_, err = r.git(ctx, dir, r.withIdentity(ctx, "rebase", "--skip")...)
	}
	return err
}

func (r *Repo) unmerged(ctx context.Context, dir string) []string {
	out, _ := r.git(ctx, dir, "diff", "--name-only", "--diff-filter=U", "-z")
	var files []string
	for f := range strings.SplitSeq(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files
}

// stageResolved resolves the files sides names, stages the files that no
// longer hold conflict markers and returns the rest.
func (r *Repo) stageResolved(ctx context.Context, dir string, files []string, sides ports.RebaseSides) []string {
	var left []string
	for _, f := range files {
		switch {
		case underAny(f, sides.Onto):
			// In a rebase, "ours" is the commit the branch is replayed on.
			if r.takeSide(ctx, dir, f, "--ours", ":2:") {
				continue
			}
		case underAny(f, sides.Own):
			if r.takeSide(ctx, dir, f, "--theirs", ":3:") {
				continue
			}
		default:
			if data, err := os.ReadFile(filepath.Join(r.dir(dir), f)); err == nil && !hasMarkers(data) {
				if _, err := r.git(ctx, dir, "add", "--", f); err == nil {
					continue
				}
			} else if os.IsNotExist(err) {
				// The file was deleted to resolve the conflict.
				if _, err := r.git(ctx, dir, "rm", "--quiet", "--cached", "--", f); err == nil {
					continue
				}
			}
		}
		left = append(left, f)
	}
	return left
}

// takeSide resolves f to one side; a side without the file deletes it.
func (r *Repo) takeSide(ctx context.Context, dir, f, flag, stage string) bool {
	if _, err := r.git(ctx, dir, "cat-file", "-e", stage+f); err != nil {
		_, err = r.git(ctx, dir, "rm", "--quiet", "--force", "--", f)
		return err == nil
	}
	if _, err := r.git(ctx, dir, "checkout", flag, "--", f); err != nil {
		return false
	}
	_, err := r.git(ctx, dir, "add", "--", f)
	return err == nil
}

func underAny(f string, paths []string) bool {
	for _, p := range paths {
		if f == p || (strings.HasSuffix(p, "/") && strings.HasPrefix(f, p)) {
			return true
		}
	}
	return false
}

// hasMarkers reports whether data still holds a conflict marker line.
func hasMarkers(data []byte) bool {
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("<<<<<<< ")) || bytes.HasPrefix(line, []byte(">>>>>>> ")) || bytes.Equal(bytes.TrimRight(line, "\r"), []byte("=======")) {
			return true
		}
	}
	return false
}

// Unfinished reports a rebase, merge, cherry-pick or revert in progress.
func (r *Repo) Unfinished(ctx context.Context, dir string) bool {
	if r.rebaseInProgress(ctx, dir) {
		return true
	}
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		if r.gitPathExists(ctx, dir, name) {
			return true
		}
	}
	return false
}

// AbortUnfinished aborts what Unfinished found.
func (r *Repo) AbortUnfinished(ctx context.Context, dir string) error {
	switch {
	case r.rebaseInProgress(ctx, dir):
		_, err := r.git(ctx, dir, "rebase", "--abort")
		return err
	case r.gitPathExists(ctx, dir, "MERGE_HEAD"):
		_, err := r.git(ctx, dir, "merge", "--abort")
		return err
	case r.gitPathExists(ctx, dir, "CHERRY_PICK_HEAD"):
		_, err := r.git(ctx, dir, "cherry-pick", "--abort")
		return err
	case r.gitPathExists(ctx, dir, "REVERT_HEAD"):
		_, err := r.git(ctx, dir, "revert", "--abort")
		return err
	}
	return nil
}
