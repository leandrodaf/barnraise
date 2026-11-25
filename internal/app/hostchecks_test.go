package app

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

// ciRepo is a repository that runs CI on its code host.
var ciRepo = withFiles(map[string]string{".github/workflows/ci.yml": "on: pull_request\n"})

// hostCI scripts what the host's CI says, and remembers the commits asked.
type hostCI struct {
	mu    sync.Mutex
	say   ports.HostChecks
	asked []string
}

func (c *hostCI) set(v ports.HostChecks) { c.mu.Lock(); c.say = v; c.mu.Unlock() }
func (c *hostCI) checks(sha string) (ports.HostChecks, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked = append(c.asked, sha)
	return c.say, nil
}
func (c *hostCI) commits() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.asked...)
}

// The commit that would land goes to the pull request, without the item's
// memory files, and main moves only after the repository's CI passed on it.
func TestMergeQueueWaitsForTheHostsCI(t *testing.T) {
	e := newEnv(t, withGit(), withHost(), ciRepo)
	ci := &hostCI{say: ports.HostChecks{Total: 2, Pending: 1}}
	e.host.HostChecks = ci.checks
	it := e.toQueue("Cards screen", map[string]string{"app/cards.txt": "cards\n", "plan.md": "# plan\n"})
	if it.PR == "" {
		t.Fatal("the item has no pull request")
	}
	main := e.repo.RemoteMain(t)
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	e.wantStatus(it.ID, domain.StatusQueued, "integrate")
	if e.repo.RemoteMain(t) != main {
		t.Fatal("main moved while the CI ran")
	}
	asked := ci.commits()
	if len(asked) != 1 {
		t.Fatalf("asked %v", asked)
	}
	// What people and the CI see on the pull request: the change on top of
	// main, in one commit, without plan.md.
	files := testkit.RunGit(t, e.repo.Root, "--git-dir", e.repo.Remote, "diff", "--name-only", main, it.Branch)
	if !strings.Contains(files, "app/cards.txt") || strings.Contains(files, "plan.md") {
		t.Fatalf("files on the pull request: %q", files)
	}
	if got := testkit.RunGit(t, e.repo.Root, "--git-dir", e.repo.Remote, "rev-parse", it.Branch); got != asked[0] {
		t.Fatalf("the pull request is at %s, the CI was asked about %s", got, asked[0])
	}
	if e.host.Count("MarkReady") == 0 {
		t.Error("the pull request stayed a draft")
	}

	// Still running: the same commit waits, nothing is built or pushed again.
	e.clock.Advance(time.Minute)
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	if got := ci.commits(); len(got) != 2 || got[1] != asked[0] {
		t.Fatalf("asked %v", got)
	}

	ci.set(ports.HostChecks{Total: 2})
	e.clock.Advance(time.Minute)
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	got := e.wantStatus(it.ID, domain.StatusIntegrated, "integrate")
	if got.MergeCommit != asked[0] || e.repo.RemoteMain(t) != asked[0] {
		t.Fatalf("landed %s, main %s, checked %s", got.MergeCommit, e.repo.RemoteMain(t), asked[0])
	}
}

// A check of the repository that fails sends the item back, with its name.
func TestMergeQueueHostCIFailureIsRework(t *testing.T) {
	e := newEnv(t, withGit(), withHost(), ciRepo)
	ci := &hostCI{say: ports.HostChecks{Total: 2, Pending: 1, Failed: []string{"unit tests"}}}
	e.host.HostChecks = ci.checks
	it := e.toQueue("Cards screen", map[string]string{"app/cards.txt": "cards\n"})
	main := e.repo.RemoteMain(t)
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	got := e.item(it.ID)
	if got.Status == domain.StatusQueued || len(got.Rework) == 0 || !strings.Contains(got.Rework[len(got.Rework)-1].Reason, "unit tests") {
		t.Fatalf("status %s rework %+v", got.Status, got.Rework)
	}
	if e.repo.RemoteMain(t) != main {
		t.Fatal("main moved on a failed CI")
	}
}

// Main moved while the CI ran: the commit is built again on the new main
// and the CI is asked about the new one.
func TestMergeQueueHostCIStartsOverWhenMainMoves(t *testing.T) {
	e := newEnv(t, withGit(), withHost(), ciRepo)
	ci := &hostCI{say: ports.HostChecks{Total: 1, Pending: 1}}
	e.host.HostChecks = ci.checks
	it := e.toQueue("Cards screen", map[string]string{"app/cards.txt": "cards\n"})
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	e.repo.PushToMain(t, "chore: someone else landed", map[string]string{"other.txt": "o\n"})
	ci.set(ports.HostChecks{Total: 1})
	e.clock.Advance(time.Minute)
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	asked := ci.commits()
	if len(asked) != 2 || asked[0] == asked[1] {
		t.Fatalf("asked %v", asked)
	}
	if got := e.wantStatus(it.ID, domain.StatusIntegrated, "integrate"); got.MergeCommit != asked[1] {
		t.Fatalf("landed %s, checked %s", got.MergeCommit, asked[1])
	}
	if !contains(e.repo.MainFiles(t), "other.txt") || !contains(e.repo.MainFiles(t), "app/cards.txt") {
		t.Fatalf("main %v", e.repo.MainFiles(t))
	}
}

// Without a CI configuration, or with merge.host_checks: ignore, the queue
// lands the item at once, as before.
func TestMergeQueueWithoutHostCI(t *testing.T) {
	for name, opts := range map[string][]opt{
		"no ci files": {withGit(), withHost()},
		"ignore":      {withGit(), withHost(), ciRepo, withCfg(func(c *domain.ProjectConfig) { c.Merge.HostChecks = "ignore" })},
	} {
		e := newEnv(t, opts...)
		e.host.HostChecks = func(string) (ports.HostChecks, error) {
			t.Errorf("%s: the host was asked", name)
			return ports.HostChecks{}, nil
		}
		it := e.toQueue("Cards screen", map[string]string{"app/cards.txt": "cards\n"})
		if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 1 || err != nil {
			t.Fatal(name, n, err)
		}
		e.wantStatus(it.ID, domain.StatusIntegrated, "integrate")
	}
}

// A repository with CI files whose host shows no check for the commit: the
// item lands after the grace period.
func TestMergeQueueHostCIGrace(t *testing.T) {
	e := newEnv(t, withGit(), withHost(), ciRepo)
	it := e.toQueue("Cards screen", map[string]string{"app/cards.txt": "cards\n"})
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	e.clock.Advance(hostChecksGrace)
	if n, err := e.svc.ProcessQueue(e.ctx, "demo"); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	e.wantStatus(it.ID, domain.StatusIntegrated, "integrate")
}
