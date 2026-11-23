// Package linear adapts Linear (GraphQL API) as a Barnraise tracker. Only
// net/http is used.
//
// Item I-42 appears as issue ENG-17 in Linear; the tracker reference stored
// on the item is the identifier "ENG-17".
package linear

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/httpx"
	"github.com/leandrodaf/barnraise/internal/adapters/out/httpx/itemdoc"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Options configures the adapter.
type Options struct {
	APIKey  string // personal API key, or "Bearer <oauth token>"
	TeamKey string // e.g. "ENG"; resolved to TeamID on first use
	TeamID  string
	BaseURL string // default https://api.linear.app/graphql
	// Columns maps a Barnraise status to a workflow state name of the team.
	// Unmapped statuses are matched by name; integrated falls back to the
	// first "completed" state and closed to the first "canceled" state.
	Columns    map[string]string
	HTTPClient *http.Client

	MaxRetries int
	Backoff    func(attempt int) time.Duration
	Sleep      func(ctx context.Context, d time.Duration) error
}

// Tracker shows items as Linear issues.
type Tracker struct {
	opts Options
	http *httpx.Client

	mu     sync.Mutex
	teamID string
	states []workflowState
	labels map[string]string // name → id
}

var _ ports.Tracker = (*Tracker)(nil)

// New builds the tracker.
func New(opts Options) (*Tracker, error) {
	if opts.TeamID == "" && opts.TeamKey == "" {
		return nil, errors.New("linear: team key or team id is required")
	}
	if opts.BaseURL == "" {
		opts.BaseURL = "https://api.linear.app/graphql"
	}
	key := opts.APIKey
	return &Tracker{
		opts:   opts,
		teamID: opts.TeamID,
		http: &httpx.Client{
			HTTP: opts.HTTPClient,
			Auth: func(r *http.Request) {
				if key != "" {
					r.Header.Set("Authorization", key)
				}
			},
			MaxRetries: opts.MaxRetries,
			Backoff:    opts.Backoff,
			Sleep:      opts.Sleep,
		},
	}, nil
}

// Name is "linear".
func (t *Tracker) Name() string { return "linear" }

func (t *Tracker) gql(ctx context.Context, query string, vars map[string]any, out any) error {
	if err := t.http.GraphQL(ctx, t.opts.BaseURL, query, vars, out); err != nil {
		return fmt.Errorf("linear: %w", err)
	}
	return nil
}

const teamQuery = `query($key: String!) { teams(filter: {key: {eq: $key}}) { nodes { id key name } } }`

func (t *Tracker) team(ctx context.Context) (string, error) {
	t.mu.Lock()
	id := t.teamID
	t.mu.Unlock()
	if id != "" {
		return id, nil
	}
	var data struct {
		Teams struct {
			Nodes []struct {
				ID  string `json:"id"`
				Key string `json:"key"`
			} `json:"nodes"`
		} `json:"teams"`
	}
	if err := t.gql(ctx, teamQuery, map[string]any{"key": t.opts.TeamKey}, &data); err != nil {
		return "", err
	}
	if len(data.Teams.Nodes) == 0 {
		return "", fmt.Errorf("linear: team %q not found", t.opts.TeamKey)
	}
	id = data.Teams.Nodes[0].ID
	t.mu.Lock()
	t.teamID = id
	t.mu.Unlock()
	return id, nil
}

type workflowState struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Type     string  `json:"type"` // triage, backlog, unstarted, started, completed, canceled
	Position float64 `json:"position"`
}

const statesQuery = `query($team: ID!) {
  workflowStates(filter: {team: {id: {eq: $team}}}, first: 100) { nodes { id name type position } }
}`

func (t *Tracker) workflowStates(ctx context.Context) ([]workflowState, error) {
	t.mu.Lock()
	st := t.states
	t.mu.Unlock()
	if st != nil {
		return st, nil
	}
	team, err := t.team(ctx)
	if err != nil {
		return nil, err
	}
	var data struct {
		WorkflowStates struct {
			Nodes []workflowState `json:"nodes"`
		} `json:"workflowStates"`
	}
	if err := t.gql(ctx, statesQuery, map[string]any{"team": team}, &data); err != nil {
		return nil, err
	}
	st = data.WorkflowStates.Nodes
	// Stable workflow order: by position.
	for i := 1; i < len(st); i++ {
		for j := i; j > 0 && st[j].Position < st[j-1].Position; j-- {
			st[j], st[j-1] = st[j-1], st[j]
		}
	}
	t.mu.Lock()
	t.states = st
	t.mu.Unlock()
	return st, nil
}

// stateFor picks the workflow state for a Barnraise status; "" means leave
// the state alone.
func (t *Tracker) stateFor(ctx context.Context, s domain.Status) (string, error) {
	states, err := t.workflowStates(ctx)
	if err != nil {
		return "", err
	}
	name, mapped := t.opts.Columns[string(s)]
	if !mapped {
		name = string(s)
	}
	for _, w := range states {
		if itemdoc.SameName(w.Name, name) {
			return w.ID, nil
		}
	}
	if mapped {
		return "", fmt.Errorf("linear: team has no workflow state %q (mapped from %s)", name, s)
	}
	fallback := map[domain.Status]string{domain.StatusIntegrated: "completed", domain.StatusClosed: "canceled"}[s]
	for _, w := range states {
		if fallback != "" && w.Type == fallback {
			return w.ID, nil
		}
	}
	return "", nil
}

const issuesQuery = `query($team: ID!, $since: DateTimeOrDuration!, $after: String) {
  issues(filter: {team: {id: {eq: $team}}, updatedAt: {gte: $since}}, first: 100, after: $after, orderBy: updatedAt) {
    nodes {
      id identifier number title description url updatedAt
      state { name type }
      labels { nodes { name } }
      creator { name displayName }
    }
    pageInfo { hasNextPage endCursor }
  }
}`

type linIssue struct {
	ID          string    `json:"id"`
	Identifier  string    `json:"identifier"`
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	URL         string    `json:"url"`
	UpdatedAt   time.Time `json:"updatedAt"`
	State       struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"state"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Creator *struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
	} `json:"creator"`
}

func (i linIssue) toItem() ports.TrackerItem {
	ti := ports.TrackerItem{
		Ref:     i.Identifier,
		Number:  i.Number,
		Title:   domain.SanitizeExternal(i.Title),
		Body:    domain.SanitizeExternal(i.Description),
		Status:  i.State.Name,
		URL:     i.URL,
		Updated: i.UpdatedAt,
		Closed:  i.State.Type == "completed" || i.State.Type == "canceled",
	}
	for _, l := range i.Labels.Nodes {
		ti.Labels = append(ti.Labels, l.Name)
	}
	if i.Creator != nil {
		ti.Author = i.Creator.DisplayName
		if ti.Author == "" {
			ti.Author = i.Creator.Name
		}
	}
	return ti
}

// List returns the team's issues updated since the given time.
func (t *Tracker) List(ctx context.Context, since time.Time) ([]ports.TrackerItem, error) {
	team, err := t.team(ctx)
	if err != nil {
		return nil, err
	}
	if since.IsZero() {
		since = time.Unix(0, 0)
	}
	var out []ports.TrackerItem
	var after any
	for page := 0; page < 1000; page++ {
		var data struct {
			Issues struct {
				Nodes    []linIssue `json:"nodes"`
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"issues"`
		}
		vars := map[string]any{"team": team, "since": since.UTC().Format(time.RFC3339Nano), "after": after}
		if err := t.gql(ctx, issuesQuery, vars, &data); err != nil {
			return nil, err
		}
		for _, i := range data.Issues.Nodes {
			out = append(out, i.toItem())
		}
		if !data.Issues.PageInfo.HasNextPage || data.Issues.PageInfo.EndCursor == "" {
			break
		}
		after = data.Issues.PageInfo.EndCursor
	}
	return out, nil
}

// priority maps P0..P3 to Linear's 1 (urgent) .. 4 (low).
func priority(p domain.Priority) int { return int(p) + 1 }

const labelsQuery = `query($team: ID!) {
  team(id: $team) { labels(first: 250) { nodes { id name } } }
  issueLabels(filter: {team: {null: true}}, first: 250) { nodes { id name } }
}`

const labelCreate = `mutation($name: String!, $team: String!) {
  issueLabelCreate(input: {name: $name, teamId: $team}) { success issueLabel { id name } }
}`

// labelIDs resolves label names to ids, creating missing team labels.
func (t *Tracker) labelIDs(ctx context.Context, names []string) ([]string, error) {
	team, err := t.team(ctx)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	known := t.labels
	t.mu.Unlock()
	if known == nil {
		var data struct {
			Team struct {
				Labels struct {
					Nodes []struct{ ID, Name string } `json:"nodes"`
				} `json:"labels"`
			} `json:"team"`
			IssueLabels struct {
				Nodes []struct{ ID, Name string } `json:"nodes"`
			} `json:"issueLabels"`
		}
		if err := t.gql(ctx, labelsQuery, map[string]any{"team": team}, &data); err != nil {
			return nil, err
		}
		known = map[string]string{}
		for _, l := range append(data.IssueLabels.Nodes, data.Team.Labels.Nodes...) {
			known[strings.ToLower(l.Name)] = l.ID
		}
	}
	var ids []string
	for _, n := range names {
		id, ok := known[strings.ToLower(n)]
		if !ok {
			var res struct {
				IssueLabelCreate struct {
					Success    bool `json:"success"`
					IssueLabel struct {
						ID string `json:"id"`
					} `json:"issueLabel"`
				} `json:"issueLabelCreate"`
			}
			if err := t.gql(ctx, labelCreate, map[string]any{"name": n, "team": team}, &res); err != nil {
				return nil, err
			}
			id = res.IssueLabelCreate.IssueLabel.ID
			known[strings.ToLower(n)] = id
		}
		ids = append(ids, id)
	}
	t.mu.Lock()
	t.labels = known
	t.mu.Unlock()
	return ids, nil
}

const createMutation = `mutation($input: IssueCreateInput!) {
  issueCreate(input: $input) {
    success
    issue { id identifier number title description url updatedAt state { name type } labels { nodes { name } } }
  }
}`

// Create opens an issue for the item in the team, with its priority, the
// barnraise/area/kind labels and the mapped workflow state.
func (t *Tracker) Create(ctx context.Context, it *domain.Item, lang string) (ports.TrackerItem, error) {
	team, err := t.team(ctx)
	if err != nil {
		return ports.TrackerItem{}, err
	}
	input := map[string]any{
		"teamId":      team,
		"title":       itemdoc.Title(it),
		"description": itemdoc.Markdown(it, lang),
		"priority":    priority(it.Priority),
	}
	if ids, err := t.labelIDs(ctx, linearLabels(it)); err != nil {
		return ports.TrackerItem{}, err
	} else if len(ids) > 0 {
		input["labelIds"] = ids
	}
	state, err := t.stateFor(ctx, it.Status)
	if err != nil {
		return ports.TrackerItem{}, err
	}
	if state != "" {
		input["stateId"] = state
	}
	var data struct {
		IssueCreate struct {
			Success bool     `json:"success"`
			Issue   linIssue `json:"issue"`
		} `json:"issueCreate"`
	}
	if err := t.gql(ctx, createMutation, map[string]any{"input": input}, &data); err != nil {
		return ports.TrackerItem{}, err
	}
	if !data.IssueCreate.Success {
		return ports.TrackerItem{}, errors.New("linear: issueCreate did not succeed")
	}
	return data.IssueCreate.Issue.toItem(), nil
}

// linearLabels are the managed labels without priority and status, which
// Linear holds natively.
func linearLabels(it *domain.Item) []string {
	var out []string
	for _, l := range itemdoc.Labels(it) {
		if l != it.Priority.String() {
			out = append(out, l)
		}
	}
	return out
}

const issueIDQuery = `query($id: String!) { issue(id: $id) { id } }`

func (t *Tracker) issueID(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("linear: empty reference")
	}
	var data struct {
		Issue *struct {
			ID string `json:"id"`
		} `json:"issue"`
	}
	if err := t.gql(ctx, issueIDQuery, map[string]any{"id": ref}, &data); err != nil {
		return "", err
	}
	if data.Issue == nil || data.Issue.ID == "" {
		return "", fmt.Errorf("linear: issue %s not found", ref)
	}
	return data.Issue.ID, nil
}

const updateMutation = `mutation($id: String!, $input: IssueUpdateInput!) {
  issueUpdate(id: $id, input: $input) { success }
}`

// Update rewrites title, description and priority, adds the managed labels
// (labels people added are kept) and moves the issue to the mapped state.
func (t *Tracker) Update(ctx context.Context, it *domain.Item, lang string) error {
	id, err := t.issueID(ctx, it.TrackerRef)
	if err != nil {
		return err
	}
	input := map[string]any{
		"title":       itemdoc.Title(it),
		"description": itemdoc.Markdown(it, lang),
		"priority":    priority(it.Priority),
	}
	ids, err := t.labelIDs(ctx, linearLabels(it))
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		input["addedLabelIds"] = ids
	}
	state, err := t.stateFor(ctx, it.Status)
	if err != nil {
		return err
	}
	if state != "" {
		input["stateId"] = state
	}
	var data struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}
	if err := t.gql(ctx, updateMutation, map[string]any{"id": id, "input": input}, &data); err != nil {
		return err
	}
	if !data.IssueUpdate.Success {
		return fmt.Errorf("linear: issueUpdate of %s did not succeed", it.TrackerRef)
	}
	return nil
}

const commentMutation = `mutation($issue: String!, $body: String!) {
  commentCreate(input: {issueId: $issue, body: $body}) { success }
}`

// Comment adds a comment to an issue ("ENG-17").
func (t *Tracker) Comment(ctx context.Context, ref, text string) error {
	id, err := t.issueID(ctx, ref)
	if err != nil {
		return err
	}
	var data struct {
		CommentCreate struct {
			Success bool `json:"success"`
		} `json:"commentCreate"`
	}
	if err := t.gql(ctx, commentMutation, map[string]any{"issue": id, "body": text}, &data); err != nil {
		return err
	}
	if !data.CommentCreate.Success {
		return fmt.Errorf("linear: commentCreate on %s did not succeed", ref)
	}
	return nil
}

// Columns returns the team's workflow state names in board order.
func (t *Tracker) Columns(ctx context.Context) ([]string, error) {
	states, err := t.workflowStates(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(states))
	for _, s := range states {
		out = append(out, s.Name)
	}
	return out, nil
}
