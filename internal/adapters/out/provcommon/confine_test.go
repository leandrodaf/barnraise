package provcommon

import (
	"errors"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// The confinement wraps the CLI, and sudo wraps the confinement: Landlock
// then holds for the account's own Linux user too.
func TestHostArgsConfineInsideSudo(t *testing.T) {
	env := MinimalEnv()
	env.Set("BARNRAISE_TOKEN", "t")
	wrap := &ports.Confine{Backend: "landlock", Wrap: func(name string, args []string) (string, []string, error) {
		return "/bin/barnraise", append([]string{"sandbox-exec", "--", name}, args...), nil
	}}
	name, args, err := HostArgs(ports.SessionSpec{Confine: wrap, RunAsUser: "agent1"}, env, "codex", []string{"exec"})
	got := name + " " + strings.Join(args, " ")
	if err != nil || !strings.HasPrefix(got, "sudo -n -u agent1") || !strings.HasSuffix(got, "-- /bin/barnraise sandbox-exec -- codex exec") {
		t.Fatalf("%s %v", got, err)
	}
	name, args, _ = HostArgs(ports.SessionSpec{}, env, "codex", []string{"exec"})
	if name != "codex" || len(args) != 1 {
		t.Fatalf("nothing to wrap: %s %v", name, args)
	}
	fail := &ports.Confine{Wrap: func(string, []string) (string, []string, error) { return "", nil, errors.New("no") }}
	if _, _, err := HostArgs(ports.SessionSpec{Confine: fail}, env, "codex", nil); err == nil {
		t.Fatal("a wrapper error stops the session")
	}
}
