package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
)

// A project registered paused takes no item until the owner resumes it: the
// owner adopts the work that already exists first.
func TestRegisterPausedStartsNothingUntilResume(t *testing.T) {
	h := startHarness(t, demoYAML)
	ctx := context.Background()

	base := filepath.Dir(h.root)
	remote := filepath.Join(base, "adopt.git")
	root := filepath.Join(base, "adopt")
	git(t, base, "init", "-q", "--bare", "-b", "main", remote)
	git(t, base, "clone", "-q", remote, root)
	git(t, root, "checkout", "-q", "-B", "main")
	yml := strings.Replace(demoYAML, "project: demo", "project: adopt", 1)
	os.WriteFile(filepath.Join(root, "README.md"), []byte("# adopt\n"), 0o644)
	os.WriteFile(filepath.Join(root, "barnraise.yaml"), []byte(yml), 0o644)
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "init")
	git(t, root, "push", "-q", "origin", "main")

	if _, err := h.cl.RegisterProject(ctx, connect.NewRequest(&pb.RegisterProjectRequest{Root: root, Paused: true})); err != nil {
		t.Fatal(err)
	}
	st, err := h.cl.Status(ctx, connect.NewRequest(&pb.ProjectRef{Project: "adopt"}))
	if err != nil {
		t.Fatal(err)
	}
	if !st.Msg.Paused || !strings.Contains(st.Msg.PausedWhy, "--paused") {
		t.Fatalf("status = paused %v (%q), want paused by --paused", st.Msg.Paused, st.Msg.PausedWhy)
	}

	res, err := h.cl.CreateItem(ctx, connect.NewRequest(&pb.CreateItemRequest{Project: "adopt", Title: "Adopted", Criteria: crit("WHEN x THE SYSTEM SHALL y")}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Item.Id
	time.Sleep(2 * time.Second)
	get := func() *pb.Item {
		r, err := h.cl.GetItem(ctx, connect.NewRequest(&pb.ItemRef{Project: "adopt", Id: id}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg.Item
	}
	if it := get(); it.Status != "ready" && it.Status != "draft" {
		t.Fatalf("paused project started %s: status %s, step %s", id, it.Status, it.Step)
	}

	// Registering again with paused keeps it paused (and does not fail).
	if _, err := h.cl.RegisterProject(ctx, connect.NewRequest(&pb.RegisterProjectRequest{Root: root, Paused: true})); err != nil {
		t.Fatal(err)
	}

	if _, err := h.cl.Resume(ctx, connect.NewRequest(&pb.ProjectRef{Project: "adopt"})); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		if it := get(); it.Status != "ready" && it.Status != "draft" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not start after resume", id)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
