package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Two attempts only for risky items: two worktrees in parallel; the checks
// (and then the reviewers) choose. Never by default, because it doubles usage.

// dualEligible reports whether a step of an item runs as two attempts.
func dualEligible(it *domain.Item, st domain.StepDef) bool {
	return it.Risky && st.Type == domain.StepAgent && !st.Review && st.Output == "" && st.Name != RefineStep
}

func (s *Service) hasAlt(p *Project, id, step string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	it := p.state.Items[id]
	return it != nil && it.Alt != nil && it.Alt.Step == step
}

func altKey(id string) string { return id + "~b" }

// altDir is the second attempt's worktree.
func (s *Service) altDir(project, id string) string { return s.WorktreeDir(project, altKey(id)) }

// prepareAlt records the second attempt and creates its branch and worktree
// from the item's branch.
func (s *Service) prepareAlt(ctx context.Context, p *Project, id string, st domain.StepDef) (string, error) {
	g := p.Ports().Git
	if g == nil {
		return "", errors.New("two attempts need a repository")
	}
	dirA := s.WorktreeDir(p.Name, id)
	headA, err := g.Head(ctx, dirA)
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	cur := p.state.Items[id]
	if cur == nil {
		p.mu.Unlock()
		return "", fmt.Errorf("%w: item %s", ErrNotFound, id)
	}
	branchB := cur.Branch + "-b"
	if cur.Alt == nil || cur.Alt.Step != st.Name {
		if _, err := s.mutateItemLocked(ctx, p, id, domain.EvItemUpdated, "barnraise", func(x *domain.Item) error {
			x.Alt = &domain.AltAttempt{Step: st.Name, Branch: branchB}
			return nil
		}, domain.Payload{Note: "risky item: two attempts in parallel at step " + st.Name}); err != nil {
			p.mu.Unlock()
			return "", err
		}
	}
	p.mu.Unlock()
	dirB := s.altDir(p.Name, id)
	if _, err := os.Stat(filepath.Join(dirB, ".git")); err == nil {
		return dirB, nil
	}
	if err := g.EnsureBranch(ctx, branchB, headA); err != nil {
		return "", err
	}
	if err := g.AddWorktree(ctx, dirB, branchB); err != nil {
		return "", err
	}
	s.excludeFromGit(dirB, "/.barnraise-session/")
	s.excludeFromGit(dirB, "/criteria.json")
	for _, f := range p.Cfg().EnvFiles {
		if data, err := os.ReadFile(filepath.Join(p.Cfg().Root, f)); err == nil {
			_ = os.WriteFile(filepath.Join(dirB, f), data, 0o600)
		}
	}
	return dirB, nil
}

// deliverAttempt holds a delivery until both attempts ended, then chooses.
func (s *Service) deliverAttempt(ctx context.Context, p *Project, a Actor, d domain.Delivery) (*domain.Item, error) {
	p.mu.Lock()
	branchB := ""
	branchA := ""
	if it := p.state.Items[d.ItemID]; it != nil && it.Alt != nil {
		branchB, branchA = it.Alt.Branch, it.Branch
	}
	p.mu.Unlock()
	b := a.Attempt == "b"
	dir, branch := s.WorktreeDir(p.Name, d.ItemID), branchA
	if b {
		dir, branch = s.altDir(p.Name, d.ItemID), branchB
	}
	sha, err := s.commitIn(ctx, p, d.ItemID, dir, branch, fmt.Sprintf("%s: attempt %s, %s step %s", d.ItemID, map[bool]string{true: "b", false: "a"}[b], strings.ToLower(string(d.Status)), d.Step))
	if err != nil && strings.Contains(err.Error(), "secret") {
		return nil, err
	}
	d.Commit = sha
	if _, err := s.emit(ctx, p, domain.EvDeliverySubmitted, d.ItemID, d.Agent, domain.Payload{Delivery: &d, Note: "attempt " + map[bool]string{true: "b", false: "a"}[b]}); err != nil {
		return nil, err
	}
	dd := d
	it, err := s.mutateItem(ctx, p, d.ItemID, domain.EvItemUpdated, d.Agent, func(x *domain.Item) error {
		if x.Alt == nil {
			return fmt.Errorf("%w: the second attempt is over", ErrConflict)
		}
		if b {
			x.Alt.B, x.Alt.BEnded = &dd, true
		} else {
			x.Alt.A = &dd
		}
		return nil
	}, domain.Payload{})
	if err != nil {
		return nil, err
	}
	if it.Alt.A != nil && it.Alt.BEnded {
		return s.resolveDual(ctx, p, d.ItemID)
	}
	return it, nil
}

// altEnded is called when the second attempt's session ends.
func (s *Service) altEnded(ctx context.Context, p *Project, id string) {
	it, err := s.mutateItem(ctx, p, id, domain.EvItemUpdated, "barnraise", func(x *domain.Item) error {
		if x.Alt == nil {
			return ErrConflict
		}
		x.Alt.BEnded = true
		return nil
	}, domain.Payload{})
	if err == nil && it.Alt.A != nil {
		if _, err := s.resolveDual(ctx, p, id); err != nil {
			s.d.Log.Warn("resolve attempts", "item", id, "err", err)
		}
	}
}

type attemptScore struct {
	name     string
	d        *domain.Delivery
	dir      string
	branch   string
	passed   int
	files    int
	head     string
	usable   bool
	failures []string
}

// resolveDual runs the local checks on both attempts and keeps the better one:
// more checks green, then the smaller change.
func (s *Service) resolveDual(ctx context.Context, p *Project, id string) (*domain.Item, error) {
	p.mu.Lock()
	cur := p.state.Items[id]
	if cur == nil || cur.Alt == nil {
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: no attempts to choose", ErrConflict)
	}
	it := cur.Clone()
	p.mu.Unlock()
	g := p.Ports().Git
	main, _ := g.MainHead(ctx)
	cands := []*attemptScore{
		{name: "a", d: it.Alt.A, dir: s.WorktreeDir(p.Name, id), branch: it.Branch},
		{name: "b", d: it.Alt.B, dir: s.altDir(p.Name, id), branch: it.Alt.Branch},
	}
	var best *attemptScore
	for _, c := range cands {
		if c.d == nil {
			continue
		}
		oc, _ := c.d.Status.Outcome()
		c.usable = oc.Positive()
		if !c.usable {
			continue
		}
		c.head, _ = g.Head(ctx, c.dir)
		if files, err := s.itemFiles(ctx, p, main, c.head); err == nil {
			c.files = len(files)
		}
		if s.d.Checks != nil {
			for _, name := range sortedKeys(p.Cfg().Checks) {
				def := p.Cfg().Checks[name]
				if def.RunsOn != "local" {
					continue
				}
				def.Scope, def.FlakyRetries = "all", 1
				res, pending, err := s.d.Checks.Run(ctx, ports.CheckSpec{Def: def, ItemID: id, Dir: c.dir, Commit: c.head})
				if err == nil && !pending && res.Passed {
					c.passed++
				} else if err == nil && !pending {
					c.failures = append(c.failures, name)
				}
			}
		}
		if best == nil || c.passed > best.passed || (c.passed == best.passed && c.files < best.files) {
			best = c
		}
	}
	reason := "neither attempt delivered a usable result"
	if best != nil {
		reason = fmt.Sprintf("attempt %s chosen: %d checks green, %d files changed", best.name, best.passed, best.files)
		for _, c := range cands {
			if c != best && c.usable {
				reason += fmt.Sprintf("; attempt %s: %d green, %d files", c.name, c.passed, c.files)
			}
		}
	}
	if best != nil && best.name == "b" && best.head != "" {
		if err := g.ResetBranch(ctx, cands[0].dir, it.Branch, best.head); err != nil {
			return nil, fmt.Errorf("adopt attempt b: %w", err)
		}
		if err := g.Push(ctx, cands[0].dir, it.Branch); err != nil {
			s.d.Log.Warn("push adopted attempt", "item", id, "err", err)
		}
	}
	_ = g.RemoveWorktree(ctx, cands[1].dir)
	_ = g.DeleteBranch(ctx, it.Alt.Branch)
	winner := it.Alt.A
	if best != nil {
		winner = best.d
	} else if winner == nil {
		winner = it.Alt.B
	}
	if _, err := s.mutateItem(ctx, p, id, domain.EvItemUpdated, "barnraise", func(x *domain.Item) error {
		x.Alt = nil
		return nil
	}, domain.Payload{Note: reason}); err != nil {
		return nil, err
	}
	if winner == nil {
		out, _, err := s.finishStep(ctx, p, stepOutcome{Item: id, Step: it.Step, Outcome: domain.OutcomeBlocked, Reason: reason, Actor: "barnraise"})
		return out, err
	}
	outcome, _ := winner.Status.Outcome()
	r := winner.Reason
	if outcome == domain.OutcomeConcerns {
		r = strings.Join(winner.Concerns, "; ")
	}
	out, _, err := s.finishStep(ctx, p, stepOutcome{Item: id, Step: it.Step, Outcome: outcome, Reason: r, Actor: winner.Agent,
		Criteria: winner.Criteria, Files: winner.Files, Payload: domain.Payload{Note: reason}})
	return out, err
}
