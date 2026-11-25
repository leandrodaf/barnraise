package gitlab

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/httpx/itemdoc"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Tracker shows items as GitLab issues; the status is a label (a board list).
type Tracker struct{ c *Client }

var _ ports.Tracker = (*Tracker)(nil)

// Name is "gitlab".
func (t *Tracker) Name() string { return "gitlab" }

// RefURL is the issue's web page: #17 -> https://gitlab.com/group/name/-/issues/17.
// A project given by its numeric id has no path to link.
func (t *Tracker) RefURL(ref string) string {
	if strings.HasPrefix(ref, "https://") {
		return ref
	}
	n, err := ParseRef(ref)
	p := strings.Trim(t.c.opts.Project, "/")
	if err != nil || !strings.Contains(p, "/") {
		return ""
	}
	return strings.TrimSuffix(strings.TrimRight(t.c.opts.BaseURL, "/"), "/api/v4") + "/" + p + "/-/issues/" + strconv.Itoa(n)
}

type glUser struct {
	Username string `json:"username"`
}

type glIssue struct {
	IID         int       `json:"iid"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	State       string    `json:"state"` // opened, closed
	Labels      []string  `json:"labels"`
	Author      glUser    `json:"author"`
	WebURL      string    `json:"web_url"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (t *Tracker) toItem(i glIssue) ports.TrackerItem {
	status := itemdoc.StatusFromLabels(i.Labels)
	if status == "" {
		for s, label := range t.c.opts.Columns {
			for _, l := range i.Labels {
				if l == label {
					status = s
				}
			}
		}
	}
	if status == "" {
		status = i.State
	}
	return ports.TrackerItem{
		Ref:     "#" + itoa(i.IID),
		Number:  i.IID,
		Title:   domain.SanitizeExternal(i.Title),
		Body:    domain.SanitizeExternal(i.Description),
		Status:  status,
		Labels:  i.Labels,
		Author:  i.Author.Username,
		URL:     i.WebURL,
		Updated: i.UpdatedAt,
		Closed:  i.State == "closed",
	}
}

// List returns issues updated since the given time, following pagination.
func (t *Tracker) List(ctx context.Context, since time.Time) ([]ports.TrackerItem, error) {
	q := url.Values{"scope": {"all"}, "order_by": {"updated_at"}, "sort": {"asc"}, "per_page": {"100"}}
	if !since.IsZero() {
		q.Set("updated_after", since.UTC().Format(time.RFC3339))
	}
	next := t.c.http.URL(t.c.path("/issues")) + "?" + q.Encode()
	var out []ports.TrackerItem
	for page := 0; next != "" && page < 1000; page++ {
		var issues []glIssue
		resp, err := t.c.do(ctx, http.MethodGet, next, nil, &issues)
		if err != nil {
			return nil, err
		}
		for _, i := range issues {
			out = append(out, t.toItem(i))
		}
		next = nextPage(resp, next)
	}
	return out, nil
}

func (t *Tracker) statusLabel(s domain.Status) string {
	if l, ok := t.c.opts.Columns[string(s)]; ok && l != "" {
		return l
	}
	return itemdoc.StatusLabel(s)
}

func (t *Tracker) managedLabels(it *domain.Item) []string {
	return append(itemdoc.Labels(it), t.statusLabel(it.Status))
}

// Create opens an issue for the item.
func (t *Tracker) Create(ctx context.Context, it *domain.Item, lang string) (ports.TrackerItem, error) {
	var created glIssue
	_, err := t.c.do(ctx, http.MethodPost, t.c.path("/issues"), map[string]any{
		"title":       itemdoc.Title(it),
		"description": itemdoc.Markdown(it, lang),
		"labels":      strings.Join(t.managedLabels(it), ","),
	}, &created)
	if err != nil {
		return ports.TrackerItem{}, err
	}
	if itemdoc.Closed(it) {
		if _, err := t.c.do(ctx, http.MethodPut, t.c.path("/issues/%d", created.IID), map[string]string{"state_event": "close"}, &created); err != nil {
			return t.toItem(created), err
		}
	}
	return t.toItem(created), nil
}

// Update rewrites title, description, state and managed labels, keeping
// labels people added.
func (t *Tracker) Update(ctx context.Context, it *domain.Item, lang string) error {
	n, err := ParseRef(it.TrackerRef)
	if err != nil {
		return err
	}
	var cur glIssue
	if _, err := t.c.do(ctx, http.MethodGet, t.c.path("/issues/%d", n), nil, &cur); err != nil {
		return err
	}
	columnLabels := map[string]bool{}
	for _, l := range t.c.opts.Columns {
		columnLabels[l] = true
	}
	var kept []string
	for _, l := range cur.Labels {
		if !columnLabels[l] {
			kept = append(kept, l)
		}
	}
	fields := map[string]any{
		"labels": strings.Join(itemdoc.MergeLabels(kept, t.managedLabels(it)), ","),
	}
	if !it.Adopted {
		// An issue a person wrote keeps its own title and description.
		fields["title"] = itemdoc.Title(it)
		fields["description"] = itemdoc.Markdown(it, lang)
	}
	switch closed := itemdoc.Closed(it); {
	case closed && cur.State != "closed":
		fields["state_event"] = "close"
	case !closed && cur.State == "closed":
		fields["state_event"] = "reopen"
	}
	_, err = t.c.do(ctx, http.MethodPut, t.c.path("/issues/%d", n), fields, nil)
	return err
}

// Comment adds a note to an issue ("#17").
func (t *Tracker) Comment(ctx context.Context, ref, text string) error {
	n, err := ParseRef(ref)
	if err != nil {
		return err
	}
	_, err = t.c.do(ctx, http.MethodPost, t.c.path("/issues/%d/notes", n), map[string]string{"body": text}, nil)
	return err
}

// Columns returns the labels of the project's first issue board lists, or
// Barnraise's statuses when the project has no board lists.
func (t *Tracker) Columns(ctx context.Context) ([]string, error) {
	var boards []struct {
		Lists []struct {
			Position int `json:"position"`
			Label    *struct {
				Name string `json:"name"`
			} `json:"label"`
		} `json:"lists"`
	}
	if _, err := t.c.do(ctx, http.MethodGet, t.c.path("/boards"), nil, &boards); err != nil {
		return nil, err
	}
	var out []string
	if len(boards) > 0 {
		for _, l := range boards[0].Lists {
			if l.Label != nil && l.Label.Name != "" {
				out = append(out, l.Label.Name)
			}
		}
	}
	if len(out) == 0 {
		return itemdoc.DefaultColumns(), nil
	}
	return out, nil
}
