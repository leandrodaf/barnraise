package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// ErrPushRejected is returned by Push when the remote refused the update,
// usually because the branch moved since it was last fetched.
var ErrPushRejected = errors.New("push rejected")

var _ ports.Git = (*Repo)(nil)

var remoteNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Repo is a git repository driven through the git binary.
type Repo struct {
	root   string
	remote string // remote name or URL; "" means local only
	main   string
	alias  string // namespace for remote-tracking refs: refs/remotes/<alias>/

	idOnce sync.Once
	idArgs []string

	removing sync.WaitGroup // background deletions of removed worktrees
}

// New returns a Repo rooted at root. remote is a remote name (for example
// "origin") or URL; "" means there is no remote and everything stays local.
// mainBranch defaults to "main".
func New(root, remote, mainBranch string) *Repo {
	if mainBranch == "" {
		mainBranch = "main"
	}
	alias := "barnraise"
	if remote != "" && remoteNameRe.MatchString(remote) {
		alias = remote
	}
	return &Repo{root: root, remote: remote, main: mainBranch, alias: alias}
}

// Root is the repository directory.
func (r *Repo) Root() string { return r.root }

// Remote is the configured remote ("" when local only).
func (r *Repo) Remote() string { return r.remote }

// HasRemote reports whether a remote is configured.
func (r *Repo) HasRemote() bool { return r.remote != "" }

// MainBranch is the name of the main branch.
func (r *Repo) MainBranch() string { return r.main }

// TrackingRef is the local ref that mirrors branch on the remote.
func (r *Repo) TrackingRef(branch string) string {
	return "refs/remotes/" + r.alias + "/" + branch
}

func (r *Repo) dir(d string) string {
	if d == "" {
		return r.root
	}
	return d
}

func (r *Repo) git(ctx context.Context, dir string, args ...string) (string, error) {
	return run(ctx, r.dir(dir), nil, args...)
}

func (r *Repo) gitEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	out, err := runEnv(ctx, r.dir(dir), nil, env, args...)
	return strings.TrimSpace(out), err
}

func (r *Repo) gitIn(ctx context.Context, dir, stdin string, args ...string) (string, error) {
	return run(ctx, r.dir(dir), strings.NewReader(stdin), args...)
}

// identity returns -c flags with the fallback identity when the repository
// has no user configured.
func (r *Repo) identity(ctx context.Context) []string {
	r.idOnce.Do(func() {
		name, _ := r.git(ctx, "", "config", "--get", "user.name")
		email, _ := r.git(ctx, "", "config", "--get", "user.email")
		if strings.TrimSpace(name) == "" || strings.TrimSpace(email) == "" {
			r.idArgs = []string{"-c", "user.name=" + fallbackName, "-c", "user.email=" + fallbackEmail}
		}
	})
	return r.idArgs
}

func (r *Repo) withIdentity(ctx context.Context, args ...string) []string {
	return append(append([]string{}, r.identity(ctx)...), args...)
}

// resolve returns the commit sha of rev, ok=false when it does not exist.
func (r *Repo) resolve(ctx context.Context, dir, rev string) (string, bool, error) {
	out, err := r.git(ctx, dir, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	if err != nil {
		if exitCode(err) == 1 {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.TrimSpace(out), true, nil
}

// refValue returns the object a ref points to, ok=false when missing.
func (r *Repo) refValue(ctx context.Context, ref string) (string, bool, error) {
	out, err := r.git(ctx, "", "rev-parse", "--verify", "--quiet", "--end-of-options", ref)
	if err != nil {
		if exitCode(err) == 1 {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.TrimSpace(out), true, nil
}

func (r *Repo) isAncestor(ctx context.Context, a, b string) (bool, error) {
	_, err := r.git(ctx, "", "merge-base", "--is-ancestor", a, b)
	if err == nil {
		return true, nil
	}
	if exitCode(err) == 1 {
		return false, nil
	}
	return false, err
}

// Fetch updates the remote-tracking refs of all branches. No-op without remote.
func (r *Repo) Fetch(ctx context.Context) error {
	if !r.HasRemote() {
		return nil
	}
	_, err := r.git(ctx, "", "fetch", "--prune", "--no-tags", "--no-write-fetch-head", r.remote,
		"+refs/heads/*:"+r.TrackingRef("*"))
	if err != nil {
		return fmt.Errorf("%w: %v", ports.ErrOffline, err)
	}
	return nil
}

// MainHead returns the remote-tracking main (as of the last Fetch) when there
// is a remote, falling back to the local main branch.
func (r *Repo) MainHead(ctx context.Context) (string, error) {
	if r.HasRemote() {
		sha, ok, err := r.resolve(ctx, "", r.TrackingRef(r.main))
		if err != nil {
			return "", err
		}
		if ok {
			return sha, nil
		}
	}
	sha, ok, err := r.resolve(ctx, "", "refs/heads/"+r.main)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("main branch %q not found", r.main)
	}
	return sha, nil
}

// EnsureBranch makes sure branch exists locally (and on the remote). If the
// remote already has it, the local branch is created from (or fast-forwarded
// to) the remote one, so a machine taking over an item continues from the
// last pushed step. Otherwise it is created from rev from and pushed.
func (r *Repo) EnsureBranch(ctx context.Context, branch, from string) error {
	local := "refs/heads/" + branch
	localSHA, localOK, err := r.resolve(ctx, "", local)
	if err != nil {
		return err
	}
	if r.HasRemote() {
		found, err := r.FetchBranch(ctx, branch)
		if err != nil {
			return err
		}
		if found {
			remoteSHA, _, err := r.resolve(ctx, "", r.TrackingRef(branch))
			if err != nil {
				return err
			}
			switch {
			case !localOK:
				_, err = r.git(ctx, "", "update-ref", local, remoteSHA, "")
				return err
			case localSHA == remoteSHA:
				return nil
			default:
				anc, err := r.isAncestor(ctx, localSHA, remoteSHA)
				if err != nil || !anc {
					return err
				}
				wts, err := r.worktreesOn(ctx, branch)
				if err != nil || len(wts) > 0 {
					return err // checked out somewhere: leave it to the worktree owner
				}
				_, err = r.git(ctx, "", "update-ref", local, remoteSHA, localSHA)
				return err
			}
		}
	}
	if !localOK {
		sha, ok, err := r.resolve(ctx, "", from)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("ensure branch %s: rev %q not found", branch, from)
		}
		if _, err := r.git(ctx, "", "update-ref", local, sha, ""); err != nil {
			return err
		}
	}
	if r.HasRemote() {
		return r.Push(ctx, "", branch)
	}
	return nil
}

// AddWorktree checks branch out in dir. It is idempotent when dir already is
// a worktree on that branch.
func (r *Repo) AddWorktree(ctx context.Context, dir, branch string) error {
	if _, err := os.Stat(dir); err == nil {
		out, err := r.git(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
		if err == nil && strings.TrimSpace(out) == branch {
			top, err := r.git(ctx, dir, "rev-parse", "--show-toplevel")
			if err == nil && samePath(strings.TrimSpace(top), dir) {
				return nil
			}
		}
	}
	_, _ = r.git(ctx, "", "worktree", "prune")
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	_, err := r.git(ctx, "", "worktree", "add", dir, branch)
	return err
}

// MergeBase returns the best common ancestor of a and b.
func (r *Repo) MergeBase(ctx context.Context, a, b string) (string, error) {
	out, err := r.git(ctx, "", "merge-base", a, b)
	return strings.TrimSpace(out), err
}

// AddDetachedWorktree checks out rev in dir without creating a branch.
func (r *Repo) AddDetachedWorktree(ctx context.Context, dir, rev string) error {
	_, _ = r.git(ctx, "", "worktree", "prune")
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	_, err := r.git(ctx, "", "worktree", "add", "--detach", dir, rev)
	return err
}

// PushRev sets branch to rev locally and, with a remote, pushes it forced.
func (r *Repo) PushRev(ctx context.Context, rev, branch string) error {
	if _, err := r.git(ctx, "", "update-ref", "refs/heads/"+branch, rev); err != nil {
		return err
	}
	if r.remote == "" {
		return nil
	}
	_, err := r.git(ctx, "", "push", "--force", "--no-verify", r.remote, rev+":refs/heads/"+branch)
	return err
}

// ResetBranch points branch at rev and checks it out in dir, discarding local changes.
func (r *Repo) ResetBranch(ctx context.Context, dir, branch, rev string) error {
	_, err := r.git(ctx, dir, "checkout", "--force", "-B", branch, rev)
	return err
}

// Checkout moves the worktree in dir to rev, detached, discarding changes.
func (r *Repo) Checkout(ctx context.Context, dir, rev string) error {
	_, err := r.git(ctx, dir, "checkout", "--force", "--detach", rev)
	return err
}

func samePath(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// RemoveWorktree removes the worktree in dir, discarding local changes, and
// prunes stale worktree entries. The directory leaves its place at once (a
// rename) and its files are deleted in the background: a worktree with its
// dependencies holds hundreds of thousands of files, and the caller (the
// merge queue, for one) does not wait for that.
func (r *Repo) RemoveWorktree(ctx context.Context, dir string) error {
	if _, statErr := os.Lstat(dir); os.IsNotExist(statErr) {
		_, perr := r.git(ctx, "", "worktree", "prune")
		return perr
	}
	trash, err := ports.MoveToTrash(dir)
	if err != nil {
		// The rename was refused (another filesystem, permissions): the slow way.
		_, err = r.git(ctx, "", "worktree", "remove", "--force", "--force", dir)
		_, perr := r.git(ctx, "", "worktree", "prune")
		if err != nil {
			if _, statErr := os.Stat(dir); os.IsNotExist(statErr) {
				return perr
			}
			return err
		}
		return perr
	}
	// The entry now points at a directory that is gone: prune forgets it and
	// frees the branch.
	_, perr := r.git(ctx, "", "worktree", "prune")
	r.removing.Go(func() { _ = ports.RemoveTree(trash) })
	return perr
}

// WaitRemovals waits for the background deletions started by RemoveWorktree.
func (r *Repo) WaitRemovals() { r.removing.Wait() }

// worktreesOn lists worktree paths that have branch checked out.
func (r *Repo) worktreesOn(ctx context.Context, branch string) ([]string, error) {
	out, err := r.git(ctx, "", "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var paths []string
	var cur string
	for line := range strings.SplitSeq(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			cur = strings.TrimPrefix(line, "worktree ")
		case line == "branch refs/heads/"+branch:
			paths = append(paths, cur)
		}
	}
	return paths, nil
}

// CommitAll stages everything in dir and commits when there are changes.
// It returns the HEAD sha and whether a commit was made.
func (r *Repo) CommitAll(ctx context.Context, dir, message string) (string, bool, error) {
	r.recordResolutions(ctx, dir)
	if _, err := r.git(ctx, dir, "add", "-A"); err != nil {
		return "", false, err
	}
	_, err := r.git(ctx, dir, "diff", "--cached", "--quiet")
	changed := false
	switch {
	case err == nil:
	case exitCode(err) == 1:
		changed = true
	default:
		if _, ok, _ := r.resolve(ctx, dir, "HEAD"); ok {
			return "", false, err
		}
		changed = true // unborn branch: diff --cached fails, commit anyway
	}
	if changed {
		if _, err := r.gitIn(ctx, dir, message, r.withIdentity(ctx, "commit", "--quiet", "-F", "-")...); err != nil {
			return "", false, err
		}
	}
	sha, err := r.Head(ctx, dir)
	return sha, changed, err
}

// Push publishes branch to the remote with --force-with-lease against the
// last fetched (or pushed) value, since item branches are rebased. No-op
// without remote.
func (r *Repo) Push(ctx context.Context, dir, branch string) error {
	if !r.HasRemote() {
		return nil
	}
	local := "refs/heads/" + branch
	sha, ok, err := r.resolve(ctx, dir, local)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("push: branch %s not found", branch)
	}
	tracking := r.TrackingRef(branch)
	expect, _, err := r.resolve(ctx, dir, tracking)
	if err != nil {
		return err
	}
	if expect == sha {
		return nil
	}
	out, err := r.git(ctx, dir, "push", "--porcelain", "--force-with-lease="+local+":"+expect, r.remote, local+":"+local)
	if err != nil {
		if rejected(out) {
			return fmt.Errorf("%w: %s: %v", ErrPushRejected, branch, err)
		}
		return fmt.Errorf("%w: %v", ports.ErrOffline, err)
	}
	_, err = r.git(ctx, dir, "update-ref", tracking, sha)
	return err
}

// rejected reports whether push --porcelain output has a refused ref.
func rejected(out string) bool {
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "!") {
			return true
		}
	}
	return false
}

// FetchBranch updates the remote-tracking ref of branch and reports whether
// the remote has it. Without remote it reports whether the local branch exists.
func (r *Repo) FetchBranch(ctx context.Context, branch string) (bool, error) {
	if !r.HasRemote() {
		_, ok, err := r.resolve(ctx, "", "refs/heads/"+branch)
		return ok, err
	}
	ref := "refs/heads/" + branch
	out, err := r.git(ctx, "", "ls-remote", r.remote, ref)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ports.ErrOffline, err)
	}
	if !hasExactRef(out, ref) {
		_, _ = r.git(ctx, "", "update-ref", "-d", r.TrackingRef(branch))
		return false, nil
	}
	_, err = r.git(ctx, "", "fetch", "--no-tags", "--no-write-fetch-head", r.remote, "+"+ref+":"+r.TrackingRef(branch))
	if err != nil {
		return false, fmt.Errorf("%w: %v", ports.ErrOffline, err)
	}
	return true, nil
}

// parseLsRemote returns the sha of ref in ls-remote output.
func parseLsRemote(out, ref string) (string, bool) {
	for line := range strings.SplitSeq(out, "\n") {
		sha, name, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && name == ref {
			return sha, true
		}
	}
	return "", false
}

func hasExactRef(out, ref string) bool {
	_, ok := parseLsRemote(out, ref)
	return ok
}

func (r *Repo) rebaseInProgress(ctx context.Context, dir string) bool {
	for _, p := range []string{"rebase-merge", "rebase-apply"} {
		out, err := r.git(ctx, dir, "rev-parse", "--git-path", p)
		if err != nil {
			continue
		}
		path := strings.TrimSpace(out)
		if !filepath.IsAbs(path) {
			path = filepath.Join(r.dir(dir), path)
		}
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

// Head returns the HEAD commit of dir ("" means the repository root).
func (r *Repo) Head(ctx context.Context, dir string) (string, error) {
	sha, ok, err := r.resolve(ctx, dir, "HEAD")
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("HEAD has no commit")
	}
	return sha, nil
}

// ChangedFiles lists paths that differ between from and to (two-dot diff).
// Renames are reported as both the old and the new path.
func (r *Repo) ChangedFiles(ctx context.Context, from, to string) ([]string, error) {
	out, err := r.git(ctx, "", "diff", "--name-only", "--no-renames", "-z", from, to, "--")
	if err != nil {
		return nil, err
	}
	var files []string
	for f := range strings.SplitSeq(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// Diff returns the unified diff between from and to, cut at max bytes when
// max > 0.
func (r *Repo) Diff(ctx context.Context, from, to string, max int) (string, error) {
	out, err := r.git(ctx, "", "diff", "--no-color", "--no-ext-diff", from, to, "--")
	if err != nil {
		return "", err
	}
	if max > 0 && len(out) > max {
		return out[:max] + fmt.Sprintf("\n[diff truncated: %d of %d bytes]\n", max, len(out)), nil
	}
	return out, nil
}

// ExistsAt reports whether path exists in rev.
func (r *Repo) ExistsAt(ctx context.Context, rev, path string) bool {
	_, err := r.git(ctx, "", "cat-file", "-e", rev+":"+path)
	return err == nil
}

// Show returns the content of path at rev.
func (r *Repo) Show(ctx context.Context, rev, path string) ([]byte, error) {
	out, err := r.git(ctx, "", "cat-file", "blob", rev+":"+path)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// SquashOnto creates (without moving any ref) one commit whose tree is the
// tree of HEAD in dir and whose only parent is base. It returns its sha.
func (r *Repo) SquashOnto(ctx context.Context, dir, base, message string, exclude ...string) (string, error) {
	tree, err := r.git(ctx, dir, "rev-parse", "--verify", "HEAD^{tree}")
	if err != nil {
		return "", err
	}
	if len(exclude) > 0 {
		// Build the tree without the item's own files (plan.md, criteria.json) in a private index.
		idx, err := os.CreateTemp("", "barnraise-index-*")
		if err != nil {
			return "", err
		}
		idxPath := idx.Name()
		idx.Close()
		os.Remove(idxPath)
		defer os.Remove(idxPath)
		env := []string{"GIT_INDEX_FILE=" + idxPath}
		if _, err := r.gitEnv(ctx, dir, env, "read-tree", "HEAD"); err != nil {
			return "", err
		}
		args := append([]string{"rm", "--cached", "--quiet", "--ignore-unmatch", "--"}, exclude...)
		if _, err := r.gitEnv(ctx, dir, env, args...); err != nil {
			return "", err
		}
		tree, err = r.gitEnv(ctx, dir, env, "write-tree")
		if err != nil {
			return "", err
		}
	}
	baseSHA, ok, err := r.resolve(ctx, dir, base)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("squash: base %q not found", base)
	}
	out, err := r.gitIn(ctx, dir, message, r.withIdentity(ctx, "commit-tree", strings.TrimSpace(tree), "-p", baseSHA, "-F", "-")...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

const logFormat = "%x1e%H%x1f%an <%ae>%x1f%aI%x1f%B%x1f%(trailers:only,unfold)%x1f"

// Log runs git log with args (revisions, paths, limits) and parses each
// commit with its trailers and changed files.
func (r *Repo) Log(ctx context.Context, args ...string) ([]ports.Commit, error) {
	full := append([]string{"-c", "core.quotePath=false", "log", "--format=" + logFormat, "--name-only", "--no-renames"}, args...)
	out, err := r.git(ctx, "", full...)
	if err != nil {
		return nil, err
	}
	return parseLog(out), nil
}

func parseLog(out string) []ports.Commit {
	var commits []ports.Commit
	for rec := range strings.SplitSeq(out, "\x1e") {
		if strings.TrimSpace(rec) == "" {
			continue
		}
		f := strings.SplitN(rec, "\x1f", 6)
		if len(f) < 6 {
			continue
		}
		c := ports.Commit{SHA: strings.TrimSpace(f[0]), Author: f[1]}
		c.Time, _ = time.Parse(time.RFC3339, strings.TrimSpace(f[2]))
		msg := strings.TrimSpace(f[3])
		subj, body, _ := strings.Cut(msg, "\n\n")
		c.Subject = strings.Join(strings.Fields(subj), " ")
		c.Body = strings.TrimSpace(body)
		c.Trailers = parseTrailers(f[4])
		if len(c.Trailers) == 0 {
			c.Trailers = parseTrailers(lastParagraph(c.Body))
		}
		for line := range strings.SplitSeq(f[5], "\n") {
			if line = strings.TrimSpace(line); line != "" {
				c.Files = append(c.Files, line)
			}
		}
		commits = append(commits, c)
	}
	return commits
}

var trailerRe = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9-]*):\s*(.*)$`)

// parseTrailers reads "Key: value" lines; the last value of a key wins.
func parseTrailers(s string) map[string]string {
	var m map[string]string
	for line := range strings.SplitSeq(s, "\n") {
		mt := trailerRe.FindStringSubmatch(strings.TrimSpace(line))
		if mt == nil {
			continue
		}
		if m == nil {
			m = map[string]string{}
		}
		m[mt[1]] = strings.TrimSpace(mt[2])
	}
	return m
}

// lastParagraph returns the final paragraph of body when every line of it
// looks like a trailer.
func lastParagraph(body string) string {
	parts := strings.Split(body, "\n\n")
	p := strings.TrimSpace(parts[len(parts)-1])
	for line := range strings.SplitSeq(p, "\n") {
		if !trailerRe.MatchString(strings.TrimSpace(line)) {
			return ""
		}
	}
	return p
}

// Blame returns the sha of the commit that last changed line (1-based) of
// file at HEAD.
func (r *Repo) Blame(ctx context.Context, file string, line int) (string, error) {
	if line < 1 {
		return "", fmt.Errorf("blame: invalid line %d", line)
	}
	l := strconv.Itoa(line)
	out, err := r.git(ctx, "", "blame", "--porcelain", "-L", l+","+l, "HEAD", "--", file)
	if err != nil {
		return "", err
	}
	first, _, _ := strings.Cut(out, "\n")
	sha, _, _ := strings.Cut(first, " ")
	if sha == "" {
		return "", fmt.Errorf("blame: no output for %s:%d", file, line)
	}
	return sha, nil
}

// DeleteBranch deletes branch locally and on the remote. Missing branches
// are ignored.
func (r *Repo) DeleteBranch(ctx context.Context, branch string) error {
	local := "refs/heads/" + branch
	if _, ok, err := r.resolve(ctx, "", local); err != nil {
		return err
	} else if ok {
		if _, err := r.git(ctx, "", "branch", "-D", "--", branch); err != nil {
			return err
		}
	}
	if !r.HasRemote() {
		return nil
	}
	out, err := r.git(ctx, "", "ls-remote", r.remote, local)
	if err != nil {
		return fmt.Errorf("%w: %v", ports.ErrOffline, err)
	}
	if hasExactRef(out, local) {
		if _, err := r.git(ctx, "", "push", "--porcelain", r.remote, ":"+local); err != nil {
			return err
		}
	}
	_, _ = r.git(ctx, "", "update-ref", "-d", r.TrackingRef(branch))
	return nil
}

// RemoteBranches implements ports.Git.
func (r *Repo) RemoteBranches(ctx context.Context, prefix string) (map[string]string, error) {
	out := map[string]string{}
	if !r.HasRemote() {
		list, err := r.git(ctx, "", "for-each-ref", "--format=%(objectname) %(refname)", "refs/heads/"+prefix)
		if err != nil {
			return nil, err
		}
		for line := range strings.SplitSeq(list, "\n") {
			if sha, ref, ok := strings.Cut(strings.TrimSpace(line), " "); ok && strings.HasPrefix(ref, "refs/heads/"+prefix) {
				out[strings.TrimPrefix(ref, "refs/heads/")] = sha
			}
		}
		return out, nil
	}
	list, err := r.git(ctx, "", "ls-remote", "--heads", r.remote, "refs/heads/"+prefix+"*")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ports.ErrOffline, err)
	}
	for line := range strings.SplitSeq(list, "\n") {
		if sha, ref, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok {
			out[strings.TrimPrefix(ref, "refs/heads/")] = sha
		}
	}
	return out, nil
}
