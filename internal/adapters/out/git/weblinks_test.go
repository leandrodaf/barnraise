package git

import "testing"

func TestRepoWeb(t *testing.T) {
	for _, c := range []struct{ remote, kind, want string }{
		{"git@github.com:alchemistfy/one-more-bastion.git", "", "https://github.com/alchemistfy/one-more-bastion"},
		{"https://github.com/o/r.git", "", "https://github.com/o/r"},
		{"ssh://git@gitlab.example.com:2222/g/sub/r.git", "", "https://gitlab.example.com/g/sub/r"},
		{"git@code.example.com:o/r.git", "gitlab", "https://code.example.com/o/r"},
		{"git@code.example.com:o/r.git", "", ""},
		{"/srv/repo.git", "", ""},
		{"", "github", ""},
	} {
		if got := ParseRepoWeb(c.remote, c.kind).Base; got != c.want {
			t.Errorf("%q %q: %q, want %q", c.remote, c.kind, got, c.want)
		}
	}
	gh := ParseRepoWeb("git@github.com:o/r.git", "")
	if got := gh.Branch("br/I-21-x y"); got != "https://github.com/o/r/tree/br/I-21-x%20y" {
		t.Error(got)
	}
	if got := gh.Compare("main", "br/I-21"); got != "https://github.com/o/r/compare/main...br/I-21" {
		t.Error(got)
	}
	gl := ParseRepoWeb("https://gitlab.com/g/r", "")
	if got := gl.Commit("abc"); got != "https://gitlab.com/g/r/-/commit/abc" {
		t.Error(got)
	}
	if (RepoWeb{}).Branch("x") != "" || gh.Branch("") != "" || gh.Compare("", "x") != "" {
		t.Error("empty links")
	}
}

// A remote with credentials in it never puts them in a link.
func TestRepoWebDropsCredentials(t *testing.T) {
	if got := ParseRepoWeb("https://bot:secret@github.com/o/r.git", "").Base; got != "https://github.com/o/r" {
		t.Fatal(got)
	}
}
