// Package gitlab adapts GitLab (REST v4) to Barnraise: issues as the
// tracker and merge requests, notes, approvals, commit statuses and
// pipelines as the code host. Only net/http is used.
//
// Item I-42 appears as issue #17 of the GitLab project; the tracker
// reference stored on the item is "#17" (the project-scoped iid).
package gitlab

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/httpx"
)

// Options configures the adapter.
type Options struct {
	BaseURL string // default https://gitlab.com/api/v4
	Project string // "group/name" or numeric id; escaped by the adapter
	Token   string // personal, project or group access token (api scope)
	// Columns maps a Barnraise status to the label of a board list. Unmapped
	// statuses use the label "status:<status>".
	Columns    map[string]string
	HTTPClient *http.Client

	MaxRetries int
	Backoff    func(attempt int) time.Duration
	Sleep      func(ctx context.Context, d time.Duration) error
	// RebaseWait is how long Merge with method "rebase" waits for GitLab to
	// finish the rebase (default 2 minutes).
	RebaseWait time.Duration
}

// Client holds the connection shared by the tracker and the code host.
type Client struct {
	opts    Options
	project string // escaped
	http    *httpx.Client
}

// New builds a client. Use Tracker() and CodeHost() to get the ports.
func New(opts Options) (*Client, error) {
	p := strings.Trim(opts.Project, "/")
	if p == "" {
		return nil, errors.New("gitlab: project is required")
	}
	if opts.BaseURL == "" {
		opts.BaseURL = "https://gitlab.com/api/v4"
	}
	if opts.RebaseWait <= 0 {
		opts.RebaseWait = 2 * time.Minute
	}
	token := opts.Token
	return &Client{
		opts:    opts,
		project: url.PathEscape(p),
		http: &httpx.Client{
			HTTP:    opts.HTTPClient,
			BaseURL: strings.TrimRight(opts.BaseURL, "/"),
			Auth: func(r *http.Request) {
				if token != "" {
					r.Header.Set("PRIVATE-TOKEN", token)
				}
			},
			MaxRetries: opts.MaxRetries,
			Backoff:    opts.Backoff,
			Sleep:      opts.Sleep,
		},
	}, nil
}

// Tracker returns the issues side.
func (c *Client) Tracker() *Tracker { return &Tracker{c: c} }

// CodeHost returns the merge request side.
func (c *Client) CodeHost() *CodeHost { return &CodeHost{c: c} }

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

func (c *Client) path(format string, args ...any) string {
	return "/projects/" + c.project + fmt.Sprintf(format, args...)
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) (*httpx.Response, error) {
	resp, err := c.http.Do(ctx, method, path, in, out)
	if err != nil {
		return resp, fmt.Errorf("gitlab: %w", err)
	}
	return resp, nil
}

// nextPage follows the Link header, falling back to X-Next-Page.
func nextPage(resp *httpx.Response, current string) string {
	if l := httpx.NextLink(resp.Header); l != "" {
		return l
	}
	np := strings.TrimSpace(resp.Header.Get("X-Next-Page"))
	if np == "" {
		return ""
	}
	u, err := url.Parse(current)
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("page", np)
	u.RawQuery = q.Encode()
	return u.String()
}

// ErrBadRef is returned for a reference that is not "#<iid>".
var ErrBadRef = errors.New("gitlab: reference is not an issue iid")

// ParseRef accepts "#17", "17", "group/name#17" or an issue URL.
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

// VerifyToken checks the X-Gitlab-Token header of a webhook in constant time.
// An empty secret never verifies.
func VerifyToken(secret, header string) bool {
	if secret == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(secret), []byte(header)) == 1
}
