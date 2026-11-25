package main

import (
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// A machine the project was shared with pushes with the repository's token
// from its keychain; its issues and pull requests use the same token, not a
// `gh` login that may not reach the repository.
func TestGithubTokenUsesTheRepositorysSharedToken(t *testing.T) {
	d := pluginDaemon(t)
	old := ghAuthToken
	t.Cleanup(func() { ghAuthToken = old })
	ghAuthToken = func() string { return "gh-login" }
	env := map[string]string{}
	resolve := func(ref, name string) string {
		if ref == "keychain:tracker" {
			return "named"
		}
		return env[name]
	}
	const repo = "alchemistfy/one-more-bastion"
	if got := d.githubToken(resolve, "", repo); got != "gh-login" {
		t.Fatalf("nothing else: %q", got)
	}
	env["GITHUB_TOKEN"] = "env"
	if got := d.githubToken(resolve, "", repo); got != "env" {
		t.Fatalf("env: %q", got)
	}
	if err := d.secrets.Set(domain.GitTokenSecret("github.com", repo), "shared"); err != nil {
		t.Fatal(err)
	}
	if got := d.githubToken(resolve, "", repo); got != "shared" {
		t.Fatalf("the repository's shared token comes before the environment: %q", got)
	}
	if got := d.githubToken(resolve, "", "someone/else"); got != "env" {
		t.Fatalf("another repository's token is never used: %q", got)
	}
	if got := d.githubToken(resolve, "keychain:tracker", repo); got != "named" {
		t.Fatalf("the token barnraise.yaml names wins: %q", got)
	}
}
