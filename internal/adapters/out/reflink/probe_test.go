package reflink

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSupportedLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	if _, err := Supported(dir); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 0 {
		t.Fatalf("left %v", ents)
	}
	if v := os.Getenv("BARNRAISE_REFLINK_DIR"); v != "" {
		ok, err := Supported(filepath.Join(v, "probe"))
		if err != nil || !ok {
			t.Fatalf("a copy-on-write filesystem: %v %v", ok, err)
		}
	}
	if _, err := Supported("/proc/self/nope/x"); err == nil {
		t.Fatal("an unwritable folder is an error")
	}
}
