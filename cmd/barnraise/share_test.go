package main

import (
	"slices"
	"testing"
)

func TestCloneURLs(t *testing.T) {
	cases := map[string][]string{
		"git@github.com:alchemistfy/one-more-bastion.git": {"git@github.com:alchemistfy/one-more-bastion.git", "https://github.com/alchemistfy/one-more-bastion.git"},
		"ssh://git@gitlab.example:2222/team/app.git":      {"ssh://git@gitlab.example:2222/team/app.git", "https://gitlab.example/team/app.git"},
		"https://github.com/nobira-finance/nobira.git":    {"https://github.com/nobira-finance/nobira.git"},
		"/srv/git/repo.git":                               {"/srv/git/repo.git"},
	}
	for in, want := range cases {
		if got := cloneURLs(in); !slices.Equal(got, want) {
			t.Errorf("cloneURLs(%q) = %v, want %v", in, got, want)
		}
	}
}
