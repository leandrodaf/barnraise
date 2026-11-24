package ports

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMoveToTrashAndRemoveTree(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "I-7")
	// Go's module cache leaves directories nobody may write in.
	locked := filepath.Join(dir, "pkg", "mod", "lib@v1")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "a.go"), []byte("package lib\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	trash, err := MoveToTrash(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatal("the directory must leave its place at once")
	}
	if filepath.Dir(trash) != root || !IsTrash(filepath.Base(trash)) || IsTrash("I-7") {
		t.Fatalf("trash name %s", trash)
	}
	if err := RemoveTree(trash); err != nil {
		t.Fatalf("a read-only directory must not stop the removal: %v", err)
	}
	if _, err := os.Lstat(trash); !os.IsNotExist(err) {
		t.Fatal("trash not deleted")
	}
	if err := RemoveTree(trash); err != nil {
		t.Fatalf("removing what is gone is not an error: %v", err)
	}
	if _, err := MoveToTrash(dir); err == nil {
		t.Fatal("moving what does not exist must fail")
	}
}
