package provcommon

import (
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/ports"
)

func TestContainerArgs(t *testing.T) {
	env := &Env{}
	env.Set("PATH", "/host/bin")
	env.Set("HOME", "/home/owner")
	env.Set("BARNRAISE_TOKEN", "brs_secret")
	env.Set("CLAUDE_CONFIG_DIR", "/data/accounts/claude-1")
	c := &ports.ContainerSpec{Image: "agent:1", Network: "bridge", User: "1000:1000", Home: "/w/I-1/.barnraise-session/home",
		Mounts: []string{"/w/I-1", "/repo/.git", "/usr/local/bin/barnraise:ro"}, Extra: []string{"--device", "/dev/dri"}}
	name, args := ContainerArgs(c, env, "/w/I-1", "/home/owner/.local/bin/claude", []string{"-p", "--model", "haiku"})
	got := strings.Join(args, " ")
	if name != "docker" {
		t.Fatalf("engine %s", name)
	}
	for _, want := range []string{"run --rm -i --init --workdir /w/I-1", "--network bridge", "--user 1000:1000",
		"--volume /w/I-1:/w/I-1 ", "--volume /repo/.git:/repo/.git ", "--volume /usr/local/bin/barnraise:/usr/local/bin/barnraise:ro",
		"--device /dev/dri", "--env HOME=/w/I-1/.barnraise-session/home", "--env BARNRAISE_TOKEN ", "--env CLAUDE_CONFIG_DIR ",
		"agent:1 claude -p --model haiku"} {
		if !strings.Contains(got, want) {
			t.Errorf("args lack %q:\n%s", want, got)
		}
	}
	// Values never reach the command line (it shows up in `ps`), and the host's PATH and HOME stay out.
	for _, never := range []string{"brs_secret", "--env PATH", "/host/bin", "HOME=/home/owner", "/home/owner/.local/bin/claude"} {
		if strings.Contains(got, never) {
			t.Errorf("args must not contain %q:\n%s", never, got)
		}
	}
	if name, _ := ContainerArgs(&ports.ContainerSpec{Engine: "podman", Image: "x"}, &Env{}, "/w", "codex", nil); name != "podman" {
		t.Fatal(name)
	}
}
