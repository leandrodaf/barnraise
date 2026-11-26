package app

import (
	"errors"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// The proof of an item's work: each delivery is a pushed commit, listed
// with the step it delivers, newest first, and only what main lacks.
func TestItemWorkListsTheCommitsOfEachDelivery(t *testing.T) {
	e := newEnv(t, withGit(), withFiles(map[string]string{"wallet/balance.go": "package wallet\n"}))
	it := e.ready("reviewed", "Balance in cents", "wallet")
	e.next("implementer", it.ID)

	// Before any delivery: nothing to prove yet, and no error.
	w, err := e.svc.ItemWork(e.ctx, "demo", reader(), it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Commits) != 0 || len(w.Steps) != 0 || w.Warning != "" || w.Branch == "" {
		t.Fatalf("before delivery: %+v", w)
	}

	// The implementation, sent back once by the review, then fixed.
	e.workspace(it.ID, map[string]string{"wallet/balance.go": "package wallet\n\nvar B = 1.5\n"})
	e.mustDeliver(it.ID, domain.Delivery{})
	if _, err := e.review(it.ID, reworkReview("floats instead of integer cents")); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Minute)
	e.workspace(it.ID, map[string]string{"wallet/balance.go": "package wallet\n\nvar B = 150\n", "wallet/cents.go": "package wallet\n"})
	e.mustDeliver(it.ID, domain.Delivery{})

	w, err = e.svc.ItemWork(e.ctx, "demo", reader(), it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Steps) != 2 || w.Steps[0].Step != "implement" || w.Steps[0].Commit == "" || w.Steps[1].Commit == "" || w.Steps[0].Commit == w.Steps[1].Commit {
		t.Fatalf("steps: %+v", w.Steps)
	}
	if len(w.Commits) != 2 || w.More || w.Warning != "" {
		t.Fatalf("commits: %+v (more %v, warning %q)", w.Commits, w.More, w.Warning)
	}
	// Newest first: the fix, with its two files, then the first attempt.
	if w.Commits[0].SHA != w.Steps[1].Commit || w.Commits[0].Files != 2 || w.Commits[1].SHA != w.Steps[0].Commit || w.Commits[1].Files != 1 {
		t.Fatalf("order or files: %+v", w.Commits)
	}
	for _, c := range w.Commits {
		if c.Step != "implement" || c.Subject == "" || c.At.IsZero() {
			t.Fatalf("commit %+v", c)
		}
	}

	if _, err := e.svc.ItemWork(e.ctx, "demo", reader(), "I-999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing item: %v", err)
	}
	if _, err := e.svc.ItemWork(e.ctx, "demo", reader(), "nope"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad id: %v", err)
	}
	if _, err := e.svc.ItemWork(e.ctx, "demo", Actor{Kind: ActorToken, Name: "none"}, it.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("no scope: %v", err)
	}
}

// Without git (a project the daemon only tracks), the deliveries still say
// what was delivered.
func TestItemWorkWithoutCommits(t *testing.T) {
	e := newEnv(t)
	it := e.ready("simple", "Docs", "x")
	e.start(it.ID)
	e.mustDeliver(it.ID, domain.Delivery{})
	w, err := e.svc.ItemWork(e.ctx, "demo", owner(), it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Steps) != 1 || w.Steps[0].Step != "implement" || len(w.Commits) != 0 || w.Warning != "" {
		t.Fatalf("%+v", w)
	}
}
