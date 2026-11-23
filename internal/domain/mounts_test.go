package domain

import "testing"

func TestForbiddenMount(t *testing.T) {
	home := "/home/u"
	for _, p := range []string{
		"/var/run/docker.sock", "/run/docker.sock", "/run/user/1000/podman/podman.sock:ro", "/run/containerd/containerd.sock",
		"/", "/etc", "/home", "/home/u", "~", "/home/other",
		"~/.ssh", "/home/u/.ssh/id_ed25519", "~/.config", "~/.config/nobira", "~/.aws:ro", "~/.local/share/keyrings",
		"~/.local", "~/.docker/config.json",
	} {
		if ForbiddenMount(p, home) == "" {
			t.Errorf("%s must be refused", p)
		}
	}
	for _, p := range []string{"~/.cache/go-build", "/opt/android-sdk:ro", "/home/u/.gradle", "~/go/pkg/mod:ro", "/home/u/.local/share/pnpm"} {
		if why := ForbiddenMount(p, home); why != "" {
			t.Errorf("%s refused: %s", p, why)
		}
	}
	// Without the home (barnraise check), "~/" paths are checked as written.
	if ForbiddenMount("~/.ssh", "") == "" || ForbiddenMount("~/.cache", "") != "" {
		t.Fatal("check without home")
	}
}
