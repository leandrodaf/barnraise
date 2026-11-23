package testkit

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	gitx "github.com/leandrodaf/barnraise/internal/adapters/out/git"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// IsolateGit keeps the developer's git configuration out of a test run.
// Call it from TestMain (it sets process environment variables).
func IsolateGit() {
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Setenv("GIT_AUTHOR_NAME", "Test")
	os.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	os.Setenv("GIT_COMMITTER_NAME", "Test")
	os.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
}

// RunGit runs git in dir and returns its trimmed output; it fails the test on error.
func RunGit(t testing.TB, dir string, args ...string) string {
	t.Helper()
	out, err := GitOutput(dir, args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return out
}

// GitOutput runs git in dir.
func GitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// WriteFile writes a file under dir, creating folders.
func WriteFile(t testing.TB, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Repo is a real git repository for tests: a clone (Root) of a bare remote
// (Remote, "" in local mode), with the git adapter and its arbiter on it.
type Repo struct {
	Root    string
	Remote  string
	Git     *gitx.Repo
	Arbiter *gitx.Arbiter
	base    string
	n       int
}

// NewRepo creates a bare remote and a clone with one commit on main holding
// files (README.md when files is empty). clock drives the arbiter's
// observations (nil = system clock).
func NewRepo(t testing.TB, clock ports.Clock, files map[string]string) *Repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()
	r := &Repo{Remote: filepath.Join(base, "remote.git"), Root: filepath.Join(base, "m1"), base: base}
	RunGit(t, base, "init", "-q", "--bare", "-b", "main", r.Remote)
	seed := filepath.Join(base, "seed")
	RunGit(t, base, "init", "-q", "-b", "main", seed)
	if len(files) == 0 {
		files = map[string]string{"README.md": "# demo\n"}
	}
	for name, content := range files {
		WriteFile(t, seed, name, content)
	}
	RunGit(t, seed, "add", "-A")
	RunGit(t, seed, "commit", "-q", "-m", "initial")
	RunGit(t, seed, "push", "-q", r.Remote, "main")
	RunGit(t, base, "clone", "-q", r.Remote, r.Root)
	r.Git = gitx.New(r.Root, "origin", "main")
	r.Arbiter = gitx.NewArbiter(r.Git, clock)
	return r
}

// NewLocalRepo creates a repository without a remote: the local disk is the arbiter.
func NewLocalRepo(t testing.TB, clock ports.Clock, files map[string]string) *Repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()
	r := &Repo{Root: filepath.Join(base, "repo"), base: base}
	RunGit(t, base, "init", "-q", "-b", "main", r.Root)
	if len(files) == 0 {
		files = map[string]string{"README.md": "# demo\n"}
	}
	for name, content := range files {
		WriteFile(t, r.Root, name, content)
	}
	RunGit(t, r.Root, "add", "-A")
	RunGit(t, r.Root, "commit", "-q", "-m", "initial")
	r.Git = gitx.New(r.Root, "", "main")
	r.Arbiter = gitx.NewArbiter(r.Git, clock)
	return r
}

// Peer is another machine: a second clone of the same remote (or the same
// directory in local mode) with its own arbiter and clock.
func (r *Repo) Peer(t testing.TB, clock ports.Clock) *Repo {
	t.Helper()
	r.n++
	if r.Remote == "" {
		g := gitx.New(r.Root, "", "main")
		return &Repo{Root: r.Root, Git: g, Arbiter: gitx.NewArbiter(g, clock), base: r.base}
	}
	dir := filepath.Join(r.base, fmt.Sprintf("peer%d", r.n))
	RunGit(t, r.base, "clone", "-q", r.Remote, dir)
	g := gitx.New(dir, "origin", "main")
	return &Repo{Root: dir, Remote: r.Remote, Git: g, Arbiter: gitx.NewArbiter(g, clock), base: r.base}
}

// PushToMain commits files straight onto the remote main from a scratch
// clone, as if another machine integrated something. It returns the new sha.
func (r *Repo) PushToMain(t testing.TB, msg string, files map[string]string) string {
	t.Helper()
	r.n++
	if r.Remote == "" {
		for name, content := range files {
			WriteFile(t, r.Root, name, content)
		}
		RunGit(t, r.Root, "add", "-A")
		RunGit(t, r.Root, "commit", "-q", "-m", msg)
		return RunGit(t, r.Root, "rev-parse", "HEAD")
	}
	dir := filepath.Join(r.base, fmt.Sprintf("scratch%d", r.n))
	RunGit(t, r.base, "clone", "-q", r.Remote, dir)
	for name, content := range files {
		WriteFile(t, dir, name, content)
	}
	RunGit(t, dir, "add", "-A")
	RunGit(t, dir, "commit", "-q", "-m", msg)
	RunGit(t, dir, "push", "-q", "origin", "main")
	return RunGit(t, dir, "rev-parse", "HEAD")
}

// RemoteMain returns main at the arbiter (the remote, or the local repo).
func (r *Repo) RemoteMain(t testing.TB) string {
	t.Helper()
	if r.Remote == "" {
		return RunGit(t, r.Root, "rev-parse", "refs/heads/main")
	}
	return RunGit(t, r.Root, "--git-dir", r.Remote, "rev-parse", "refs/heads/main")
}

// MainFiles lists the files of main at the arbiter.
func (r *Repo) MainFiles(t testing.TB) []string {
	t.Helper()
	dir := []string{"--git-dir", r.Remote}
	if r.Remote == "" {
		dir = nil
	}
	out := RunGit(t, r.Root, append(dir, "ls-tree", "-r", "--name-only", "main")...)
	return strings.Fields(out)
}

// MainLog returns the subjects and bodies of main at the arbiter.
func (r *Repo) MainLog(t testing.TB) string {
	t.Helper()
	dir := []string{"--git-dir", r.Remote}
	if r.Remote == "" {
		dir = nil
	}
	return RunGit(t, r.Root, append(dir, "log", "--format=%s%n%b", "main")...)
}

// FakeGit is a recording ports.Git without a repository. Values are
// configurable; the zero value answers with empty results and no errors.
type FakeGit struct {
	mu    sync.Mutex
	calls []string

	RootDir     string
	Main        string
	MainErr     error
	FetchErr    error
	HeadSHA     string
	HeadErr     error
	Changed     map[string][]string // "from..to" -> files; key "*" for any
	Existing    map[string]bool     // path -> exists at any revision
	Files       map[string][]byte   // path -> content for Show
	DiffText    string
	Commits     []ports.Commit
	BlameSHA    string
	BlameErr    error
	RebaseErr   error
	WorktreeErr error
	SquashSHA   string
	CommitSHA   string
}

var _ ports.Git = (*FakeGit)(nil)

func (g *FakeGit) record(format string, args ...any) {
	g.mu.Lock()
	g.calls = append(g.calls, fmt.Sprintf(format, args...))
	g.mu.Unlock()
}

// Calls returns the recorded calls, like "Rebase /dir main".
func (g *FakeGit) Calls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.calls...)
}

// Called reports whether a call starting with prefix was made.
func (g *FakeGit) Called(prefix string) bool {
	for _, c := range g.Calls() {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// Root implements ports.Git.
func (g *FakeGit) Root() string { return g.RootDir }

// Fetch implements ports.Git.
func (g *FakeGit) Fetch(context.Context) error { g.record("Fetch"); return g.FetchErr }

// MainHead implements ports.Git.
func (g *FakeGit) MainHead(context.Context) (string, error) {
	g.record("MainHead")
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.Main, g.MainErr
}

// SetMain changes the main head.
func (g *FakeGit) SetMain(sha string) {
	g.mu.Lock()
	g.Main = sha
	g.mu.Unlock()
}

// EnsureBranch implements ports.Git.
func (g *FakeGit) EnsureBranch(_ context.Context, branch, from string) error {
	g.record("EnsureBranch %s %s", branch, from)
	return nil
}

// AddWorktree implements ports.Git: it creates dir with a .git file.
func (g *FakeGit) AddWorktree(_ context.Context, dir, branch string) error {
	g.record("AddWorktree %s %s", dir, branch)
	if g.WorktreeErr != nil {
		return g.WorktreeErr
	}
	return fakeWorktree(dir)
}

// AddDetachedWorktree implements ports.Git.
func (g *FakeGit) AddDetachedWorktree(_ context.Context, dir, rev string) error {
	g.record("AddDetachedWorktree %s %s", dir, rev)
	if g.WorktreeErr != nil {
		return g.WorktreeErr
	}
	return fakeWorktree(dir)
}

func fakeWorktree(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+filepath.Join(dir, ".fakegit")+"\n"), 0o644)
}

// PushRev implements ports.Git.
func (g *FakeGit) PushRev(_ context.Context, rev, branch string) error {
	g.record("PushRev %s %s", rev, branch)
	return nil
}

// Checkout implements ports.Git.
func (g *FakeGit) Checkout(_ context.Context, dir, rev string) error {
	g.record("Checkout %s %s", dir, rev)
	return nil
}

// RemoveWorktree implements ports.Git.
func (g *FakeGit) RemoveWorktree(_ context.Context, dir string) error {
	g.record("RemoveWorktree %s", dir)
	return os.RemoveAll(dir)
}

// CommitAll implements ports.Git.
func (g *FakeGit) CommitAll(_ context.Context, dir, message string) (string, bool, error) {
	g.record("CommitAll %s %s", dir, firstLine(message))
	return g.CommitSHA, g.CommitSHA != "", nil
}

// Push implements ports.Git.
func (g *FakeGit) Push(_ context.Context, dir, branch string) error {
	g.record("Push %s %s", dir, branch)
	return nil
}

// FetchBranch implements ports.Git.
func (g *FakeGit) FetchBranch(_ context.Context, branch string) (bool, error) {
	g.record("FetchBranch %s", branch)
	return true, nil
}

// Rebase implements ports.Git.
func (g *FakeGit) Rebase(_ context.Context, dir, onto string) error {
	g.record("Rebase %s %s", dir, onto)
	return g.RebaseErr
}

// Head implements ports.Git.
func (g *FakeGit) Head(_ context.Context, dir string) (string, error) {
	g.record("Head %s", dir)
	return g.HeadSHA, g.HeadErr
}

// ChangedFiles implements ports.Git.
func (g *FakeGit) ChangedFiles(_ context.Context, from, to string) ([]string, error) {
	g.record("ChangedFiles %s %s", from, to)
	g.mu.Lock()
	defer g.mu.Unlock()
	if f, ok := g.Changed[from+".."+to]; ok {
		return f, nil
	}
	return g.Changed["*"], nil
}

// Diff implements ports.Git.
func (g *FakeGit) Diff(_ context.Context, from, to string, _ int) (string, error) {
	g.record("Diff %s %s", from, to)
	return g.DiffText, nil
}

// ExistsAt implements ports.Git.
func (g *FakeGit) ExistsAt(_ context.Context, rev, path string) bool {
	g.record("ExistsAt %s %s", rev, path)
	return g.Existing[path]
}

// Show implements ports.Git.
func (g *FakeGit) Show(_ context.Context, rev, path string) ([]byte, error) {
	g.record("Show %s %s", rev, path)
	if b, ok := g.Files[path]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("path %s does not exist at %s", path, rev)
}

// SquashOnto implements ports.Git.
func (g *FakeGit) SquashOnto(_ context.Context, dir, base, message string, _ ...string) (string, error) {
	g.record("SquashOnto %s %s %s", dir, base, firstLine(message))
	return g.SquashSHA, nil
}

// MergeBase implements ports.Git.
func (g *FakeGit) MergeBase(_ context.Context, a, b string) (string, error) {
	g.record("MergeBase %s %s", a, b)
	return a, nil
}

// Log implements ports.Git.
func (g *FakeGit) Log(_ context.Context, args ...string) ([]ports.Commit, error) {
	g.record("Log %s", strings.Join(args, " "))
	return g.Commits, nil
}

// Blame implements ports.Git.
func (g *FakeGit) Blame(_ context.Context, file string, line int) (string, error) {
	g.record("Blame %s %d", file, line)
	return g.BlameSHA, g.BlameErr
}

// DeleteBranch implements ports.Git.
func (g *FakeGit) DeleteBranch(_ context.Context, branch string) error {
	g.record("DeleteBranch %s", branch)
	return nil
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}
