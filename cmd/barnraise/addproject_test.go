package main

import "testing"

func TestParseRepoSource(t *testing.T) {
	for in, want := range map[string]repoSource{
		"leandrodaf/barnraise":                                {URL: "https://github.com/leandrodaf/barnraise.git", Host: "github.com", Path: "leandrodaf/barnraise", Name: "barnraise"},
		"github.com/alchemistfy/one-more-bastion":             {URL: "https://github.com/alchemistfy/one-more-bastion.git", Host: "github.com", Path: "alchemistfy/one-more-bastion", Name: "one-more-bastion"},
		"https://github.com/a/b.git":                          {URL: "https://github.com/a/b.git", Host: "github.com", Path: "a/b", Name: "b"},
		"https://github.com/a/b/tree/main/src":                {URL: "https://github.com/a/b.git", Host: "github.com", Path: "a/b", Name: "b"},
		"  https://github.com/a/b/  ":                         {URL: "https://github.com/a/b.git", Host: "github.com", Path: "a/b", Name: "b"},
		"git@github.com:a/b.git":                              {URL: "git@github.com:a/b.git", Host: "github.com", Path: "a/b", Name: "b"},
		"ssh://git@gitlab.com/group/sub/app.git":              {URL: "ssh://git@gitlab.com/group/sub/app.git", Host: "gitlab.com", Path: "group/sub/app", Name: "app"},
		"https://gitlab.com/group/sub/app/-/merge_requests/3": {URL: "https://gitlab.com/group/sub/app.git", Host: "gitlab.com", Path: "group/sub/app", Name: "app"},
	} {
		got, err := parseRepoSource(in)
		if err != nil || got != want {
			t.Errorf("%q: %+v %v, want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "barnraise", "-oops/x", "a b/c", "https://github.com/onlyowner", "ftp:/x"} {
		if _, err := parseRepoSource(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestNoAccess(t *testing.T) {
	for _, m := range []string{
		"fatal: could not read Username for 'https://github.com': terminal prompts disabled",
		"remote: Repository not found.\nfatal: repository 'https://github.com/a/b.git/' not found",
		"git@github.com: Permission denied (publickey).",
		"fatal: unable to access '…': The requested URL returned error: 403",
	} {
		if !noAccess(m) {
			t.Errorf("%q is a missing credential", m)
		}
	}
	if noAccess("fatal: unable to access '…': Could not resolve host: github.com") {
		t.Error("a network error is not a missing credential")
	}
}

func TestSameRepo(t *testing.T) {
	src, _ := parseRepoSource("octocat/Hello-World")
	for _, o := range []string{"https://github.com/octocat/Hello-World.git", "git@github.com:octocat/hello-world.git", "https://x-access-token@github.com/octocat/Hello-World"} {
		if !sameRepo(o, src) {
			t.Errorf("%s is the same repository", o)
		}
	}
	if sameRepo("https://github.com/someone/Hello-World.git", src) {
		t.Error("another owner's repository of the same name is not the same")
	}
}
