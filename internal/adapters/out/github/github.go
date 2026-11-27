// Package github adapts GitHub to Barnraise: issues and Projects v2 as the
// tracker, pull requests, reviews, commit statuses and Actions as the code
// host, plus webhook verification and parsing. Only net/http is used.
//
// An item and its issue are different things: item I-42 appears as issue
// #1608 on GitHub. The tracker reference stored on the item is "#1608".
package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/httpx"
)

// Options configures the adapter.
type Options struct {
	Repo       string // "owner/name"
	Token      string
	BaseURL    string // default https://api.github.com (GHES: https://host/api/v3)
	GraphQLURL string // default derived from BaseURL
	// Project is the number of a GitHub Projects v2 board; 0 disables it.
	Project int
	// Owner is the user or organization that owns the project; default is
	// the repository owner.
	Owner string
	// Columns maps a Barnraise status to the name of a Status option of the
	// project. Unmapped statuses are matched by name ("in_progress" ↔ "In progress").
	Columns    map[string]string
	HTTPClient *http.Client

	// Retry tuning; zero values use the httpx defaults.
	MaxRetries int
	Backoff    func(attempt int) time.Duration
	Sleep      func(ctx context.Context, d time.Duration) error
}

// Client holds the connection shared by the tracker and the code host.
type Client struct {
	opts       Options
	owner      string
	repo       string
	http       *httpx.Client
	graphqlURL string

	mu      sync.Mutex
	project *projectInfo
}

// New builds a client. Use Tracker() and CodeHost() to get the ports.
func New(opts Options) (*Client, error) {
	owner, repo, ok := strings.Cut(strings.Trim(opts.Repo, "/"), "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return nil, fmt.Errorf("github: repo must be \"owner/name\", got %q", opts.Repo)
	}
	if opts.BaseURL == "" {
		opts.BaseURL = "https://api.github.com"
	}
	opts.BaseURL = strings.TrimRight(opts.BaseURL, "/")
	if opts.GraphQLURL == "" {
		opts.GraphQLURL = strings.TrimSuffix(opts.BaseURL, "/v3") + "/graphql"
	}
	if opts.Owner == "" {
		opts.Owner = owner
	}
	token := opts.Token
	c := &Client{
		opts:       opts,
		owner:      owner,
		repo:       repo,
		graphqlURL: opts.GraphQLURL,
		http: &httpx.Client{
			HTTP:    opts.HTTPClient,
			BaseURL: opts.BaseURL,
			Header: http.Header{
				"Accept":               {"application/vnd.github+json"},
				"X-Github-Api-Version": {"2022-11-28"},
			},
			Auth: func(r *http.Request) {
				if token != "" {
					r.Header.Set("Authorization", "Bearer "+token)
				}
			},
			MaxRetries: opts.MaxRetries,
			Backoff:    opts.Backoff,
			Sleep:      opts.Sleep,
		},
	}
	return c, nil
}

// Tracker returns the issues/Projects side.
func (c *Client) Tracker() *Tracker { return &Tracker{c: c} }

// CodeHost returns the pull request side.
func (c *Client) CodeHost() *CodeHost { return &CodeHost{c: c} }

// WebBase is the host's web address: https://github.com, or the GitHub
// Enterprise host whose API is https://host/api/v3.
func (c *Client) WebBase() string {
	b := c.opts.BaseURL
	if b == "https://api.github.com" {
		return "https://github.com"
	}
	return strings.TrimSuffix(strings.TrimSuffix(b, "/v3"), "/api")
}

// NewTracker is New(opts).Tracker().
func NewTracker(opts Options) (*Tracker, error) {
	c, err := New(opts)
	if err != nil {
		return nil, err
	}
	return c.Tracker(), nil
}

// NewCodeHost is New(opts).CodeHost().
func NewCodeHost(opts Options) (*CodeHost, error) {
	c, err := New(opts)
	if err != nil {
		return nil, err
	}
	return c.CodeHost(), nil
}

func (c *Client) repoPath(format string, args ...any) string {
	return "/repos/" + url.PathEscape(c.owner) + "/" + url.PathEscape(c.repo) + fmt.Sprintf(format, args...)
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) (*httpx.Response, error) {
	resp, err := c.http.Do(ctx, method, path, in, out)
	// Issues written at once that name the same new label race to create it,
	// and every one but the first is refused; the label exists now, so the
	// same call goes through. Sixteen issues of one project were lost so.
	for try := 0; err != nil && labelRace(err) && try < 2; try++ {
		select {
		case <-ctx.Done():
			return resp, fmt.Errorf("github: %w", err)
		case <-time.After(time.Duration(300*(try+1)) * time.Millisecond):
		}
		resp, err = c.http.Do(ctx, method, path, in, out)
	}
	if err != nil {
		return resp, fmt.Errorf("github: %w", err)
	}
	return resp, nil
}

func (c *Client) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	if err := c.http.GraphQL(ctx, c.graphqlURL, query, vars, out); err != nil {
		return fmt.Errorf("github: %w", err)
	}
	return nil
}

// ErrBadRef is returned for a reference that is not "#<number>".
var ErrBadRef = errors.New("github: reference is not an issue or pull request number")

// ParseRef accepts "#1608", "1608", "owner/name#1608" or an issue/PR URL.
func ParseRef(ref string) (int, error) {
	r := strings.TrimSpace(ref)
	if i := strings.LastIndexAny(r, "#/"); i >= 0 {
		r = r[i+1:]
	}
	n, err := strconv.Atoi(r)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: %q", ErrBadRef, ref)
	}
	return n, nil
}

// Ref formats an issue number as a tracker reference.
func Ref(n int) string { return "#" + strconv.Itoa(n) }

// labelRace tells GitHub's refusal of a label another request created a
// moment before.
func labelRace(err error) bool {
	m := err.Error()
	return strings.Contains(m, "422") && strings.Contains(m, `"resource":"Label"`) && strings.Contains(m, "already_exists")
}
