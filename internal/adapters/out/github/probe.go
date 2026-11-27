package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Permission is one permission of a fine-grained token Barnraise needs.
type Permission struct {
	Name   string `json:"name"`   // the query parameter of GitHub's token form
	Access string `json:"access"` // read or write
	Why    string `json:"why"`
}

// TokenPermissions are every permission Barnraise uses on a repository:
// without one of them some step fails later, often with no clear trace.
var TokenPermissions = []Permission{
	{"metadata", "read", "read the repository"},
	{"contents", "write", "clone, push the item branches and move main"},
	{"pull_requests", "write", "open, update and merge the pull requests"},
	{"issues", "write", "create and update the items' issues"},
	{"statuses", "write", "set Barnraise's checks and read the ones other services set"},
	{"actions", "write", "read the repository's CI (GitHub Actions) and start workflows"},
	{"workflows", "write", "push changes to .github/workflows"},
}

// projectsPermission is needed only when the tracker uses a GitHub Project board.
var projectsPermission = Permission{"organization_projects", "write", "move the items on the GitHub Project board"}

// Permissions lists what a token for this project needs.
func Permissions(projectBoard bool) []Permission {
	out := append([]Permission(nil), TokenPermissions...)
	if projectBoard {
		out = append(out, projectsPermission)
	}
	return out
}

// TemplateURL opens GitHub's form for a new fine-grained token with the
// owner, the name and every permission already filled in: the person only
// picks the repository and presses Generate.
func TemplateURL(repo string, projectBoard bool) string {
	owner, name, _ := strings.Cut(repo, "/")
	q := url.Values{}
	q.Set("name", truncateRunes("Barnraise "+name, 40))
	q.Set("description", "Barnraise: branches, pull requests, issues, checks and CI of "+repo+". Pick only this repository.")
	q.Set("target_name", owner)
	q.Set("expires_in", "366")
	for _, p := range Permissions(projectBoard) {
		if p.Name == "metadata" {
			continue // always granted
		}
		q.Set(p.Name, p.Access)
	}
	return "https://github.com/settings/personal-access-tokens/new?" + q.Encode()
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// Check is the result of testing one permission.
type Check struct {
	Name     string `json:"name"`
	Access   string `json:"access"`
	Why      string `json:"why"`
	OK       bool   `json:"ok"`
	Testable bool   `json:"testable"`
	Detail   string `json:"detail,omitempty"`
}

// ProbeOptions say which repository to test a token on.
type ProbeOptions struct {
	Token        string
	Repo         string // owner/name
	ProjectBoard int    // the tracker's GitHub Project number, 0 when none
	APIURL       string // default https://api.github.com
	WebURL       string // default https://github.com
	HTTP         *http.Client
}

// Probe tests each permission with a call that changes nothing: a write
// endpoint answers 422 (the request is invalid) to a token that may write,
// and 403 or 404 to one that may not. Only workflows cannot be tested
// without a real push.
func Probe(ctx context.Context, o ProbeOptions) ([]Check, Summary) {
	if o.APIURL == "" {
		o.APIURL = "https://api.github.com"
	}
	if o.WebURL == "" {
		o.WebURL = "https://github.com"
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	pr := prober{o: o}
	var sum Summary
	checks := make([]Check, 0, len(TokenPermissions)+1)
	add := func(p Permission, ok bool, testable bool, detail string) {
		checks = append(checks, Check{Name: p.Name, Access: p.Access, Why: p.Why, OK: ok, Testable: testable, Detail: detail})
	}
	perm := map[string]Permission{}
	for _, p := range Permissions(o.ProjectBoard > 0) {
		perm[p.Name] = p
	}

	// metadata: the repository itself, and what kind of token this is.
	var repo struct {
		DefaultBranch string `json:"default_branch"`
	}
	st, h, err := pr.api(ctx, http.MethodGet, "/repos/"+o.Repo, nil, &repo)
	sum.Kind = tokenKind(o.Token)
	if h != nil {
		sum.Expires = h.Get("github-authentication-token-expiration")
		sum.Scopes = h.Get("X-OAuth-Scopes")
	}
	if st != http.StatusOK {
		add(perm["metadata"], false, true, describe(st, err, "the token cannot see "+o.Repo+": pick this repository when creating it"))
		for _, p := range Permissions(o.ProjectBoard > 0)[1:] {
			add(p, false, p.Name != "workflows", "not tested: the token cannot see the repository")
		}
		return checks, sum
	}
	add(perm["metadata"], true, true, "")

	// contents: git's push endpoint answers only to a token that may push.
	st, err = pr.gitReceivePack(ctx)
	add(perm["contents"], st == http.StatusOK, true, describe(st, err, "git push is refused"))

	st, _, err = pr.api(ctx, http.MethodPost, "/repos/"+o.Repo+"/pulls", map[string]any{}, nil)
	add(perm["pull_requests"], st == http.StatusUnprocessableEntity, true, describe(st, err, "pull requests cannot be opened"))

	st, _, err = pr.api(ctx, http.MethodPost, "/repos/"+o.Repo+"/issues", map[string]any{}, nil)
	add(perm["issues"], st == http.StatusUnprocessableEntity, true, describe(st, err, "issues cannot be created"))

	var br struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	sha := strings.Repeat("0", 40)
	if repo.DefaultBranch != "" {
		if s, _, _ := pr.api(ctx, http.MethodGet, "/repos/"+o.Repo+"/branches/"+url.PathEscape(repo.DefaultBranch), nil, &br); s == http.StatusOK && br.Commit.SHA != "" {
			sha = br.Commit.SHA
		}
	}
	st, _, err = pr.api(ctx, http.MethodPost, "/repos/"+o.Repo+"/statuses/"+sha, map[string]any{"state": "not-a-state"}, nil)
	add(perm["statuses"], st == http.StatusUnprocessableEntity, true, describe(st, err, "commit statuses cannot be set"))

	st, _, err = pr.api(ctx, http.MethodGet, "/repos/"+o.Repo+"/actions/runs?per_page=1", nil, nil)
	add(perm["actions"], st == http.StatusOK, true, describe(st, err, "the CI's runs cannot be read"))

	if sum.Scopes != "" {
		// A classic or OAuth token says its scopes: workflow is one of them.
		ok := slices.Contains(strings.Split(strings.ReplaceAll(sum.Scopes, " ", ""), ","), "workflow")
		add(perm["workflows"], ok, true, map[bool]string{false: "the token lacks the workflow scope"}[ok])
	} else {
		add(perm["workflows"], true, false, "GitHub offers no test that does not push: it is asked for in the link")
	}

	if o.ProjectBoard > 0 {
		owner, _, _ := strings.Cut(o.Repo, "/")
		q := map[string]any{"query": `query($o:String!,$n:Int!){organization(login:$o){projectV2(number:$n){id}}}`, "variables": map[string]any{"o": owner, "n": o.ProjectBoard}}
		var res struct {
			Data struct {
				Organization struct {
					ProjectV2 *struct {
						ID string `json:"id"`
					} `json:"projectV2"`
				} `json:"organization"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		st, _, err = pr.api(ctx, http.MethodPost, "/graphql", q, &res)
		ok := st == http.StatusOK && len(res.Errors) == 0 && res.Data.Organization.ProjectV2 != nil
		detail := describe(st, err, "the Project board cannot be read")
		if st == http.StatusOK && !ok {
			detail = "the Project board cannot be read"
			if len(res.Errors) > 0 {
				detail += ": " + res.Errors[0].Message
			}
		}
		add(perm["organization_projects"], ok, true, detail)
	}
	return checks, sum
}

// Summary says what the token is, besides its permissions.
type Summary struct {
	Kind    string `json:"kind"`              // fine-grained, classic, oauth
	Expires string `json:"expires,omitempty"` // as GitHub says it
	Scopes  string `json:"scopes,omitempty"`  // classic and OAuth tokens
}

// AllOK reports whether every testable permission passed.
func AllOK(checks []Check) bool {
	for _, c := range checks {
		if c.Testable && !c.OK {
			return false
		}
	}
	return len(checks) > 0
}

func tokenKind(tok string) string {
	switch {
	case strings.HasPrefix(tok, "github_pat_"):
		return "fine-grained"
	case strings.HasPrefix(tok, "ghp_"):
		return "classic"
	case strings.HasPrefix(tok, "gho_"):
		return "oauth"
	}
	return "unknown"
}

// describe is "" for a pass, else why the call failed.
func describe(status int, err error, what string) string {
	switch {
	case err != nil:
		return what + ": " + err.Error()
	case status == http.StatusOK || status == http.StatusUnprocessableEntity:
		return ""
	case status == http.StatusUnauthorized:
		return what + ": the token is not valid (401)"
	default:
		return fmt.Sprintf("%s (%d)", what, status)
	}
}

type prober struct{ o ProbeOptions }

func (p prober) api(ctx context.Context, method, path string, body, out any) (int, http.Header, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.o.APIURL, "/")+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.o.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "barnraise")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := p.o.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if out != nil && res.StatusCode == http.StatusOK {
		_ = json.Unmarshal(b, out)
	}
	return res.StatusCode, res.Header, nil
}

func (p prober) gitReceivePack(ctx context.Context) (int, error) {
	u := strings.TrimRight(p.o.WebURL, "/") + "/" + p.o.Repo + ".git/info/refs?service=git-receive-pack"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.SetBasicAuth("x-access-token", p.o.Token)
	req.Header.Set("User-Agent", "git/2 barnraise")
	res, err := p.o.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
	_ = res.Body.Close()
	return res.StatusCode, nil
}
