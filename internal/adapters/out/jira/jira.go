// Package jira adapts Jira Cloud (REST v3) as a Barnraise tracker. Only
// net/http is used.
//
// Item I-42 appears as issue PROJ-17 in Jira; the tracker reference stored on
// the item is the issue key "PROJ-17".
package jira

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/httpx"
	"github.com/leandrodaf/barnraise/internal/adapters/out/httpx/itemdoc"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Options configures the adapter.
type Options struct {
	BaseURL    string // https://your-site.atlassian.net
	Email      string
	Token      string // API token (basic auth with Email)
	ProjectKey string
	// IssueType is the Jira issue type name for new items (default "Task",
	// which is Jira's own data).
	IssueType string
	// Columns maps a Barnraise status to a Jira status name; the adapter
	// takes the transition that leads there. Unmapped statuses are matched
	// by name; integrated and closed fall back to "Done".
	Columns    map[string]string
	HTTPClient *http.Client

	MaxRetries int
	Backoff    func(attempt int) time.Duration
	Sleep      func(ctx context.Context, d time.Duration) error
	// Now is used to compute the overlap of List; default time.Now.
	Now func() time.Time
}

// Tracker shows items as Jira issues.
type Tracker struct {
	opts Options
	http *httpx.Client
}

var _ ports.Tracker = (*Tracker)(nil)

// New builds the tracker.
func New(opts Options) (*Tracker, error) {
	if opts.BaseURL == "" {
		return nil, errors.New("jira: base URL is required")
	}
	if opts.ProjectKey == "" {
		return nil, errors.New("jira: project key is required")
	}
	if opts.IssueType == "" {
		opts.IssueType = "Task"
	}
	opts.BaseURL = strings.TrimRight(opts.BaseURL, "/")
	email, token := opts.Email, opts.Token
	return &Tracker{
		opts: opts,
		http: &httpx.Client{
			HTTP:    opts.HTTPClient,
			BaseURL: opts.BaseURL,
			Auth: func(r *http.Request) {
				if token != "" {
					r.SetBasicAuth(email, token)
				}
			},
			MaxRetries: opts.MaxRetries,
			Backoff:    opts.Backoff,
			Sleep:      opts.Sleep,
		},
	}, nil
}

// Name is "jira".
func (t *Tracker) Name() string { return "jira" }

// RefURL is the issue's web page: PROJ-12 -> https://site.atlassian.net/browse/PROJ-12.
func (t *Tracker) RefURL(ref string) string {
	if ref = strings.TrimSpace(ref); ref == "" || strings.ContainsAny(ref, "/ ?#") {
		return ""
	}
	return t.opts.BaseURL + "/browse/" + ref
}

func (t *Tracker) do(ctx context.Context, method, path string, in, out any) error {
	if _, err := t.http.Do(ctx, method, path, in, out); err != nil {
		return fmt.Errorf("jira: %w", err)
	}
	return nil
}

type jiraTime struct{ time.Time }

func (j *jiraTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05.000-0700", time.RFC3339Nano, "2006-01-02T15:04:05-0700"} {
		if t, err := time.Parse(layout, s); err == nil {
			j.Time = t
			return nil
		}
	}
	return fmt.Errorf("jira: bad time %q", s)
}

type jiraIssue struct {
	Key    string `json:"key"`
	Fields struct {
		Summary     string   `json:"summary"`
		Description *Block   `json:"description"`
		Labels      []string `json:"labels"`
		Updated     jiraTime `json:"updated"`
		Status      struct {
			Name           string `json:"name"`
			StatusCategory struct {
				Key string `json:"key"`
			} `json:"statusCategory"`
		} `json:"status"`
		Reporter *struct {
			DisplayName  string `json:"displayName"`
			EmailAddress string `json:"emailAddress"`
		} `json:"reporter"`
	} `json:"fields"`
}

func (t *Tracker) toItem(i jiraIssue) ports.TrackerItem {
	f := i.Fields
	ti := ports.TrackerItem{
		Ref:     i.Key,
		Number:  keyNumber(i.Key),
		Title:   domain.SanitizeExternal(f.Summary),
		Body:    domain.SanitizeExternal(PlainText(f.Description)),
		Status:  f.Status.Name,
		Labels:  f.Labels,
		URL:     t.opts.BaseURL + "/browse/" + i.Key,
		Updated: f.Updated.Time,
		Closed:  f.Status.StatusCategory.Key == "done",
	}
	if f.Reporter != nil {
		ti.Author = f.Reporter.DisplayName
		if ti.Author == "" {
			ti.Author = f.Reporter.EmailAddress
		}
	}
	return ti
}

func keyNumber(key string) int {
	_, n, _ := strings.Cut(key, "-")
	v, _ := strconv.Atoi(n)
	return v
}

var listFields = []string{"summary", "description", "labels", "updated", "status", "reporter"}

// List returns issues of the project updated since the given time. JQL has
// minute precision in the account's time zone, so the query overlaps by a
// day and the result is filtered here.
func (t *Tracker) List(ctx context.Context, since time.Time) ([]ports.TrackerItem, error) {
	jql := fmt.Sprintf("project = %s", quote(t.opts.ProjectKey))
	if !since.IsZero() {
		jql += fmt.Sprintf(" AND updated >= %s", quote(since.Add(-24*time.Hour).UTC().Format("2006-01-02 15:04")))
	}
	jql += " ORDER BY updated ASC"
	var out []ports.TrackerItem
	token := ""
	for range 1000 {
		req := map[string]any{"jql": jql, "fields": listFields, "maxResults": 100}
		if token != "" {
			req["nextPageToken"] = token
		}
		var res struct {
			Issues        []jiraIssue `json:"issues"`
			NextPageToken string      `json:"nextPageToken"`
			IsLast        *bool       `json:"isLast"`
		}
		if err := t.do(ctx, http.MethodPost, "/rest/api/3/search/jql", req, &res); err != nil {
			return nil, err
		}
		for _, i := range res.Issues {
			ti := t.toItem(i)
			if !since.IsZero() && ti.Updated.Before(since) {
				continue
			}
			out = append(out, ti)
		}
		if res.NextPageToken == "" || (res.IsLast != nil && *res.IsLast) {
			break
		}
		token = res.NextPageToken
	}
	return out, nil
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// Labels in Jira cannot hold spaces.
func jiraLabels(ls []string) []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, strings.ReplaceAll(strings.TrimSpace(l), " ", "-"))
	}
	return out
}

func description(it *domain.Item, lang string) Block {
	return Document(itemdoc.Sections(it, lang), itemdoc.Footer(it, lang))
}

// Create opens an issue for the item and moves it to the mapped status. A
// failed transition is returned together with the created issue.
func (t *Tracker) Create(ctx context.Context, it *domain.Item, lang string) (ports.TrackerItem, error) {
	labels := jiraLabels(append(itemdoc.Labels(it), itemdoc.StatusLabel(it.Status)))
	var created struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	err := t.do(ctx, http.MethodPost, "/rest/api/3/issue", map[string]any{"fields": map[string]any{
		"project":     map[string]string{"key": t.opts.ProjectKey},
		"issuetype":   map[string]string{"name": t.opts.IssueType},
		"summary":     itemdoc.Title(it),
		"description": description(it, lang),
		"labels":      labels,
	}}, &created)
	if err != nil {
		return ports.TrackerItem{}, err
	}
	ti := ports.TrackerItem{
		Ref: created.Key, Number: keyNumber(created.Key), Title: itemdoc.Title(it),
		Status: string(it.Status), Labels: labels, URL: t.opts.BaseURL + "/browse/" + created.Key,
		Closed: itemdoc.Closed(it),
	}
	if err := t.transition(ctx, created.Key, "", it.Status); err != nil {
		return ti, err
	}
	return ti, nil
}

// Update rewrites summary, description and managed labels, keeping labels
// people added, then transitions to the mapped status.
func (t *Tracker) Update(ctx context.Context, it *domain.Item, lang string) error {
	key := strings.TrimSpace(it.TrackerRef)
	if key == "" {
		return errors.New("jira: item has no tracker reference")
	}
	var cur jiraIssue
	if err := t.do(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(key)+"?fields=labels,status", nil, &cur); err != nil {
		return err
	}
	managed := jiraLabels(append(itemdoc.Labels(it), itemdoc.StatusLabel(it.Status)))
	fields := map[string]any{"labels": itemdoc.MergeLabels(cur.Fields.Labels, managed)}
	if !it.Adopted {
		// An issue a person wrote keeps its own summary and description.
		fields["summary"] = itemdoc.Title(it)
		fields["description"] = description(it, lang)
	}
	err := t.do(ctx, http.MethodPut, "/rest/api/3/issue/"+url.PathEscape(key), map[string]any{"fields": fields}, nil)
	if err != nil {
		return err
	}
	return t.transition(ctx, key, cur.Fields.Status.Name, it.Status)
}

// targetStatus is the Jira status name a Barnraise status should show as.
func (t *Tracker) targetStatus(s domain.Status) (name string, mapped bool) {
	if n, ok := t.opts.Columns[string(s)]; ok && n != "" {
		return n, true
	}
	return string(s), false
}

type transition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	To   struct {
		Name string `json:"name"`
	} `json:"to"`
}

// transition moves the issue to the status mapped from s. An unmapped
// status with no same-named target is left alone; terminal statuses fall
// back to a "Done" target.
func (t *Tracker) transition(ctx context.Context, key, current string, s domain.Status) error {
	target, mapped := t.targetStatus(s)
	if current != "" && itemdoc.SameName(current, target) {
		return nil
	}
	var res struct {
		Transitions []transition `json:"transitions"`
	}
	if err := t.do(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(key)+"/transitions", nil, &res); err != nil {
		return err
	}
	pick := func(name string) *transition {
		for i := range res.Transitions {
			if itemdoc.SameName(res.Transitions[i].To.Name, name) {
				return &res.Transitions[i]
			}
		}
		for i := range res.Transitions {
			if itemdoc.SameName(res.Transitions[i].Name, name) {
				return &res.Transitions[i]
			}
		}
		return nil
	}
	tr := pick(target)
	if tr == nil && !mapped && s.Terminal() {
		if current != "" && itemdoc.SameName(current, "Done") {
			return nil
		}
		tr = pick("Done")
	}
	if tr == nil {
		if !mapped && !s.Terminal() {
			return nil
		}
		var names []string
		for _, x := range res.Transitions {
			names = append(names, x.To.Name)
		}
		return fmt.Errorf("jira: %s has no transition to %q (available: %s)", key, target, strings.Join(names, ", "))
	}
	return t.do(ctx, http.MethodPost, "/rest/api/3/issue/"+url.PathEscape(key)+"/transitions",
		map[string]any{"transition": map[string]string{"id": tr.ID}}, nil)
}

// Comment adds a comment (ADF) to an issue key.
func (t *Tracker) Comment(ctx context.Context, ref, text string) error {
	key := strings.TrimSpace(ref)
	if key == "" {
		return errors.New("jira: empty reference")
	}
	return t.do(ctx, http.MethodPost, "/rest/api/3/issue/"+url.PathEscape(key)+"/comment",
		map[string]any{"body": Paragraphs(text)}, nil)
}

// Columns returns the statuses available to the configured issue type in
// the project, in workflow order.
func (t *Tracker) Columns(ctx context.Context) ([]string, error) {
	var res []struct {
		Name     string `json:"name"`
		Statuses []struct {
			Name string `json:"name"`
		} `json:"statuses"`
	}
	if err := t.do(ctx, http.MethodGet, "/rest/api/3/project/"+url.PathEscape(t.opts.ProjectKey)+"/statuses", nil, &res); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, typ := range res {
		if strings.EqualFold(typ.Name, t.opts.IssueType) {
			for _, s := range typ.Statuses {
				add(s.Name)
			}
		}
	}
	if len(out) == 0 {
		for _, typ := range res {
			for _, s := range typ.Statuses {
				add(s.Name)
			}
		}
	}
	return out, nil
}
