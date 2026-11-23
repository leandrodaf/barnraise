package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// Keep the developer's git config out of the tests.
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Exit(m.Run())
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func g(t testing.TB, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.com"}, args...)
	out, err := run(context.Background(), dir, nil, full...)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return strings.TrimSpace(out)
}

func write(t testing.TB, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixture is a bare remote with one commit on main and two clones of it.
type fixture struct {
	bare, c1, c2 string
}

func newFixture(t testing.TB) fixture {
	t.Helper()
	base := t.TempDir()
	f := fixture{bare: filepath.Join(base, "remote.git"), c1: filepath.Join(base, "m1"), c2: filepath.Join(base, "m2")}
	g(t, base, "init", "-q", "--bare", "-b", "main", f.bare)
	seed := filepath.Join(base, "seed")
	g(t, base, "init", "-q", "-b", "main", seed)
	write(t, seed, "a.txt", "one\ntwo\nthree\n")
	g(t, seed, "add", "-A")
	g(t, seed, "commit", "-q", "-m", "initial")
	g(t, seed, "push", "-q", f.bare, "main")
	g(t, base, "clone", "-q", f.bare, f.c1)
	g(t, base, "clone", "-q", f.bare, f.c2)
	return f
}

// newLocal is a repository without remote.
func newLocal(t testing.TB) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	g(t, filepath.Dir(dir), "init", "-q", "-b", "main", dir)
	write(t, dir, "a.txt", "one\ntwo\nthree\n")
	g(t, dir, "add", "-A")
	g(t, dir, "commit", "-q", "-m", "initial")
	return dir
}
