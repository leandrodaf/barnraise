package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/httpx"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// CodeHost drives merge requests, notes, approvals, statuses and pipelines.
// ports.PR.Number is the merge request iid.
type CodeHost struct{ c *Client }

var _ ports.CodeHost = (*CodeHost)(nil)

// Name is "gitlab".
func (h *CodeHost) Name() string { return "gitlab" }

func itoa(n int) string { return strconv.Itoa(n) }

type glMR struct {
	IID             int    `json:"iid"`
	Title           string `json:"title"`
	WebURL          string `json:"web_url"`
	State           string `json:"state"` // opened, closed, merged, locked
	Draft           bool   `json:"draft"`
	WIP             bool   `json:"work_in_progress"`
	SourceBranch    string `json:"source_branch"`
	SHA             string `json:"sha"`
	MergeCommitSHA  string `json:"merge_commit_sha"`
	SquashCommitSHA string `json:"squash_commit_sha"`
	RebaseInProg    bool   `json:"rebase_in_progress"`
	MergeError      string `json:"merge_error"`
}

func (m glMR) toPR() ports.PR {
	return ports.PR{Number: m.IID, URL: m.WebURL, State: m.State, Draft: m.Draft || m.WIP, Head: m.SourceBranch}
}

var draftPrefix = regexp.MustCompile(`(?i)^\s*(\[draft\]|\(draft\)|draft:|draft\s*-|wip:|\[wip\])\s*`)

func stripDraft(title string) string { return draftPrefix.ReplaceAllString(title, "") }

func draftTitle(title string) string { return "Draft: " + stripDraft(title) }

func (h *CodeHost) findOpen(ctx context.Context, branch string) (*glMR, error) {
	q := url.Values{"source_branch": {branch}, "state": {"opened"}}
	var mrs []glMR
	if _, err := h.c.do(ctx, http.MethodGet, h.c.path("/merge_requests")+"?"+q.Encode(), nil, &mrs); err != nil {
		return nil, err
	}
	for i := range mrs {
		if mrs[i].SourceBranch == branch {
			return &mrs[i], nil
		}
	}
	return nil, nil
}

// OpenDraftPR opens a draft merge request ("Draft: " title prefix), or
// returns the open one from the same source branch.
func (h *CodeHost) OpenDraftPR(ctx context.Context, branch, base, title, body string) (ports.PR, error) {
	if m, err := h.findOpen(ctx, branch); err != nil {
		return ports.PR{}, err
	} else if m != nil {
		return m.toPR(), nil
	}
	var created glMR
	_, err := h.c.do(ctx, http.MethodPost, h.c.path("/merge_requests"), map[string]any{
		"source_branch": branch, "target_branch": base, "title": draftTitle(title), "description": body,
	}, &created)
	if httpx.IsStatus(err, http.StatusConflict) {
		if m, ferr := h.findOpen(ctx, branch); ferr == nil && m != nil {
			return m.toPR(), nil
		}
	}
	if err != nil {
		return ports.PR{}, err
	}
	created.Draft = true
	return created.toPR(), nil
}

// UpdatePR changes title and description, keeping the draft prefix while
// the merge request is a draft.
func (h *CodeHost) UpdatePR(ctx context.Context, pr ports.PR, title, body string) error {
	m := map[string]any{}
	if title != "" {
		if pr.Draft {
			title = draftTitle(title)
		}
		m["title"] = title
	}
	if body != "" {
		m["description"] = body
	}
	if len(m) == 0 {
		return nil
	}
	_, err := h.c.do(ctx, http.MethodPut, h.c.path("/merge_requests/%d", pr.Number), m, nil)
	return err
}

// MarkReady removes the draft prefix from the title.
func (h *CodeHost) MarkReady(ctx context.Context, pr ports.PR) error {
	var m glMR
	if _, err := h.c.do(ctx, http.MethodGet, h.c.path("/merge_requests/%d", pr.Number), nil, &m); err != nil {
		return err
	}
	ready := stripDraft(m.Title)
	if !m.Draft && !m.WIP && ready == m.Title {
		return nil
	}
	_, err := h.c.do(ctx, http.MethodPut, h.c.path("/merge_requests/%d", pr.Number), map[string]string{"title": ready}, nil)
	return err
}

func (h *CodeHost) note(ctx context.Context, iid int, body string) error {
	_, err := h.c.do(ctx, http.MethodPost, h.c.path("/merge_requests/%d/notes", iid), map[string]string{"body": body}, nil)
	return err
}

// Comment adds a note to the merge request.
func (h *CodeHost) Comment(ctx context.Context, pr ports.PR, body string) error {
	return h.note(ctx, pr.Number, body)
}

// Review approves (best effort: approvals need a tier or a different user)
// or withdraws an approval, and always leaves the verdict as a note.
func (h *CodeHost) Review(ctx context.Context, pr ports.PR, approve bool, body string) error {
	verdict := "Changes requested"
	action := "/merge_requests/%d/unapprove"
	if approve {
		verdict, action = "Approved", "/merge_requests/%d/approve"
	}
	_, err := h.c.do(ctx, http.MethodPost, h.c.path(action, pr.Number), nil, nil)
	if err != nil && !httpx.IsStatus(err, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		http.StatusMethodNotAllowed, http.StatusConflict, http.StatusUnprocessableEntity) {
		return err
	}
	text := "**" + verdict + "**"
	if strings.TrimSpace(body) != "" {
		text += "\n\n" + body
	}
	return h.note(ctx, pr.Number, text)
}

// ReportCheck sets a commit status named "barnraise/<name>".
func (h *CodeHost) ReportCheck(ctx context.Context, sha, name string, passed bool, summary string) error {
	state := "failed"
	if passed {
		state = "success"
	}
	if r := []rune(summary); len(r) > 255 {
		summary = string(r[:254]) + "…"
	}
	_, err := h.c.do(ctx, http.MethodPost, h.c.path("/statuses/%s", url.PathEscape(sha)), map[string]string{
		"state": state, "name": "barnraise/" + name, "description": summary,
	}, nil)
	return err
}

// Merge accepts the merge request. "squash" squashes, "merge" keeps the
// commits, "rebase" asks GitLab to rebase first and waits for it. Returns
// the merge commit, else the squash commit, else the head sha
// (fast-forward projects).
func (h *CodeHost) Merge(ctx context.Context, pr ports.PR, method string) (string, error) {
	squash := false
	switch method {
	case "", "squash":
		squash = true
	case "merge":
	case "rebase":
		if err := h.rebase(ctx, pr.Number); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("gitlab: unknown merge method %q", method)
	}
	var m glMR
	if _, err := h.c.do(ctx, http.MethodPut, h.c.path("/merge_requests/%d/merge", pr.Number), map[string]any{"squash": squash}, &m); err != nil {
		return "", err
	}
	if m.State != "" && m.State != "merged" {
		return "", fmt.Errorf("gitlab: merge request !%d is %s after merge", pr.Number, m.State)
	}
	for _, s := range []string{m.MergeCommitSHA, m.SquashCommitSHA, m.SHA} {
		if s != "" {
			return s, nil
		}
	}
	return "", fmt.Errorf("gitlab: merge of !%d returned no commit", pr.Number)
}

func (h *CodeHost) rebase(ctx context.Context, iid int) error {
	if _, err := h.c.do(ctx, http.MethodPut, h.c.path("/merge_requests/%d/rebase", iid), nil, nil); err != nil {
		return err
	}
	deadline := time.Now().Add(h.c.opts.RebaseWait)
	for attempt := 1; ; attempt++ {
		var m glMR
		if _, err := h.c.do(ctx, http.MethodGet, h.c.path("/merge_requests/%d", iid)+"?include_rebase_in_progress=true", nil, &m); err != nil {
			return err
		}
		if !m.RebaseInProg {
			if m.MergeError != "" {
				return fmt.Errorf("gitlab: rebase of !%d failed: %s", iid, m.MergeError)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("gitlab: rebase of !%d still running after %s", iid, h.c.opts.RebaseWait)
		}
		sleep := h.c.http.Sleep
		if sleep == nil {
			sleep = httpx.Sleep
		}
		if err := sleep(ctx, min(time.Duration(attempt)*500*time.Millisecond, 5*time.Second)); err != nil {
			return err
		}
	}
}

// Close leaves the reason as a note and closes the merge request.
func (h *CodeHost) Close(ctx context.Context, pr ports.PR, reason string) error {
	if strings.TrimSpace(reason) != "" {
		if err := h.note(ctx, pr.Number, reason); err != nil {
			return err
		}
	}
	_, err := h.c.do(ctx, http.MethodPut, h.c.path("/merge_requests/%d", pr.Number), map[string]string{"state_event": "close"}, nil)
	return err
}

// DispatchWorkflow starts a pipeline on ref. GitLab has one pipeline per
// ref, so the workflow name travels as the BARNRAISE_WORKFLOW variable for
// the CI rules to pick jobs; inputs become pipeline variables.
func (h *CodeHost) DispatchWorkflow(ctx context.Context, workflow, ref string, inputs map[string]string) error {
	type variable struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	vars := []variable{}
	if workflow != "" {
		vars = append(vars, variable{"BARNRAISE_WORKFLOW", workflow})
	}
	keys := make([]string, 0, len(inputs))
	for k := range inputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		vars = append(vars, variable{k, inputs[k]})
	}
	_, err := h.c.do(ctx, http.MethodPost, h.c.path("/pipeline"), map[string]any{"ref": ref, "variables": vars}, nil)
	return err
}
