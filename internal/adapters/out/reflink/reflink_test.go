package reflink

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/leandrodaf/barnraise/internal/ports"
)

func tree(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "target")
	for name, body := range map[string]string{"a.o": "object a", "deep/b/c.rlib": "rlib", "deep/empty.txt": ""} {
		p := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(src, "a.o"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("deep/b/c.rlib", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	return src
}

func checkCopy(t *testing.T, dst string) {
	t.Helper()
	if b, err := os.ReadFile(filepath.Join(dst, "deep", "b", "c.rlib")); err != nil || string(b) != "rlib" {
		t.Fatalf("nested file: %q %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "deep", "empty.txt")); err != nil || len(b) != 0 {
		t.Fatalf("empty file: %q %v", b, err)
	}
	if st, err := os.Stat(filepath.Join(dst, "a.o")); err != nil || st.Mode().Perm() != 0o750 {
		t.Fatalf("mode kept: %v %v", st, err)
	}
	if l, err := os.Readlink(filepath.Join(dst, "link")); err != nil || l != "deep/b/c.rlib" {
		t.Fatalf("a link stays a link: %q %v", l, err)
	}
}

// With a full copy allowed, the tree always arrives: cloned where the
// filesystem can, copied where it cannot.
func TestCloneTreeWithFullCopy(t *testing.T) {
	src := tree(t)
	dst := filepath.Join(t.TempDir(), "w", "target")
	if err := New().CloneTree(context.Background(), src, dst, true); err != nil {
		t.Fatal(err)
	}
	checkCopy(t, dst)
	// A change in the copy leaves the original alone.
	if err := os.WriteFile(filepath.Join(dst, "a.o"), []byte("changed"), 0o750); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(src, "a.o")); string(b) != "object a" {
		t.Fatalf("original changed: %q", b)
	}
}

// Without it, a filesystem with no copy-on-write leaves nothing behind.
func TestCloneTreeWithoutCopyOnWriteSkips(t *testing.T) {
	src := tree(t)
	dst := filepath.Join(t.TempDir(), "target")
	err := New().CloneTree(context.Background(), src, dst, false)
	switch {
	case err == nil:
		checkCopy(t, dst) // this filesystem clones (Btrfs, XFS, APFS)
	case errors.Is(err, ports.ErrCloneUnsupported):
		if _, serr := os.Lstat(dst); !os.IsNotExist(serr) {
			t.Fatalf("nothing may be left at %s: %v", dst, serr)
		}
	default:
		t.Fatal(err)
	}
}

func TestCloneTreeRefusals(t *testing.T) {
	src := tree(t)
	c := New()
	if err := c.CloneTree(context.Background(), filepath.Join(src, "a.o"), filepath.Join(t.TempDir(), "x"), true); err == nil {
		t.Fatal("a file is not a tree")
	}
	if err := c.CloneTree(context.Background(), filepath.Join(src, "missing"), filepath.Join(t.TempDir(), "x"), true); err == nil {
		t.Fatal("a missing source is an error")
	}
	if err := c.CloneTree(context.Background(), src, t.TempDir(), true); err == nil {
		t.Fatal("an existing destination is never overwritten")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dst := filepath.Join(t.TempDir(), "t")
	if err := c.CloneTree(ctx, src, dst, true); err == nil && runtime.GOOS != "darwin" {
		t.Fatal("a cancelled context stops the walk")
	}
}

func TestCopyFileRefusesAnExistingFile(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	_ = os.WriteFile(a, []byte("a"), 0o600)
	_ = os.WriteFile(b, []byte("b"), 0o600)
	if err := copyFile(a, b, 0o600); err == nil {
		t.Fatal("copyFile overwrote a file")
	}
	if err := copyFile(filepath.Join(dir, "none"), filepath.Join(dir, "c"), 0o600); err == nil {
		t.Fatal("copyFile of a missing file")
	}
}
