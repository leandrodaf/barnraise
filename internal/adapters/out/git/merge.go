package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/leandrodaf/barnraise/internal/ports"
)

var _ ports.ReusingRebaser = (*Repo)(nil)

// reusedRe matches git's notice that rerere resolved a file from a
// resolution recorded earlier: "Resolved" when it only rewrote the file,
// "Staged" when rerere.autoupdate also staged it.
var reusedRe = regexp.MustCompile(`(?m)^(?:Resolved|Staged) '(.+)' using previous resolution\.`)

// maxRebaseStops bounds the continue loop of one rebase.
const maxRebaseStops = 500

// Rebase rebases the branch checked out in dir onto rev onto. Conflicts git
// resolves from recorded resolutions (rerere) are continued; any other
// conflict aborts the rebase, leaving dir as it was, and returns
// ports.ErrConflict naming the files.
func (r *Repo) Rebase(ctx context.Context, dir, onto string) error {
	_, err := r.RebaseReusing(ctx, dir, onto)
	return err
}

// RebaseReusing is Rebase that also reports the files whose conflicts were
// resolved from a recorded resolution.
func (r *Repo) RebaseReusing(ctx context.Context, dir, onto string) (ports.RebaseOutcome, error) {
	var out ports.RebaseOutcome
	_, err := r.git(ctx, dir, r.withIdentity(ctx, "rebase", "--quiet", "--no-autostash", onto)...)
	for stop := 0; err != nil; stop++ {
		if !r.rebaseInProgress(ctx, dir) {
			return out, err
		}
		reused := reusedFiles(err)
		unmerged, _ := r.git(ctx, dir, "diff", "--name-only", "--diff-filter=U", "-z")
		var left []string
		for f := range strings.SplitSeq(unmerged, "\x00") {
			if f == "" {
				continue
			}
			if contains(reused, f) {
				// rerere.autoupdate off: the file is resolved but not staged.
				if _, aerr := r.git(ctx, dir, "add", "--", f); aerr == nil {
					continue
				}
			}
			left = append(left, f)
		}
		if len(reused) == 0 || len(left) > 0 || stop >= maxRebaseStops {
			return ports.RebaseOutcome{}, r.abortRebase(ctx, dir, err, left)
		}
		out.Reapplied = appendNew(out.Reapplied, reused...)
		_, err = r.git(ctx, dir, r.withIdentity(ctx, "rebase", "--continue")...)
		if err != nil && r.rebaseInProgress(ctx, dir) && r.nothingStaged(ctx, dir) {
			// The recorded resolution leaves this commit with no change of its own.
			_, err = r.git(ctx, dir, r.withIdentity(ctx, "rebase", "--skip")...)
		}
	}
	return out, nil
}

// abortRebase aborts the rebase in progress and returns the conflict error.
func (r *Repo) abortRebase(ctx context.Context, dir string, cause error, files []string) error {
	if len(files) == 0 {
		out, _ := r.git(ctx, dir, "diff", "--name-only", "--diff-filter=U")
		files = strings.Fields(out)
	}
	if _, aerr := r.git(ctx, dir, "rebase", "--abort"); aerr != nil {
		return fmt.Errorf("%w (abort failed: %v): %v", ports.ErrConflict, aerr, cause)
	}
	if len(files) == 0 {
		return fmt.Errorf("%w: %v", ports.ErrConflict, cause)
	}
	return fmt.Errorf("%w: %s", ports.ErrConflict, strings.Join(files, ", "))
}

// nothingStaged reports whether the index of dir equals HEAD and nothing
// is left unmerged.
func (r *Repo) nothingStaged(ctx context.Context, dir string) bool {
	if out, _ := r.git(ctx, dir, "diff", "--name-only", "--diff-filter=U"); strings.TrimSpace(out) != "" {
		return false
	}
	_, err := r.git(ctx, dir, "diff", "--cached", "--quiet", "HEAD")
	return err == nil
}

// reusedFiles reads rerere's notices from a failed git invocation.
func reusedFiles(err error) []string {
	ce, ok := errors.AsType[*CmdError](err)
	if !ok {
		return nil
	}
	var files []string
	for _, m := range reusedRe.FindAllStringSubmatch(ce.Stderr+"\n"+ce.Stdout, -1) {
		files = appendNew(files, m[1])
	}
	return files
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func appendNew(list []string, more ...string) []string {
	for _, s := range more {
		if !contains(list, s) {
			list = append(list, s)
		}
	}
	return list
}

// recordResolutions lets rerere record what was resolved in dir while a
// merge or a rebase is still in progress there (an agent resolving a
// conflict and leaving the commit to Barnraise). git commit and rebase
// --continue record on their own; this covers the files staged before.
// It does nothing when rerere is off.
func (r *Repo) recordResolutions(ctx context.Context, dir string) {
	if !r.gitPathExists(ctx, dir, "MERGE_RR") {
		return
	}
	_, _ = r.git(ctx, dir, "rerere")
}

// gitPathExists reports whether the file name exists in dir's git directory.
func (r *Repo) gitPathExists(ctx context.Context, dir, name string) bool {
	out, err := r.git(ctx, dir, "rev-parse", "--git-path", name)
	if err != nil {
		return false
	}
	path := strings.TrimSpace(out)
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.dir(dir), path)
	}
	_, err = os.Stat(path)
	return err == nil
}

// CommonDir is the git directory every worktree of the repository shares
// (where rr-cache and info/attributes live).
func (r *Repo) CommonDir(ctx context.Context) (string, error) {
	out, err := r.git(ctx, "", "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(out)
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.root, path)
	}
	return filepath.Clean(path), nil
}
