package git

import (
	"net/url"
	"strings"

	"github.com/leandrodaf/barnraise/internal/ports"
)

var _ ports.RepoLinks = RepoWeb{}

// RepoWeb is a repository's web page on its host, to link the owner to a
// branch, a comparison, a commit, an issue or a pull request. Base is empty
// when the host is unknown: then there is nothing to link.
type RepoWeb struct {
	Base string `json:"base,omitempty"` // https://github.com/owner/name
	Kind string `json:"kind,omitempty"` // github or gitlab
}

// ParseRepoWeb turns a git remote URL (git@host:owner/name.git,
// ssh://git@host/owner/name, https://host/owner/name.git) into its web page.
// kind is the configured host type; empty guesses it from the host name.
func ParseRepoWeb(remote, kind string) RepoWeb {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return RepoWeb{}
	}
	var host, path string
	if u, err := url.Parse(remote); err == nil && u.Host != "" {
		host, path = u.Hostname(), u.Path
	} else if at := strings.Index(remote, "@"); at >= 0 && strings.Contains(remote[at:], ":") {
		host, path, _ = strings.Cut(remote[at+1:], ":")
	} else {
		return RepoWeb{}
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if host == "" || !strings.Contains(path, "/") {
		return RepoWeb{}
	}
	if kind != "github" && kind != "gitlab" {
		switch {
		case strings.Contains(host, "github"):
			kind = "github"
		case strings.Contains(host, "gitlab"):
			kind = "gitlab"
		default:
			return RepoWeb{}
		}
	}
	return RepoWeb{Base: "https://" + host + "/" + path, Kind: kind}
}

func (r RepoWeb) page(github, gitlab string) string {
	if r.Base == "" {
		return ""
	}
	if r.Kind == "gitlab" {
		return r.Base + "/-/" + gitlab
	}
	return r.Base + "/" + github
}

// Branch is the branch's page.
func (r RepoWeb) Branch(branch string) string {
	if branch == "" {
		return ""
	}
	return r.page("tree/"+escapeRef(branch), "tree/"+escapeRef(branch))
}

// Compare is the branch's changes against base.
func (r RepoWeb) Compare(base, branch string) string {
	if base == "" || branch == "" {
		return ""
	}
	return r.page("compare/"+escapeRef(base)+"..."+escapeRef(branch), "compare/"+escapeRef(base)+"..."+escapeRef(branch))
}

// Commit is a commit's page.
func (r RepoWeb) Commit(sha string) string {
	if sha == "" {
		return ""
	}
	return r.page("commit/"+url.PathEscape(sha), "commit/"+url.PathEscape(sha))
}

// escapeRef escapes a branch name for a URL path, keeping its slashes.
func escapeRef(ref string) string {
	parts := strings.Split(ref, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
