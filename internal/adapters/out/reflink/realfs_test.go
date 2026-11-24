package reflink

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestRealCopyOnWriteFilesystem runs on a directory the caller names in
// BARNRAISE_REFLINK_DIR, on a filesystem that clones (Btrfs, XFS with
// reflink, APFS): without a full copy allowed, the tree must still arrive.
func TestRealCopyOnWriteFilesystem(t *testing.T) {
	dir := os.Getenv("BARNRAISE_REFLINK_DIR")
	if dir == "" {
		t.Skip("set BARNRAISE_REFLINK_DIR to a directory on a copy-on-write filesystem")
	}
	base, err := os.MkdirTemp(dir, "reflink-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	src := tree(t)
	// tree() builds under t.TempDir(); move it onto the filesystem under test.
	onfs := filepath.Join(base, "target")
	if err := New().CloneTree(context.Background(), src, onfs, true); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(base, "w", "target")
	if err := New().CloneTree(context.Background(), onfs, dst, false); err != nil {
		t.Fatalf("clone on a copy-on-write filesystem: %v", err)
	}
	checkCopy(t, dst)
	if err := os.WriteFile(filepath.Join(dst, "a.o"), []byte("changed"), 0o750); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(onfs, "a.o")); string(b) != "object a" {
		t.Fatalf("original changed: %q", b)
	}
}
