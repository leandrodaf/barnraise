package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// fakeGitHub answers like GitHub to a fine-grained token with the given
// permissions: 403 to what it may not do, 422 to an invalid write it may.
func fakeGitHub(t *testing.T, may map[string]bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			if _, pw, ok := r.BasicAuth(); !ok || pw == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		allow := func(perm string, code int) {
			if !may[perm] {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			if code == http.StatusOK {
				_, _ = w.Write([]byte(`{"default_branch":"main","commit":{"sha":"abc"}}`))
			}
		}
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, ".git/info/refs"):
			allow("contents", http.StatusOK)
		case r.Method == http.MethodGet && p == "/repos/acme/app":
			allow("metadata", http.StatusOK)
		case strings.HasPrefix(p, "/repos/acme/app/branches/"):
			allow("metadata", http.StatusOK)
		case p == "/repos/acme/app/pulls":
			allow("pull_requests", http.StatusUnprocessableEntity)
		case p == "/repos/acme/app/issues":
			allow("issues", http.StatusUnprocessableEntity)
		case strings.HasPrefix(p, "/repos/acme/app/statuses/"):
			allow("statuses", http.StatusUnprocessableEntity)
		case p == "/repos/acme/app/actions/runs":
			allow("actions", http.StatusOK)
		default:
			t.Errorf("unexpected %s %s", r.Method, p)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func probeAgainst(t *testing.T, may map[string]bool) map[string]Check {
	srv := fakeGitHub(t, may)
	defer srv.Close()
	checks, sum := Probe(context.Background(), ProbeOptions{Token: "github_pat_x", Repo: "acme/app", APIURL: srv.URL, WebURL: srv.URL})
	if sum.Kind != "fine-grained" {
		t.Errorf("kind %q", sum.Kind)
	}
	out := map[string]Check{}
	for _, c := range checks {
		out[c.Name] = c
	}
	return out
}

func TestProbeEveryPermission(t *testing.T) {
	got := probeAgainst(t, map[string]bool{"metadata": true, "contents": true, "pull_requests": true, "issues": true, "statuses": true, "actions": true})
	var checks []Check
	for _, c := range got {
		checks = append(checks, c)
		if !c.OK {
			t.Errorf("%s failed: %s", c.Name, c.Detail)
		}
	}
	if !AllOK(checks) || len(checks) != len(TokenPermissions) {
		t.Fatalf("checks %+v", checks)
	}
	if got["workflows"].Testable {
		t.Error("workflows cannot be tested with a fine-grained token")
	}
}

// A token without statuses and actions (the one that left the merge queue
// waiting a night) is caught before it is installed.
func TestProbeMissingPermissions(t *testing.T) {
	got := probeAgainst(t, map[string]bool{"metadata": true, "contents": true, "pull_requests": true, "issues": true})
	for name, want := range map[string]bool{"contents": true, "pull_requests": true, "issues": true, "statuses": false, "actions": false} {
		if got[name].OK != want {
			t.Errorf("%s ok=%v, want %v (%s)", name, got[name].OK, want, got[name].Detail)
		}
	}
	if !strings.Contains(got["statuses"].Detail, "403") {
		t.Errorf("detail %q", got["statuses"].Detail)
	}
}

func TestProbeRepositoryNotPicked(t *testing.T) {
	got := probeAgainst(t, map[string]bool{})
	if got["metadata"].OK || got["contents"].OK || !strings.Contains(got["metadata"].Detail, "pick this repository") {
		t.Fatalf("%+v", got)
	}
}

func TestTemplateURL(t *testing.T) {
	u, err := url.Parse(TemplateURL("acme/app", true))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != "github.com" || u.Path != "/settings/personal-access-tokens/new" || q.Get("target_name") != "acme" {
		t.Fatalf("url %s", u)
	}
	for _, p := range Permissions(true) {
		if p.Name != "metadata" && q.Get(p.Name) != p.Access {
			t.Errorf("%s=%q, want %s", p.Name, q.Get(p.Name), p.Access)
		}
	}
	if len([]rune(q.Get("name"))) > 40 {
		t.Errorf("name %q is longer than GitHub takes", q.Get("name"))
	}
}
