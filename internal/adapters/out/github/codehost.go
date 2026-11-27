package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/leandrodaf/barnraise/internal/adapters/out/httpx"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// CodeHost drives pull requests, reviews, statuses and Actions on GitHub.
type CodeHost struct{ c *Client }

var _ ports.CodeHost = (*CodeHost)(nil)

// Name is "github".
func (h *CodeHost) Name() string { return "github" }

type ghPull struct {
	Number   int     `json:"number"`
	NodeID   string  `json:"node_id"`
	HTMLURL  string  `json:"html_url"`
	State    string  `json:"state"`
	Draft    bool    `json:"draft"`
	MergedAt *string `json:"merged_at"`
	Head     struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

func (p ghPull) toPR() ports.PR {
	state := p.State
	if p.MergedAt != nil && *p.MergedAt != "" {
		state = "merged"
	}
	return ports.PR{Number: p.Number, URL: p.HTMLURL, State: state, Draft: p.Draft, Head: p.Head.Ref}
}

func (h *CodeHost) findOpen(ctx context.Context, branch string) (*ghPull, error) {
	q := url.Values{"head": {h.c.owner + ":" + branch}, "state": {"open"}, "per_page": {"10"}}
	var pulls []ghPull
	if _, err := h.c.do(ctx, http.MethodGet, h.c.repoPath("/pulls")+"?"+q.Encode(), nil, &pulls); err != nil {
		return nil, err
	}
	for i := range pulls {
		if pulls[i].Head.Ref == branch {
			return &pulls[i], nil
		}
	}
	return nil, nil
}

// OpenDraftPR opens a draft PR from branch into base, or returns the open PR
// that already has that head. Repositories without draft support get a
// regular PR.
func (h *CodeHost) OpenDraftPR(ctx context.Context, branch, base, title, body string) (ports.PR, error) {
	if p, err := h.findOpen(ctx, branch); err != nil {
		return ports.PR{}, err
	} else if p != nil {
		return p.toPR(), nil
	}
	req := map[string]any{"title": title, "head": branch, "base": base, "body": body, "draft": true}
	var created ghPull
	_, err := h.c.do(ctx, http.MethodPost, h.c.repoPath("/pulls"), req, &created)
	if httpx.IsStatus(err, http.StatusUnprocessableEntity) {
		msg := strings.ToLower(err.Error())
		switch {
		case strings.Contains(msg, "already exists"):
			if p, ferr := h.findOpen(ctx, branch); ferr == nil && p != nil {
				return p.toPR(), nil
			}
		case strings.Contains(msg, "draft"):
			req["draft"] = false
			_, err = h.c.do(ctx, http.MethodPost, h.c.repoPath("/pulls"), req, &created)
		}
	}
	if err != nil {
		return ports.PR{}, err
	}
	return created.toPR(), nil
}

// UpdatePR changes title and body; empty values are left unchanged.
func (h *CodeHost) UpdatePR(ctx context.Context, pr ports.PR, title, body string) error {
	m := map[string]any{}
	if title != "" {
		m["title"] = title
	}
	if body != "" {
		m["body"] = body
	}
	if len(m) == 0 {
		return nil
	}
	_, err := h.c.do(ctx, http.MethodPatch, h.c.repoPath("/pulls/%d", pr.Number), m, nil)
	return err
}

const readyMutation = `mutation($id: ID!) {
  markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { isDraft } }
}`

// MarkReady takes the PR out of draft (GraphQL; REST has no endpoint for it).
func (h *CodeHost) MarkReady(ctx context.Context, pr ports.PR) error {
	var p ghPull
	if _, err := h.c.do(ctx, http.MethodGet, h.c.repoPath("/pulls/%d", pr.Number), nil, &p); err != nil {
		return err
	}
	if !p.Draft {
		return nil
	}
	return h.c.graphql(ctx, readyMutation, map[string]any{"id": p.NodeID}, nil)
}

// Comment posts a conversation comment on the PR.
func (h *CodeHost) Comment(ctx context.Context, pr ports.PR, body string) error {
	return h.c.comment(ctx, pr.Number, body)
}

// Review posts APPROVE or REQUEST_CHANGES. GitHub refuses both on a PR
// opened by the same account (422); the review is then posted as COMMENT
// with the verdict in the text.
func (h *CodeHost) Review(ctx context.Context, pr ports.PR, approve bool, body string) error {
	event, verdict := "REQUEST_CHANGES", "Changes requested"
	if approve {
		event, verdict = "APPROVE", "Approved"
	}
	if strings.TrimSpace(body) == "" {
		body = verdict + "."
	}
	path := h.c.repoPath("/pulls/%d/reviews", pr.Number)
	_, err := h.c.do(ctx, http.MethodPost, path, map[string]string{"event": event, "body": body}, nil)
	if httpx.IsStatus(err, http.StatusUnprocessableEntity) {
		_, err = h.c.do(ctx, http.MethodPost, path, map[string]string{
			"event": "COMMENT", "body": "**" + verdict + "**\n\n" + body,
		}, nil)
	}
	return err
}

// ReportCheck sets a commit status with context "barnraise/<name>".
func (h *CodeHost) ReportCheck(ctx context.Context, sha, name string, passed bool, summary string) error {
	state := "failure"
	if passed {
		state = "success"
	}
	if r := []rune(summary); len(r) > 140 {
		summary = string(r[:139]) + "…"
	}
	_, err := h.c.do(ctx, http.MethodPost, h.c.repoPath("/statuses/%s", url.PathEscape(sha)), map[string]string{
		"state": state, "context": "barnraise/" + name, "description": summary,
	}, nil)
	return err
}

// ownCheck is the prefix of the statuses ReportCheck sets.
const ownCheck = "barnraise/"

// Checks reads the check runs (GitHub Actions and apps) and the commit
// statuses of sha. For a status set more than once, the latest counts.
func (h *CodeHost) Checks(ctx context.Context, sha string) (ports.HostChecks, error) {
	var out ports.HostChecks
	var runs struct {
		CheckRuns []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if _, err := h.c.do(ctx, http.MethodGet, h.c.repoPath("/commits/%s/check-runs?per_page=100", url.PathEscape(sha)), nil, &runs); err != nil {
		if !denied(err) {
			return out, err
		}
		// A fine-grained token can never call the Checks API: GitHub
		// Actions' own runs of the commit say the same with actions: read.
		var wf struct {
			Runs []struct {
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
			} `json:"workflow_runs"`
		}
		if _, err := h.c.do(ctx, http.MethodGet, h.c.repoPath("/actions/runs?head_sha=%s&per_page=100", url.QueryEscape(sha)), nil, &wf); err != nil {
			return out, checksErr(err)
		}
		runs.CheckRuns = runs.CheckRuns[:0]
		for _, r := range wf.Runs {
			runs.CheckRuns = append(runs.CheckRuns, struct {
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
			}{r.Name, r.Status, r.Conclusion})
		}
	}
	for _, r := range runs.CheckRuns {
		out.Total++
		switch {
		case r.Status != "completed":
			out.Pending++
		case r.Conclusion != "success" && r.Conclusion != "neutral" && r.Conclusion != "skipped":
			out.Failed = append(out.Failed, r.Name)
		}
	}
	var combined struct {
		Statuses []struct {
			Context string `json:"context"`
			State   string `json:"state"`
		} `json:"statuses"`
	}
	if _, err := h.c.do(ctx, http.MethodGet, h.c.repoPath("/commits/%s/status?per_page=100", url.PathEscape(sha)), nil, &combined); err != nil {
		return out, checksErr(err)
	}
	for _, st := range combined.Statuses {
		if strings.HasPrefix(st.Context, ownCheck) {
			continue
		}
		out.Total++
		switch st.State {
		case "success":
		case "pending":
			out.Pending++
		default:
			out.Failed = append(out.Failed, st.Context)
		}
	}
	return out, nil
}

// checksErr marks a refusal (a fine-grained token without the Checks or
// Commit statuses permission answers 403) so the queue does not wait for
// checks it will never be shown.
func checksErr(err error) error {
	if denied(err) {
		return fmt.Errorf("%w: %w", ports.ErrChecksDenied, err)
	}
	return err
}

// denied reports a refusal of the token (401 or 403).
func denied(err error) bool {
	switch httpx.StatusOf(err) {
	case http.StatusUnauthorized, http.StatusForbidden:
		return true
	}
	return false
}

// Merge merges with squash (default), rebase or merge and returns the sha.
func (h *CodeHost) Merge(ctx context.Context, pr ports.PR, method string) (string, error) {
	switch method {
	case "":
		method = "squash"
	case "squash", "rebase", "merge":
	default:
		return "", fmt.Errorf("github: unknown merge method %q", method)
	}
	var out struct {
		SHA    string `json:"sha"`
		Merged bool   `json:"merged"`
	}
	if _, err := h.c.do(ctx, http.MethodPut, h.c.repoPath("/pulls/%d/merge", pr.Number), map[string]string{"merge_method": method}, &out); err != nil {
		return "", err
	}
	if !out.Merged {
		return "", fmt.Errorf("github: PR #%d was not merged", pr.Number)
	}
	return out.SHA, nil
}

// Close comments the reason (when given) and closes the PR.
func (h *CodeHost) Close(ctx context.Context, pr ports.PR, reason string) error {
	if strings.TrimSpace(reason) != "" {
		if err := h.c.comment(ctx, pr.Number, reason); err != nil {
			return err
		}
	}
	_, err := h.c.do(ctx, http.MethodPatch, h.c.repoPath("/pulls/%d", pr.Number), map[string]string{"state": "closed"}, nil)
	return err
}

// DispatchWorkflow triggers a workflow_dispatch run of a workflow file name
// (e.g. "ci.yml") or id on ref.
func (h *CodeHost) DispatchWorkflow(ctx context.Context, workflow, ref string, inputs map[string]string) error {
	body := map[string]any{"ref": ref}
	if len(inputs) > 0 {
		body["inputs"] = inputs
	}
	_, err := h.c.do(ctx, http.MethodPost, h.c.repoPath("/actions/workflows/%s/dispatches", url.PathEscape(workflow)), body, nil)
	return err
}
