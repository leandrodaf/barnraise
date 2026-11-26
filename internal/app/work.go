package app

import (
	"context"
	"fmt"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// maxWorkCommits bounds the commits ItemWork lists: an item is a few steps,
// not a history.
const maxWorkCommits = 50

// WorkCommit is one commit of an item's work.
type WorkCommit struct {
	SHA     string    `json:"sha"`
	Subject string    `json:"subject"`
	Author  string    `json:"author,omitempty"`
	At      time.Time `json:"at,omitzero"`
	Files   int       `json:"files"`
	// URL opens the commit on the code host; "" without one.
	URL string `json:"url,omitempty"`
	// Step is the step whose delivery this commit records, "" for a commit
	// the agent made along the way.
	Step string `json:"step,omitempty"`
}

// WorkStep is one delivery of the item and the commit that holds it.
type WorkStep struct {
	Step   string    `json:"step"`
	Agent  string    `json:"agent"`
	Status string    `json:"status"`
	Commit string    `json:"commit,omitempty"`
	URL    string    `json:"url,omitempty"`
	At     time.Time `json:"at,omitzero"`
}

// ItemWorkView is the proof of an item's work: the commits on its branch,
// the commit each delivery was pushed as, the pull request they are
// reviewed in and, once integrated, the commit on the main branch.
type ItemWorkView struct {
	Branch    string       `json:"branch,omitempty"`
	BranchURL string       `json:"branch_url,omitempty"`
	Compare   string       `json:"compare,omitempty"`
	PR        *PRView      `json:"pr,omitempty"`
	Commits   []WorkCommit `json:"commits"`
	// More is true when the branch has more commits than Commits lists.
	More   bool       `json:"more,omitempty"`
	Steps  []WorkStep `json:"steps"`
	Merged string     `json:"merged,omitempty"`
	// MergedURL opens Merged on the code host.
	MergedURL string `json:"merged_url,omitempty"`
	// Warning says why the commits could not be read (the branch is on
	// another machine and the remote did not answer).
	Warning string `json:"warning,omitempty"`
}

// ItemWork returns the proof of an item's work. The commits are those of
// its last delivered commit that the main branch does not have: the step
// commits Barnraise pushes and any the agent made in between. A commit made
// on another machine is fetched first.
func (s *Service) ItemWork(ctx context.Context, project string, a Actor, id string) (*ItemWorkView, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeItemsRead, p.Name); err != nil {
		return nil, err
	}
	nid, err := domain.NormalizeItemID(id)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	it := p.Snapshot().Items[nid]
	if it == nil {
		return nil, fmt.Errorf("%w: item %s", ErrNotFound, nid)
	}
	out := &ItemWorkView{Branch: it.Branch, Commits: []WorkCommit{}, Steps: []WorkStep{}, Merged: it.MergeCommit}
	web := p.Ports().Web
	link := func(sha string) string {
		if web == nil || sha == "" {
			return ""
		}
		return web.Commit(sha)
	}
	if it.PR != "" {
		pr := prFromRef(it.PR)
		out.PR = &PRView{Number: pr.Number, URL: pr.URL, State: prState(p, it)}
	}
	out.MergedURL = link(it.MergeCommit)
	if web != nil && it.Branch != "" && it.MergeCommit == "" {
		// A branch is deleted once its item lands: the merged commit says it then.
		out.BranchURL = web.Branch(it.Branch)
		out.Compare = web.Compare(p.Cfg().MainBranch, it.Branch)
	}

	evs, err := s.d.Store.Query(ctx, ports.EventQuery{Project: p.Name, Subject: nid, Types: []string{domain.EvDeliverySubmitted}})
	if err != nil {
		return nil, err
	}
	stepOf := map[string]string{}
	head := ""
	for _, e := range evs {
		pl, err := e.DecodePayload()
		if err != nil || pl.Delivery == nil {
			continue
		}
		d := pl.Delivery
		out.Steps = append(out.Steps, WorkStep{Step: d.Step, Agent: d.Agent, Status: string(d.Status), Commit: d.Commit, URL: link(d.Commit), At: d.At})
		if d.Commit != "" {
			stepOf[d.Commit] = d.Step
			head = d.Commit
		}
	}
	if head == "" || p.Ports().Git == nil {
		return out, nil
	}
	commits, err := s.workCommits(ctx, p, it.Branch, head)
	if err != nil {
		out.Warning = err.Error()
		return out, nil
	}
	if len(commits) > maxWorkCommits {
		commits, out.More = commits[:maxWorkCommits], true
	}
	for _, c := range commits {
		out.Commits = append(out.Commits, WorkCommit{SHA: c.SHA, Subject: c.Subject, Author: c.Author, At: c.Time, Files: len(c.Files), URL: link(c.SHA), Step: stepOf[c.SHA]})
	}
	return out, nil
}

// workCommits lists the commits of head that the main branch does not have,
// newest first, fetching the item's branch when head is not here yet.
func (s *Service) workCommits(ctx context.Context, p *Project, branch, head string) ([]ports.Commit, error) {
	g := p.Ports().Git
	// The main branch moves under an item (another machine integrates, a
	// person pushes): read it fresh, or its new commits, which the rebased
	// branch carries, would show as the item's work. Offline, the last fetch
	// still answers.
	fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	_ = g.Fetch(fctx)
	cancel()
	main, err := g.MainHead(ctx)
	if err != nil {
		return nil, fmt.Errorf("the main branch cannot be read: %v", err)
	}
	args := []string{head, "--not", main, "-n", fmt.Sprint(maxWorkCommits + 1)}
	commits, err := g.Log(ctx, args...)
	if err == nil {
		return commits, nil
	}
	if branch == "" {
		return nil, fmt.Errorf("commit %s is not on this machine", short(head))
	}
	if _, ferr := g.FetchBranch(ctx, branch); ferr != nil {
		return nil, fmt.Errorf("commit %s is not on this machine and branch %s could not be fetched: %v", short(head), branch, ferr)
	}
	return g.Log(ctx, args...)
}
