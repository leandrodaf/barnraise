package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func TestCommandProgram(t *testing.T) {
	for line, want := range map[string]string{
		"COMPOSE_PROJECT_NAME=nobira make verificar-mudou": "make",
		"MOBILE_ENTREGA=1 A=b ./scripts/x.sh":              "./scripts/x.sh",
		"golangci-lint run":                                "golangci-lint",
		"  ":                                               "",
	} {
		if got := commandProgram(line); got != want {
			t.Errorf("commandProgram(%q) = %q, want %q", line, got, want)
		}
	}
}

type fakeMachine struct {
	noRemote, noPush, gh bool
	tools                map[string]bool
	env                  map[string]string
	secrets              map[string]string
}

func (f fakeMachine) readiness() readiness {
	return readiness{
		run: func(_ context.Context, _, name string, args ...string) (string, error) {
			cmd := name + " " + strings.Join(args, " ")
			switch {
			case strings.HasPrefix(cmd, "git remote get-url"):
				if f.noRemote {
					return "error: No such remote", errors.New("exit 2")
				}
				return "git@github.com:o/r.git", nil
			case strings.HasPrefix(cmd, "git push --dry-run"):
				if f.noPush {
					return "\nERROR: Permission to o/r.git denied to someone.\nfatal: could not read", errors.New("exit 128")
				}
				return "Done", nil
			case cmd == "gh auth token":
				if f.gh {
					return "gho_x\n", nil
				}
				return "", errors.New("not logged in")
			}
			return "", errors.New("unexpected " + cmd)
		},
		lookPath: func(p string) (string, error) {
			if f.tools[p] {
				return "/usr/bin/" + p, nil
			}
			return "", errors.New("not found")
		},
		secret: func(k string) string { return f.secrets[k] },
		env:    func(k string) string { return f.env[k] },
	}
}

func nobiraLike() *domain.ProjectConfig {
	return &domain.ProjectConfig{
		Root:     "/repo",
		Tracker:  domain.TrackerDef{Type: "github", Token: "keychain:tracker/github"},
		CodeHost: domain.CodeHostDef{Type: "github", Remote: "origin"},
		Checks: map[string]domain.CheckDef{
			"gate":   {Run: "COMPOSE_PROJECT_NAME=nobira make verificar-mudou", RunsOn: "local"},
			"device": {Run: "MOBILE_ENTREGA=1 make verificar-mudou", RunsOn: "machine:emulators"},
			"lint":   {Run: "golangci-lint run", RunsOn: "local"},
			"ui":     {RunsOn: "github-actions", Workflow: "ui.yml"},
		},
	}
}

func TestReadinessReadyMachine(t *testing.T) {
	m := fakeMachine{gh: true, tools: map[string]bool{"make": true, "golangci-lint": true}}
	if got := m.readiness().problems(context.Background(), nobiraLike()); len(got) != 0 {
		t.Fatalf("ready machine has problems: %v", got)
	}
}

// The second machine of the Nobira cluster: no GitHub credential and a
// non-interactive PATH without ~/go/bin.
func TestReadinessMachineWithoutCredentialsOrTools(t *testing.T) {
	m := fakeMachine{noPush: true, tools: map[string]bool{"make": true}}
	got := m.readiness().problems(context.Background(), nobiraLike())
	if len(got) != 3 {
		t.Fatalf("problems = %d, want 3:\n%s", len(got), strings.Join(got, "\n"))
	}
	for i, want := range []string{"cannot push to the git remote \"origin\" (ERROR: Permission to o/r.git denied", "no GitHub token", "golangci-lint is not on the PATH (check lint)"} {
		if !strings.Contains(got[i], want) {
			t.Errorf("problem %d = %q, want it to say %q", i, got[i], want)
		}
	}
}

func TestReadinessTokenSources(t *testing.T) {
	cfg := nobiraLike()
	base := fakeMachine{tools: map[string]bool{"make": true, "golangci-lint": true}}
	for name, m := range map[string]fakeMachine{
		"keychain": {tools: base.tools, secrets: map[string]string{"tracker/github": "t"}},
		"env":      {tools: base.tools, env: map[string]string{"GITHUB_TOKEN": "t"}},
		"gh":       {tools: base.tools, gh: true},
	} {
		if got := m.readiness().problems(context.Background(), cfg); len(got) != 0 {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestReadinessMissingRemote(t *testing.T) {
	m := fakeMachine{noRemote: true, gh: true, tools: map[string]bool{"make": true, "golangci-lint": true}}
	got := m.readiness().problems(context.Background(), nobiraLike())
	if len(got) != 1 || !strings.Contains(got[0], `the git remote "origin" does not exist`) {
		t.Fatalf("problems = %v", got)
	}
}

func TestUnitFileKeepsThePath(t *testing.T) {
	u := unitFile("/home/u/.local/bin/barnraise", "/home/u/go/bin:/home/u/.nvm/versions/node/v24/bin:/usr/bin")
	if !strings.Contains(u, `Environment="PATH=/home/u/go/bin:/home/u/.nvm/versions/node/v24/bin:/usr/bin"`) {
		t.Fatalf("unit without the PATH:\n%s", u)
	}
	if !strings.Contains(u, "ExecStart=/home/u/.local/bin/barnraise daemon") {
		t.Fatalf("unit without ExecStart:\n%s", u)
	}
	if u := unitFile("/b", `/a%b:/c"d`); !strings.Contains(u, `Environment="PATH=/a%%b:/c\"d"`) {
		t.Fatalf("specifiers and quotes not escaped:\n%s", u)
	}
	if u := unitFile("/b", ""); !strings.Contains(u, `PATH=/usr/local/bin:/usr/bin:/bin"`) {
		t.Fatalf("empty PATH:\n%s", u)
	}
}
