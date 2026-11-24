package domain

import (
	"slices"
	"testing"
)

func TestParseAllowEntry(t *testing.T) {
	cases := []struct {
		in     string
		rule   bool
		want   HostRule
		failed bool
	}{
		{in: "example.com", want: HostRule{Host: "example.com"}},
		{in: " *.Example.COM. ", want: HostRule{Host: "*.example.com"}},
		{in: "GET github.com/owner/repo/**", rule: true, want: HostRule{Methods: []string{"GET"}, Host: "github.com", Path: "owner/repo/**"}},
		{in: "post,put api.github.com/repos/{project-repo}/**", rule: true, want: HostRule{Methods: []string{"POST", "PUT"}, Host: "api.github.com", Path: "repos/{project-repo}/**"}},
		{in: "github.com/owner/", rule: true, want: HostRule{Host: "github.com", Path: "owner"}},
		{in: "* https://example.org", rule: true, want: HostRule{Host: "example.org", Path: "**"}},
		{in: "DELETE example.org/", rule: true, want: HostRule{Methods: []string{"DELETE"}, Host: "example.org", Path: "**"}},
		{in: "FETCH example.org/x", failed: true},
		{in: "", failed: true},
		{in: "GET /x", failed: true},
	}
	for _, c := range cases {
		r, isRule, err := ParseAllowEntry(c.in)
		if c.failed {
			if err == nil {
				t.Errorf("%q: want an error", c.in)
			}
			continue
		}
		if err != nil || isRule != c.rule || r.Host != c.want.Host || r.Path != c.want.Path || !slices.Equal(r.Methods, c.want.Methods) {
			t.Errorf("%q: got %+v rule=%v err=%v", c.in, r, isRule, err)
		}
	}
}

func TestSplitAllowHostsAndNames(t *testing.T) {
	entries := []string{"example.com", "GET github.com/o/r/**", "github.com", "POST api.github.com/repos/o/r/**"}
	hosts, rules, err := SplitAllowHosts(entries)
	if err != nil || !slices.Equal(hosts, []string{"example.com", "github.com"}) || len(rules) != 2 {
		t.Fatal(hosts, rules, err)
	}
	if _, _, err := SplitAllowHosts([]string{"BREW x.com/y"}); err == nil {
		t.Fatal("bad method accepted")
	}
	names := AllowHostNames(append(entries, "BREW bad/x", ""))
	if !slices.Equal(names, []string{"example.com", "github.com", "api.github.com"}) {
		t.Fatal(names)
	}
}

func TestHostRuleMatch(t *testing.T) {
	r, _, _ := ParseAllowEntry("POST,PUT *.github.com/repos/o/r/**")
	if !r.MatchesHost("api.github.com") || !r.MatchesHost("GITHUB.COM.") || r.MatchesHost("evilgithub.com") {
		t.Fatal("host")
	}
	if !r.MatchesMethod("post") || r.MatchesMethod("GET") {
		t.Fatal("method")
	}
	if !r.MatchesPath("/repos/o/r") || !r.MatchesPath("/repos/o/r/pulls/1") || r.MatchesPath("/repos/o/other/pulls") {
		t.Fatal("path")
	}
	exact, _, _ := ParseAllowEntry("GET example.com/a")
	if !exact.MatchesHost("example.com") || exact.MatchesHost("x.example.com") || !exact.MatchesMethod("GET") {
		t.Fatal("exact host")
	}
	anyM, _, _ := ParseAllowEntry("example.com/x")
	if !anyM.MatchesMethod("DELETE") || anyM.String() != "* example.com/x" || r.String() != "POST,PUT *.github.com/repos/o/r/**" {
		t.Fatal(anyM.String(), r.String())
	}
}

func TestMatchPathGlob(t *testing.T) {
	for _, c := range []struct {
		pat, p string
		want   bool
	}{
		{"**", "", true},
		{"**", "a/b/c", true},
		{"a/*", "a/b", true},
		{"a/*", "a/b/c", false},
		{"a/**/git-upload-pack", "a/x/y.git/git-upload-pack", true},
		{"**/git-upload-pack", "o/r.git/git-upload-pack", true},
		{"**/git-upload-pack", "o/r.git/git-receive-pack", false},
		{"*/*.git/info", "o/r.git/info", true},
		{"a", "", false},
		{"", "", true},
		{"[", "x", false},
	} {
		if got := MatchPathGlob(c.pat, c.p); got != c.want {
			t.Errorf("%q ~ %q: %v", c.pat, c.p, got)
		}
	}
}

func TestExpandProjectRepo(t *testing.T) {
	if s, ok := ExpandProjectRepo("POST api.github.com/repos/{project-repo}/**", "/o/r/"); !ok || s != "POST api.github.com/repos/o/r/**" {
		t.Fatal(s, ok)
	}
	if _, ok := ExpandProjectRepo("GET x.com/{project-repo}", ""); ok {
		t.Fatal("expanded without a repository")
	}
	if s, ok := ExpandProjectRepo("x.com", ""); !ok || s != "x.com" {
		t.Fatal(s)
	}
}

func TestEgressRules(t *testing.T) {
	e, err := CompileEgress([]string{"*.npmjs.org", "npmjs.org", "GET only.example/docs/**", "POST github.com/o/r/**", "github.com"})
	if err != nil || len(e.Hosts) != 2 {
		t.Fatal(e, err)
	}
	if _, err := CompileEgress([]string{"BREW x/y"}); err == nil {
		t.Fatal("bad entry compiled")
	}
	for _, c := range []struct {
		method, host, p string
		want            bool
		rule            string
	}{
		{"GET", "registry.npmjs.org:443", "/x", true, "allow-host"},
		{"GET", "only.example", "/docs/a", true, "GET only.example/docs/**"},
		{"GET", "only.example", "/admin", false, "path-rule"},
		{"POST", "only.example", "/docs/a", false, "not-allowed"},
		{"POST", "GitHub.com.", "/o/r/x", true, "POST github.com/o/r/**"},
		{"POST", "github.com", "/e/x", false, "path-rule"},
		{"GET", "github.com", "/e/x", true, "allow-host"},
		{"GET", "evil.test", "/", false, "not-allowed"},
		{"GET", "[::1]:80", "/", false, "not-allowed"},
	} {
		ok, rule := e.Allow(c.method, c.host, c.p)
		if ok != c.want || rule != c.rule {
			t.Errorf("%s %s%s: %v %q", c.method, c.host, c.p, ok, rule)
		}
	}
	if !e.HostAllowed("only.example:443") || e.HostAllowed("other.example") || !e.HasRules("github.com") || e.HasRules("npmjs.org") {
		t.Fatal("host checks")
	}
	for in, want := range map[string]string{"A.b:443": "a.b", "x.": "x", "[::1]:80": "::1", "::1": "::1", "[::1]": "::1"} {
		if got := HostOnly(in); got != want {
			t.Errorf("HostOnly(%q) = %q", in, got)
		}
	}
}

func TestRepoScopeRules(t *testing.T) {
	if RepoScopeRules("github", "", "") != nil || RepoScopeRules("gitea", "x", "o/r") != nil {
		t.Fatal("rules without a known host or repository")
	}
	gh := RepoScopeRules("github", "", "o/r")
	er, err := CompileEgress(append([]string{"github.com", "*.github.com"}, gh...))
	if err != nil {
		t.Fatal(err)
	}
	allowed := func(method, host, p string) bool { ok, _ := er.Allow(method, host, p); return ok }
	for _, c := range []struct {
		method, host, p string
		want            bool
	}{
		{"POST", "github.com", "/o/r.git/git-receive-pack", true},
		{"POST", "github.com", "/evil/drop.git/git-receive-pack", false},
		{"POST", "github.com", "/other/dep.git/git-upload-pack", true},
		{"GET", "github.com", "/other/dep.git/info/refs", true},
		{"POST", "api.github.com", "/repos/o/r/pulls", true},
		{"POST", "api.github.com", "/repos/evil/x/issues", false},
		{"POST", "api.github.com", "/gists", false},
		{"POST", "api.github.com", "/graphql", true},
		{"GET", "api.github.com", "/repos/any/thing", true},
	} {
		if got := allowed(c.method, c.host, c.p); got != c.want {
			t.Errorf("%s %s%s: %v", c.method, c.host, c.p, got)
		}
	}
	ghe := RepoScopeRules("github", "git.corp", "o/r")
	if !slices.Contains(ghe, "POST,PUT,PATCH,DELETE git.corp/api/v3/repos/o/r/**") {
		t.Fatal(ghe)
	}
	gl := RepoScopeRules("gitlab", "", "g/sub/r")
	if !slices.Contains(gl, "POST,PUT,PATCH,DELETE gitlab.com/api/v4/projects/g/sub/r/**") || len(RepoScopeRules("gitlab", "gl.corp", "g/r")) != 5 {
		t.Fatal(gl)
	}
}
