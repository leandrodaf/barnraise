package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// VerifySignature checks an X-Hub-Signature-256 header ("sha256=<hex>")
// against the body in constant time. An empty secret never verifies.
func VerifySignature(secret, body []byte, header string) bool {
	if len(secret) == 0 {
		return false
	}
	hexSig, ok := strings.CutPrefix(strings.TrimSpace(header), "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(hexSig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// Event is a webhook delivery normalized for the daemon. Text that people
// wrote (titles, bodies, comments) is sanitized with domain.SanitizeExternal.
type Event struct {
	Type   string `json:"type"` // push, check_run, workflow_run, issue_comment, issues, ping
	Action string `json:"action,omitempty"`
	Repo   string `json:"repo,omitempty"` // owner/name

	// push
	Ref    string   `json:"ref,omitempty"`    // refs/heads/main
	Branch string   `json:"branch,omitempty"` // main (push) or head branch (check_run, workflow_run)
	Before string   `json:"before,omitempty"`
	SHA    string   `json:"sha,omitempty"` // after (push) or head sha (runs)
	Files  []string `json:"files,omitempty"`

	// check_run, workflow_run
	Name       string `json:"name,omitempty"`
	Status     string `json:"status,omitempty"`
	Conclusion string `json:"conclusion,omitempty"`
	URL        string `json:"url,omitempty"`

	// issues, issue_comment
	Number  int       `json:"number,omitempty"`
	Title   string    `json:"title,omitempty"`
	Body    string    `json:"body,omitempty"`
	Author  string    `json:"author,omitempty"`
	Labels  []string  `json:"labels,omitempty"`
	IsPR    bool      `json:"is_pr,omitempty"`
	State   string    `json:"state,omitempty"`
	Updated time.Time `json:"updated,omitzero"`
}

// ErrUnsupportedEvent is returned for event types the daemon does not use.
var ErrUnsupportedEvent = errors.New("github: unsupported webhook event")

type whRepo struct {
	FullName string `json:"full_name"`
}

type whIssue struct {
	Number      int             `json:"number"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	State       string          `json:"state"`
	HTMLURL     string          `json:"html_url"`
	User        ghUser          `json:"user"`
	Labels      []ghLabel       `json:"labels"`
	UpdatedAt   time.Time       `json:"updated_at"`
	PullRequest json.RawMessage `json:"pull_request"`
}

// ParseEvent normalizes a webhook body; eventType is the X-GitHub-Event header.
func ParseEvent(eventType string, body []byte) (Event, error) {
	ev := Event{Type: eventType}
	switch eventType {
	case "ping":
		var p struct {
			Repository whRepo `json:"repository"`
		}
		_ = json.Unmarshal(body, &p)
		ev.Repo = p.Repository.FullName
		return ev, nil
	case "push":
		var p struct {
			Ref        string `json:"ref"`
			Before     string `json:"before"`
			After      string `json:"after"`
			Repository whRepo `json:"repository"`
			Pusher     struct {
				Name string `json:"name"`
			} `json:"pusher"`
			Sender  ghUser `json:"sender"`
			Commits []struct {
				Added    []string `json:"added"`
				Removed  []string `json:"removed"`
				Modified []string `json:"modified"`
			} `json:"commits"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return ev, fmt.Errorf("github: parse push: %w", err)
		}
		ev.Repo, ev.Ref, ev.Before, ev.SHA = p.Repository.FullName, p.Ref, p.Before, p.After
		ev.Branch = strings.TrimPrefix(p.Ref, "refs/heads/")
		ev.Author = p.Sender.Login
		if ev.Author == "" {
			ev.Author = p.Pusher.Name
		}
		seen := map[string]bool{}
		for _, c := range p.Commits {
			for _, list := range [][]string{c.Added, c.Removed, c.Modified} {
				for _, f := range list {
					if !seen[f] {
						seen[f] = true
						ev.Files = append(ev.Files, f)
					}
				}
			}
		}
		sort.Strings(ev.Files)
		return ev, nil
	case "check_run":
		var p struct {
			Action     string `json:"action"`
			Repository whRepo `json:"repository"`
			CheckRun   struct {
				Name       string `json:"name"`
				HeadSHA    string `json:"head_sha"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				HTMLURL    string `json:"html_url"`
				CheckSuite struct {
					HeadBranch string `json:"head_branch"`
				} `json:"check_suite"`
			} `json:"check_run"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return ev, fmt.Errorf("github: parse check_run: %w", err)
		}
		cr := p.CheckRun
		ev.Action, ev.Repo, ev.Name, ev.SHA = p.Action, p.Repository.FullName, cr.Name, cr.HeadSHA
		ev.Status, ev.Conclusion, ev.URL, ev.Branch = cr.Status, cr.Conclusion, cr.HTMLURL, cr.CheckSuite.HeadBranch
		return ev, nil
	case "workflow_run":
		var p struct {
			Action      string `json:"action"`
			Repository  whRepo `json:"repository"`
			WorkflowRun struct {
				Name       string `json:"name"`
				HeadBranch string `json:"head_branch"`
				HeadSHA    string `json:"head_sha"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				HTMLURL    string `json:"html_url"`
				Path       string `json:"path"`
			} `json:"workflow_run"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return ev, fmt.Errorf("github: parse workflow_run: %w", err)
		}
		wr := p.WorkflowRun
		ev.Action, ev.Repo, ev.Name, ev.Branch, ev.SHA = p.Action, p.Repository.FullName, wr.Name, wr.HeadBranch, wr.HeadSHA
		ev.Status, ev.Conclusion, ev.URL = wr.Status, wr.Conclusion, wr.HTMLURL
		return ev, nil
	case "issue_comment":
		var p struct {
			Action     string  `json:"action"`
			Repository whRepo  `json:"repository"`
			Issue      whIssue `json:"issue"`
			Comment    struct {
				Body      string    `json:"body"`
				User      ghUser    `json:"user"`
				HTMLURL   string    `json:"html_url"`
				UpdatedAt time.Time `json:"updated_at"`
			} `json:"comment"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return ev, fmt.Errorf("github: parse issue_comment: %w", err)
		}
		ev.Action, ev.Repo, ev.Number = p.Action, p.Repository.FullName, p.Issue.Number
		ev.Title = domain.SanitizeExternal(p.Issue.Title)
		ev.Body = domain.SanitizeExternal(p.Comment.Body)
		ev.Author, ev.URL, ev.Updated = p.Comment.User.Login, p.Comment.HTMLURL, p.Comment.UpdatedAt
		ev.IsPR = isSet(p.Issue.PullRequest)
		ev.State = p.Issue.State
		return ev, nil
	case "issues":
		var p struct {
			Action     string  `json:"action"`
			Repository whRepo  `json:"repository"`
			Issue      whIssue `json:"issue"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return ev, fmt.Errorf("github: parse issues: %w", err)
		}
		is := p.Issue
		ev.Action, ev.Repo, ev.Number = p.Action, p.Repository.FullName, is.Number
		ev.Title, ev.Body = domain.SanitizeExternal(is.Title), domain.SanitizeExternal(is.Body)
		ev.Author, ev.URL, ev.State, ev.Updated = is.User.Login, is.HTMLURL, is.State, is.UpdatedAt
		for _, l := range is.Labels {
			ev.Labels = append(ev.Labels, l.Name)
		}
		ev.IsPR = isSet(is.PullRequest)
		return ev, nil
	}
	return ev, fmt.Errorf("%w: %q", ErrUnsupportedEvent, eventType)
}

func isSet(raw json.RawMessage) bool { return len(raw) > 0 && string(raw) != "null" }
