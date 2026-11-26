package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// The daemon's mark makes the next tool call read the inbox, once.
func TestMailWaitingReadsTheDaemonsMarkOnce(t *testing.T) {
	wt := t.TempDir()
	t.Setenv("BARNRAISE_WORKTREE", wt)
	if mailWaiting() {
		t.Fatal("no mark")
	}
	dir := filepath.Join(wt, ".barnraise-session")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, domain.MailFlag), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !mailWaiting() {
		t.Fatal("the mark was not seen")
	}
	if mailWaiting() {
		t.Fatal("the mark is cleared once read")
	}
	t.Setenv("BARNRAISE_WORKTREE", "")
	if mailWaiting() {
		t.Fatal("outside a session")
	}
}
