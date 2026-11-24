package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
)

// pause --all and resume --all reach every project of the daemon; without
// --all only the project of the current directory changes.
func TestPauseAndResumeAllProjects(t *testing.T) {
	h := startHarness(t, demoYAML)
	ctx := context.Background()
	base := filepath.Dir(h.root)
	remote, root := filepath.Join(base, "second.git"), filepath.Join(base, "second")
	git(t, base, "init", "-q", "--bare", "-b", "main", remote)
	git(t, base, "clone", "-q", remote, root)
	git(t, root, "checkout", "-q", "-B", "main")
	os.WriteFile(filepath.Join(root, "barnraise.yaml"), []byte(strings.Replace(demoYAML, "project: demo", "project: second", 1)), 0o644)
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "init")
	git(t, root, "push", "-q", "origin", "main")
	if _, err := h.cl.RegisterProject(ctx, connect.NewRequest(&pb.RegisterProjectRequest{Root: root})); err != nil {
		t.Fatal(err)
	}
	paused := func(project string) (bool, string) {
		t.Helper()
		st, err := h.cl.Status(ctx, connect.NewRequest(&pb.ProjectRef{Project: project}))
		if err != nil {
			t.Fatal(err)
		}
		return st.Msg.Paused, st.Msg.PausedWhy
	}

	out, code := cliRun(t, h.root, "pause", "--all", "night", "shift")
	if code != 0 || !strings.Contains(out, "demo") || !strings.Contains(out, "second") {
		t.Fatalf("pause --all: %d %s", code, out)
	}
	for _, name := range []string{"demo", "second"} {
		if on, why := paused(name); !on || why != "night shift" {
			t.Fatalf("%s after pause --all: paused %v (%q)", name, on, why)
		}
	}
	// One project, from its directory: the other stays paused.
	if out, code := cliRun(t, h.root, "resume"); code != 0 {
		t.Fatalf("resume: %d %s", code, out)
	}
	if on, _ := paused("demo"); on {
		t.Fatal("demo still paused after resume")
	}
	if on, _ := paused("second"); !on {
		t.Fatal("resume without --all reached another project")
	}
	out, code = cliRun(t, h.root, "resume", "--all")
	if code != 0 || !strings.Contains(out, "second") {
		t.Fatalf("resume --all: %d %s", code, out)
	}
	for _, name := range []string{"demo", "second"} {
		if on, _ := paused(name); on {
			t.Fatalf("%s still paused after resume --all", name)
		}
	}
}
