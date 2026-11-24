package main

import (
	"github.com/leandrodaf/barnraise/internal/domain"
)

// mainRefs are the refs a built-in check tries for main when it comes
// without its base commit: the remote-tracking main first (the git adapter
// mirrors a remote under its name, or under "barnraise" for a URL), then
// the local branch.
func mainRefs(cfg *domain.ProjectConfig) []string {
	main := cfg.MainBranch
	if main == "" {
		main = "main"
	}
	var out []string
	if r := cfg.CodeHost.Remote; r != "" {
		out = append(out, "refs/remotes/"+r+"/"+main)
	}
	return append(out, "refs/remotes/barnraise/"+main, "refs/heads/"+main)
}
