package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// The code host's own CI (ADR-0030 keeps Barnraise's checks; this is the
// repository's). People who work on the repository without Barnraise rely
// on it, and so does a protected main: the merge queue puts the commit
// that would land on the item's pull request, waits for every check the
// host runs on it and only then moves main. A failed check sends the item
// back as rework, with the names of the checks.

// errHostWait says the host's checks of a queued item still run.
var errHostWait = errors.New("waiting for the code host's checks")

const (
	// hostChecksGrace is how long a commit may show no check at all before
	// the repository is taken to run none on pull requests.
	hostChecksGrace = 2 * time.Minute
	// hostChecksEvery spaces the questions to the host about one commit.
	hostChecksEvery = 20 * time.Second
	// hostChecksLong is when the owner is told a commit still waits.
	hostChecksLong = 30 * time.Minute
)

// ciFiles are where a repository says it runs CI on the code host.
var ciFiles = []string{".github/workflows", ".gitlab-ci.yml"}

// hostWait is one commit on a pull request, waiting for the host's checks.
type hostWait struct {
	main   string    // the main the commit was built on
	orig   string    // the head of the item's worktree before the queue built it
	sha    string    // the commit on the pull request
	since  time.Time // when it was pushed
	asked  time.Time // when the host was last asked
	told   bool      // the item says it waits
	warned bool      // the owner was told it takes long
	denied bool      // the owner was told the host refuses the checks
}

// hostChecksApply reports whether the merge queue waits for the code
// host's checks in this project: there is a code host, merge.host_checks
// is not "ignore" and main has a CI configuration.
func (s *Service) hostChecksApply(ctx context.Context, p *Project) bool {
	g := p.Ports().Git
	if p.Ports().CodeHost == nil || g == nil || p.Cfg().Merge.HostChecks == "ignore" {
		return false
	}
	main, err := g.MainHead(ctx)
	if err != nil || main == "" {
		return false
	}
	for _, f := range ciFiles {
		if g.ExistsAt(ctx, main, f) && !emptyCIFile(ctx, g, main, f) {
			return true
		}
	}
	return false
}

// emptyCIFile reports a CI path that cannot run anything: .github/workflows
// as a file instead of a folder (GitHub runs no workflow from it), or an
// empty .gitlab-ci.yml. Waiting on such a repository's checks never ends.
func emptyCIFile(ctx context.Context, g ports.Git, rev, path string) bool {
	b, err := g.Show(ctx, rev, path)
	if err != nil {
		return false // not a file: a folder of workflows
	}
	return path == ".github/workflows" || strings.TrimSpace(string(b)) == ""
}

// hostWaiting returns the commit of the item that waits on its pull
// request, when it still is what would land: main did not move and the
// item's worktree is as the queue left it. Otherwise it is forgotten and
// the queue builds the commit again.
func (s *Service) hostWaiting(ctx context.Context, p *Project, it *domain.Item, main string) *hostWait {
	p.mu.Lock()
	w := p.hostWait[it.ID]
	p.mu.Unlock()
	if w == nil {
		return nil
	}
	head, err := p.Ports().Git.Head(ctx, s.WorktreeDir(p.Name, it.ID))
	if err != nil || w.main != main || head != w.orig {
		s.forgetHostWait(p, []*domain.Item{it})
		return nil
	}
	return w
}

func (s *Service) forgetHostWait(p *Project, items []*domain.Item) {
	p.mu.Lock()
	for _, it := range items {
		delete(p.hostWait, it.ID)
	}
	p.mu.Unlock()
}

// hostVerdict asks the host about the waiting commit. pass: every check
// ended well (or the repository ran none). Otherwise err is errHostWait
// while checks run, or nil with the reason the item goes back.
func (s *Service) hostVerdict(ctx context.Context, p *Project, it *domain.Item, w *hostWait) (pass bool, why string, err error) {
	now := s.d.Clock.Now()
	if !w.asked.IsZero() && now.Sub(w.asked) < hostChecksEvery {
		return false, "", errHostWait
	}
	w.asked = now
	c, cerr := p.Ports().CodeHost.Checks(ctx, w.sha)
	switch {
	case errors.Is(cerr, ports.ErrChecksDenied):
		s.d.Log.Warn("the code host's checks", "item", it.ID, "commit", short(w.sha), "err", cerr)
		s.queueProblem(p, "the code host refuses this machine's token the checks ("+it.ID+"): give it read access to Checks and Commit statuses")
		if !w.denied {
			// Waiting cannot end: say so at once, on the item and to the owner.
			w.denied = true
			_, _ = s.mutateItem(ctx, p, it.ID, domain.EvItemUpdated, "merge-queue", func(*domain.Item) error { return nil },
				domain.Payload{Note: "the code host refuses this machine's token the checks of " + short(w.sha) + ": it cannot land until the token can read them"})
			go s.notifyOwner(context.WithoutCancel(ctx), p, "ci_denied", it.ID+" cannot land: "+s.d.Machine+"'s token cannot read the checks",
				"The code host answers "+s.d.Machine+" that its token may not read the checks of "+short(w.sha)+" ("+cerr.Error()+"). "+
					"Give the token read access to Checks and Commit statuses (a fine-grained GitHub token: Repository permissions), "+
					"or set `merge.host_checks: ignore` in barnraise.yaml to land items on Barnraise's own checks alone.")
		}
	case cerr != nil:
		s.d.Log.Warn("the code host's checks", "item", it.ID, "commit", short(w.sha), "err", cerr)
		s.queueProblem(p, "reading the checks of "+it.ID+" fails: "+cerr.Error())
	case len(c.Failed) > 0:
		s.forgetHostWait(p, []*domain.Item{it})
		return false, "the repository's CI failed on the pull request (" + short(w.sha) + "): " + strings.Join(c.Failed, ", "), nil
	case c.Pending == 0 && (c.Total > 0 || now.Sub(w.since) >= hostChecksGrace):
		return true, "", nil
	}
	if !w.told {
		w.told = true
		_, _ = s.mutateItem(ctx, p, it.ID, domain.EvItemUpdated, "merge-queue", func(*domain.Item) error { return nil },
			domain.Payload{Note: "on its pull request as " + short(w.sha) + ", waiting for the repository's CI"})
	}
	if !w.warned && now.Sub(w.since) >= hostChecksLong {
		w.warned = true
		go s.notifyOwner(context.WithoutCancel(ctx), p, "ci_waits", it.ID+" waits for the repository's CI",
			"The commit "+short(w.sha)+" of "+it.ID+" has been on its pull request for "+hostChecksLong.String()+" and its checks have not ended. The item lands when they pass; `merge.host_checks: ignore` in barnraise.yaml lands items on Barnraise's own checks alone.")
	}
	return false, "", errHostWait
}
