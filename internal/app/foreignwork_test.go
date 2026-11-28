package app

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

// Another Barnraise on the same repository works on I-50, changing a.go:
// this cluster does not start an item that changes a.go too, and starts
// one that changes something else.
func TestOtherClustersWorkHoldsItemsOnTheSameFiles(t *testing.T) {
	e := newEnv(t, withGit(), withFiles(map[string]string{"a.go": "a\n", "b.go": "b\n"}))
	other := filepath.Join(t.TempDir(), "other")
	testkit.RunGit(t, filepath.Dir(other), "clone", "-q", e.repo.Remote, other)
	testkit.RunGit(t, other, "checkout", "-qb", "br/I-50-their-work")
	testkit.WriteFile(t, other, "a.go", "theirs\n")
	testkit.RunGit(t, other, "-c", "user.email=r@x", "-c", "user.name=R", "commit", "-qam", "their work")
	testkit.RunGit(t, other, "push", "-q", "origin", "br/I-50-their-work")
	if _, err := e.arb.Claim(e.ctx, "claims/I-50", "apolo"); err != nil {
		t.Fatal(err)
	}
	same := e.create(NewItem{Title: "Also a.go", Kind: "simple", Criteria: crit(ears), Priority: "P0", Anchors: domain.Anchors{Files: []string{"a.go"}}})
	elsewhere := e.create(NewItem{Title: "Only b.go", Kind: "simple", Criteria: crit(ears), Priority: "P1", Anchors: domain.Anchors{Files: []string{"b.go"}}})
	e.svc.refreshForeignWork(e.ctx, e.p, e.clock.Now().Add(foreignEvery))
	if got := e.svc.ForeignWork("demo"); len(got) != 1 {
		t.Fatalf("foreign work %v", got)
	}
	a := e.next("implementer", "")
	if a.Item.ID != elsewhere.ID {
		t.Fatalf("started %s, want %s (%s waits for I-50)", a.Item.ID, elsewhere.ID, same.ID)
	}
	if e.item(same.ID).Status != domain.StatusReady {
		t.Fatal("the item on the same files must wait")
	}
	// ignore starts it anyway.
	e.cfg.Scheduling.OtherClusters = domain.OtherClustersIgnore
	e.svc.refreshForeignWork(e.ctx, e.p, e.clock.Now().Add(2*foreignEvery))
	if got := e.svc.ForeignWork("demo"); len(got) != 0 {
		t.Fatalf("ignore keeps no foreign work: %v", got)
	}
}

// A person's open pull request holds items on its files; one idle past
// scheduling.stale_after holds nothing and is proposed to be continued.
func TestPeoplesPullRequestsHoldAndStaleOnesAreProposed(t *testing.T) {
	e := newEnv(t, withGit(), withHost(), withFiles(map[string]string{"a.go": "a\n"}))
	now := e.clock.Now()
	e.host.PRs = []ports.OpenPR{{Number: 32, Title: "Redesign the Lodge", Author: "rafael", Head: "lodge-redesign", SHA: "abc1234", Updated: now}}
	e.host.PRFileMap = map[int][]string{32: {"a.go"}}
	same := e.create(NewItem{Title: "Also a.go", Kind: "simple", Criteria: crit(ears), Priority: "P1", Anchors: domain.Anchors{Files: []string{"a.go"}}})
	e.svc.holdHousekeeping(e.ctx, e.p)
	e.svc.refreshForeignWork(e.ctx, e.p, now.Add(foreignEvery))
	if _, err := e.svc.NextWork(e.ctx, "demo", slot("implementer")); err == nil {
		t.Fatal("started an item on the files of a person's open pull request")
	}
	// Four days without activity: it holds nothing, and is proposed once.
	later := now.Add(96 * time.Hour)
	e.clock.Advance(96 * time.Hour)
	e.svc.refreshForeignWork(e.ctx, e.p, later)
	e.svc.refreshForeignWork(e.ctx, e.p, later.Add(foreignEvery))
	n := 0
	for _, pr := range e.p.Snapshot().ProposalList() {
		if strings.Contains(pr.Evidence, "stale work #32") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d proposals to continue #32, want 1", n)
	}
	e.next("implementer", same.ID)
}
