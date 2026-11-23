package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/httpx"
	"github.com/leandrodaf/barnraise/internal/adapters/out/httpx/itemdoc"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Tracker shows items as GitHub issues, optionally on a Projects v2 board.
type Tracker struct{ c *Client }

var _ ports.Tracker = (*Tracker)(nil)

// Name is "github".
func (t *Tracker) Name() string { return "github" }

type ghLabel struct {
	Name string `json:"name"`
}

type ghUser struct {
	Login string `json:"login"`
}

type ghIssue struct {
	Number      int             `json:"number"`
	NodeID      string          `json:"node_id"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	State       string          `json:"state"`
	HTMLURL     string          `json:"html_url"`
	Labels      []ghLabel       `json:"labels"`
	User        ghUser          `json:"user"`
	UpdatedAt   time.Time       `json:"updated_at"`
	PullRequest json.RawMessage `json:"pull_request,omitempty"`
}

func (i ghIssue) labelNames() []string {
	out := make([]string, 0, len(i.Labels))
	for _, l := range i.Labels {
		out = append(out, l.Name)
	}
	return out
}

func (i ghIssue) toItem() ports.TrackerItem {
	labels := i.labelNames()
	status := itemdoc.StatusFromLabels(labels)
	if status == "" {
		status = i.State
	}
	return ports.TrackerItem{
		Ref:     Ref(i.Number),
		Number:  i.Number,
		Title:   domain.SanitizeExternal(i.Title),
		Body:    domain.SanitizeExternal(i.Body),
		Status:  status,
		Labels:  labels,
		Author:  i.User.Login,
		URL:     i.HTMLURL,
		Updated: i.UpdatedAt,
		Closed:  i.State == "closed",
	}
}

// List returns issues (not pull requests) updated since the given time,
// following pagination. Titles and bodies are sanitized.
func (t *Tracker) List(ctx context.Context, since time.Time) ([]ports.TrackerItem, error) {
	q := url.Values{"state": {"all"}, "sort": {"updated"}, "direction": {"asc"}, "per_page": {"100"}}
	if !since.IsZero() {
		q.Set("since", since.UTC().Format(time.RFC3339))
	}
	next := t.c.repoPath("/issues") + "?" + q.Encode()
	var out []ports.TrackerItem
	for page := 0; next != "" && page < 1000; page++ {
		var issues []ghIssue
		resp, err := t.c.do(ctx, http.MethodGet, next, nil, &issues)
		if err != nil {
			return nil, err
		}
		for _, i := range issues {
			if len(i.PullRequest) > 0 && string(i.PullRequest) != "null" {
				continue
			}
			out = append(out, i.toItem())
		}
		next = httpx.NextLink(resp.Header)
	}
	return out, nil
}

// Create opens an issue for the item: "[I-42] Title", a body in the reader's
// language and the managed labels. When a project is configured the issue is
// added to it with the item's status; a failure there is returned together
// with the created issue.
func (t *Tracker) Create(ctx context.Context, it *domain.Item, lang string) (ports.TrackerItem, error) {
	labels := append(itemdoc.Labels(it), itemdoc.StatusLabel(it.Status))
	var created ghIssue
	_, err := t.c.do(ctx, http.MethodPost, t.c.repoPath("/issues"), map[string]any{
		"title":  itemdoc.Title(it),
		"body":   itemdoc.Markdown(it, lang),
		"labels": labels,
	}, &created)
	if err != nil {
		return ports.TrackerItem{}, err
	}
	ti := created.toItem()
	if itemdoc.Closed(it) {
		if err := t.setState(ctx, created.Number, it); err != nil {
			return ti, err
		}
		ti.Closed = true
	}
	if t.c.opts.Project > 0 {
		if err := t.c.setProjectStatus(ctx, created.Number, string(it.Status)); err != nil {
			return ti, err
		}
	}
	return ti, nil
}

func (t *Tracker) setState(ctx context.Context, n int, it *domain.Item) error {
	_, err := t.c.do(ctx, http.MethodPatch, t.c.repoPath("/issues/%d", n), stateFields(it, map[string]any{}), nil)
	return err
}

func stateFields(it *domain.Item, m map[string]any) map[string]any {
	if itemdoc.Closed(it) {
		m["state"] = "closed"
		if it.Status == domain.StatusIntegrated {
			m["state_reason"] = "completed"
		} else {
			m["state_reason"] = "not_planned"
		}
	} else {
		m["state"] = "open"
	}
	return m
}

// Update rewrites title, body, state and managed labels of the item's issue,
// keeping labels people added, and moves its project item.
func (t *Tracker) Update(ctx context.Context, it *domain.Item, lang string) error {
	n, err := ParseRef(it.TrackerRef)
	if err != nil {
		return err
	}
	var cur ghIssue
	if _, err := t.c.do(ctx, http.MethodGet, t.c.repoPath("/issues/%d", n), nil, &cur); err != nil {
		return err
	}
	managed := append(itemdoc.Labels(it), itemdoc.StatusLabel(it.Status))
	fields := stateFields(it, map[string]any{
		"labels": itemdoc.MergeLabels(cur.labelNames(), managed),
	})
	if !it.Adopted {
		// An issue a person wrote keeps its own title and text.
		fields["title"] = itemdoc.Title(it)
		fields["body"] = itemdoc.Markdown(it, lang)
	}
	if _, err := t.c.do(ctx, http.MethodPatch, t.c.repoPath("/issues/%d", n), fields, nil); err != nil {
		return err
	}
	if t.c.opts.Project > 0 {
		return t.c.setProjectStatus(ctx, n, string(it.Status))
	}
	return nil
}

// Comment adds a comment to an issue ("#1608").
func (t *Tracker) Comment(ctx context.Context, ref, text string) error {
	n, err := ParseRef(ref)
	if err != nil {
		return err
	}
	return t.c.comment(ctx, n, text)
}

func (c *Client) comment(ctx context.Context, n int, text string) error {
	_, err := c.do(ctx, http.MethodPost, c.repoPath("/issues/%d/comments", n), map[string]string{"body": text}, nil)
	return err
}

// Columns returns the Status options of the project, or Barnraise's
// statuses when no project is configured.
func (t *Tracker) Columns(ctx context.Context) ([]string, error) {
	if t.c.opts.Project <= 0 {
		return itemdoc.DefaultColumns(), nil
	}
	p, err := t.c.projectInfo(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(p.Options))
	for _, o := range p.Options {
		out = append(out, o.Name)
	}
	return out, nil
}
