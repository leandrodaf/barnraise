package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMarkNotIndexed(t *testing.T) {
	dir := t.TempDir()
	// A marker someone already wrote keeps its content.
	if err := os.WriteFile(filepath.Join(dir, ".nomedia"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 { // every daemon start marks again
		if err := MarkNotIndexed(dir); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".trackerignore")); err != nil {
		t.Fatalf("no .trackerignore: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".nomedia")); string(b) != "mine" {
		t.Fatalf(".nomedia was rewritten: %q", b)
	}
	if err := MarkNotIndexed(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a directory that does not exist must be an error")
	}
}
