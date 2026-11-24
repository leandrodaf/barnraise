package toolinstall

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func TestUsePATH(t *testing.T) {
	root := Root(t.TempDir())
	_ = os.MkdirAll(BinDir(root), 0o755)
	_ = os.WriteFile(filepath.Join(BinDir(root), "barnraise-test-tool"), []byte("#!/bin/sh\necho hi\n"), 0o755)
	t.Setenv("PATH", "/usr/bin:/bin")
	UsePATH(root)
	UsePATH(root) // once only
	if got := os.Getenv("PATH"); got != BinDir(root)+":/usr/bin:/bin" {
		t.Fatalf("PATH %q", got)
	}
	if os.Getenv(domain.EnvToolsDir) != root {
		t.Fatal("tools dir")
	}
	if p, err := exec.LookPath("barnraise-test-tool"); err != nil || !strings.HasPrefix(p, BinDir(root)) {
		t.Fatalf("lookup %q %v", p, err)
	}
	t.Setenv("PATH", "")
	UsePATH(root)
	if os.Getenv("PATH") != BinDir(root) {
		t.Fatal("empty PATH")
	}
}
