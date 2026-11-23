package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/adapters/out/sqlite"
	"github.com/leandrodaf/barnraise/internal/ports"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

func TestMain(m *testing.M) {
	testkit.IsolateGit()
	os.Exit(m.Run())
}

func TestEventStoreMemory(t *testing.T) {
	EventStore(t, func(*testing.T) ports.EventStore { return testkit.NewStore() })
}

func TestEventStoreSQLiteMemory(t *testing.T) {
	EventStore(t, func(t *testing.T) ports.EventStore {
		s, err := sqlite.Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func TestEventStoreSQLiteFile(t *testing.T) {
	EventStore(t, func(t *testing.T) ports.EventStore {
		s, err := sqlite.Open(filepath.Join(t.TempDir(), "events.db"))
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func TestArbiterMemory(t *testing.T) {
	Arbiter(t, func(*testing.T) func(ports.Clock) ports.Arbiter {
		b := testkit.NewArbiterBackend()
		return func(c ports.Clock) ports.Arbiter { return b.Handle(c) }
	})
}

func TestArbiterGitRemote(t *testing.T) {
	t.Parallel()
	Arbiter(t, func(t *testing.T) func(ports.Clock) ports.Arbiter {
		var r *testkit.Repo
		return func(c ports.Clock) ports.Arbiter {
			if r == nil {
				r = testkit.NewRepo(t, c, nil)
				return r.Arbiter
			}
			return r.Peer(t, c).Arbiter
		}
	})
}

func TestArbiterGitLocal(t *testing.T) {
	t.Parallel()
	Arbiter(t, func(t *testing.T) func(ports.Clock) ports.Arbiter {
		var r *testkit.Repo
		return func(c ports.Clock) ports.Arbiter {
			if r == nil {
				r = testkit.NewLocalRepo(t, c, nil)
				return r.Arbiter
			}
			return r.Peer(t, c).Arbiter
		}
	})
}

func TestArbiterIntegrateMemory(t *testing.T) {
	ArbiterIntegrate(t, func(*testing.T) IntegrateFixture {
		b := testkit.NewArbiterBackend()
		b.AddCommit("m0", "")
		b.AddCommit("c1", "m0")
		b.AddCommit("c2", "c1")
		b.AddCommit("u1", "")
		b.SetMain("m0")
		return IntegrateFixture{Arbiter: b.Handle(nil), Main: "m0", Child: "c1", Grandchild: "c2", Unrelated: "u1", MainAt: b.Main}
	})
}

func gitIntegrateFixture(t *testing.T, r *testkit.Repo) IntegrateFixture {
	main := testkit.RunGit(t, r.Root, "rev-parse", "HEAD")
	testkit.RunGit(t, r.Root, "checkout", "-q", "-b", "side")
	testkit.WriteFile(t, r.Root, "b.txt", "b\n")
	testkit.RunGit(t, r.Root, "add", "-A")
	testkit.RunGit(t, r.Root, "commit", "-q", "-m", "child")
	child := testkit.RunGit(t, r.Root, "rev-parse", "HEAD")
	testkit.WriteFile(t, r.Root, "c.txt", "c\n")
	testkit.RunGit(t, r.Root, "add", "-A")
	testkit.RunGit(t, r.Root, "commit", "-q", "-m", "grandchild")
	grand := testkit.RunGit(t, r.Root, "rev-parse", "HEAD")
	tree := testkit.RunGit(t, r.Root, "rev-parse", "HEAD^{tree}")
	unrelated := testkit.RunGit(t, r.Root, "commit-tree", tree, "-m", "orphan")
	testkit.RunGit(t, r.Root, "checkout", "-q", "--detach")
	return IntegrateFixture{Arbiter: r.Arbiter, Main: main, Child: child, Grandchild: grand, Unrelated: unrelated,
		MainAt: func() string { return strings.TrimSpace(r.RemoteMain(t)) }}
}

func TestArbiterIntegrateGitRemote(t *testing.T) {
	t.Parallel()
	ArbiterIntegrate(t, func(t *testing.T) IntegrateFixture { return gitIntegrateFixture(t, testkit.NewRepo(t, nil, nil)) })
}

func TestArbiterIntegrateGitLocal(t *testing.T) {
	t.Parallel()
	ArbiterIntegrate(t, func(t *testing.T) IntegrateFixture { return gitIntegrateFixture(t, testkit.NewLocalRepo(t, nil, nil)) })
}
