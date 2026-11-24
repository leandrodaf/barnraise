package main

import (
	"context"
	"os/exec"
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

func TestHTTPSHostPath(t *testing.T) {
	for in, want := range map[string][2]string{
		"https://github.com/alchemistfy/one-more-bastion.git": {"github.com", "alchemistfy/one-more-bastion.git"},
		"https://user@gitlab.example/team/app.git":            {"gitlab.example", "team/app.git"},
	} {
		h, p, ok := httpsHostPath(in)
		if !ok || h != want[0] || p != want[1] {
			t.Errorf("%s: %q %q %v", in, h, p, ok)
		}
	}
	if _, _, ok := httpsHostPath("git@github.com:a/b.git"); ok {
		t.Error("an SSH address split as HTTPS")
	}
}

// The repository's own configuration makes git ask barnraise alone for the
// host's credentials, with the path so the token of this repository is used.
func TestCredentialConfig(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	args := credentialConfig("github.com", "/opt/barnraise")
	for range 2 { // twice: the same configuration, not twice the helper
		if err := setCredentialConfig(context.Background(), dir, args); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command("git", "-C", dir, "config", "--local", "--get-all", "credential.https://github.com.helper").Output()
	if err != nil || string(out) != "\n!/opt/barnraise git-credential\n" {
		t.Fatalf("helpers %q %v", out, err)
	}
	if out, _ := exec.Command("git", "-C", dir, "config", "--local", "credential.https://github.com.useHttpPath").Output(); string(out) != "true\n" {
		t.Fatalf("useHttpPath %q", out)
	}
}
