package landlock

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// fakeFS is a tree of folders and files for planning.
type fakeFS map[string]bool // path -> is a folder

type fakeInfo struct {
	name string
	dir  bool
}

func (i fakeInfo) Name() string               { return i.name }
func (i fakeInfo) Size() int64                { return 0 }
func (i fakeInfo) Mode() fs.FileMode          { return 0 }
func (i fakeInfo) ModTime() time.Time         { return time.Time{} }
func (i fakeInfo) IsDir() bool                { return i.dir }
func (i fakeInfo) Sys() any                   { return nil }
func (i fakeInfo) Type() fs.FileMode          { return 0 }
func (i fakeInfo) Info() (fs.FileInfo, error) { return i, nil }

func (f fakeFS) Stat(p string) (os.FileInfo, error) {
	dir, ok := f[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	return fakeInfo{filepath.Base(p), dir}, nil
}

func (f fakeFS) ReadDir(p string) ([]os.DirEntry, error) {
	if !f[p] {
		return nil, os.ErrNotExist
	}
	var out []os.DirEntry
	for q, dir := range f {
		if filepath.Dir(q) == p && q != p {
			out = append(out, fakeInfo{filepath.Base(q), dir})
		}
	}
	return out, nil
}

func TestPlanCarvesDeniedPaths(t *testing.T) {
	f := fakeFS{"/home/u": true, "/home/u/.cache": true, "/home/u/.cache/barnraise": true, "/home/u/.cache/go-build": true,
		"/home/u/.cache/pip": true, "/home/u/.cache/token": false, "/home/u/.local": true, "/home/u/.local/share": true,
		"/home/u/.local/share/barnraise": true, "/home/u/.local/share/barnraise/wt": true, "/home/u/.local/share/barnraise/wt/i1": true,
		"/usr": true, "/etc": true, "/etc/hosts": false, "/home/u/.gitconfig": false, "/dev": true}
	c := domain.Confinement{
		Work:      []string{"/home/u/.local/share/barnraise/wt/i1", "relative/is/ignored"},
		ReadWrite: []string{"/home/u/.cache", "/dev", "/missing"},
		ReadOnly:  []string{"/usr", "/etc", "/home/u/.gitconfig", "/home/u/.local/share/barnraise/wt"},
		DenyRead:  []string{"/home/u/.cache/barnraise", "/home/u/.local/share/barnraise"},
		Network:   domain.NetLocal, Connect: []int{7421, 0, 70000}, Ephemeral: true,
	}
	rs := plan(c, f, func() (int, int) { return 60000, 60002 })
	has := func(list []string, p string) bool { return domain.Contains(list, p) }
	if !has(rs.RWDirs, "/home/u/.local/share/barnraise/wt/i1") {
		t.Fatalf("the worktree is granted even inside a denied folder: %+v", rs)
	}
	if has(rs.RWDirs, "/home/u/.cache") || has(rs.RWDirs, "/home/u/.cache/barnraise") {
		t.Fatalf("a folder holding a denied path is carved: %+v", rs.RWDirs)
	}
	if !has(rs.RWDirs, "/home/u/.cache/go-build") || !has(rs.RWDirs, "/home/u/.cache/pip") || !has(rs.RWFiles, "/home/u/.cache/token") {
		t.Fatalf("its other entries stay: %+v", rs)
	}
	if has(rs.RODirs, "/home/u/.local/share/barnraise/wt") {
		t.Fatal("a read-only path inside a denied folder is dropped")
	}
	if !has(rs.RODirs, "/usr") || !has(rs.ROFiles, "/home/u/.gitconfig") || !has(rs.RWDirs, "/dev") {
		t.Fatalf("plain grants: %+v", rs)
	}
	if !rs.RestrictNet || fmt.Sprint(rs.Connect) != "[7421 60000 60001 60002]" {
		t.Fatalf("ports: %v", rs.Connect)
	}
	c.Network = domain.NetOpen
	if rs := plan(c, f, nil); rs.RestrictNet || len(rs.Connect) != 0 {
		t.Fatal("open network")
	}
	// A deny folder that cannot be listed grants nothing of its parent.
	g := fakeFS{"/a": true}
	g2 := plan(domain.Confinement{ReadOnly: []string{"/a"}, DenyRead: []string{"/a/b/c"}}, statOnly{g}, nil)
	if len(g2.RODirs) != 0 {
		t.Fatalf("unlistable: %+v", g2)
	}
}

type statOnly struct{ fakeFS }

func (statOnly) ReadDir(string) ([]os.DirEntry, error) { return nil, errors.New("no") }

func TestWithinAndRange(t *testing.T) {
	if !within("/a/b", "/a") || within("/ab", "/a") || !within("/x", "/") || !within("/a", "/a") {
		t.Fatal("within")
	}
	if lo, hi, ok := parseRange("32768\t60999\n"); !ok || lo != 32768 || hi != 60999 {
		t.Fatal("range")
	}
	for _, bad := range []string{"", "1", "a b", "9 3", "0 10", "1 70000"} {
		if _, _, ok := parseRange(bad); ok {
			t.Errorf("%q", bad)
		}
	}
	if lo, hi := EphemeralRange(); lo < 1 || hi > 65535 || lo > hi {
		t.Fatal("ephemeral")
	}
}

func TestSupport(t *testing.T) {
	if s := supportFor(func() (int, error) { return 0, errors.New("boom") }); s.Available || s.Reason != "boom" {
		t.Fatal(s)
	}
	if s := supportFor(func() (int, error) { return 0, nil }); s.Available || !strings.Contains(s.Reason, "5.13") {
		t.Fatal(s)
	}
	if s := supportFor(func() (int, error) { return 3, nil }); !s.Available || s.Network {
		t.Fatal(s)
	}
	if s := supportFor(func() (int, error) { return 6, nil }); !s.Available || !s.Network || s.ABI != 6 {
		t.Fatal(s)
	}
	if New("/x").Support().Backend != Backend {
		t.Fatal("backend")
	}
}

func TestWrap(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "barnraise")
	c := &Confiner{Binary: bin, probe: func() (int, error) { return 6, nil },
		lookPath: func(n string) (string, error) { return "/opt/tools/bin/" + n, nil }}
	name, args, err := c.Wrap(domain.Confinement{Profile: "check", Work: []string{"/w"}}, "sh", []string{"-c", "go test"})
	if err != nil || name != bin || args[0] != ExecCommand || args[1] != "--spec" || args[3] != "--" || args[4] != "sh" || args[6] != "go test" {
		t.Fatalf("%s %v %v", name, args, err)
	}
	var conf domain.Confinement
	if err := json.Unmarshal([]byte(args[2]), &conf); err != nil {
		t.Fatal(err)
	}
	if !domain.Contains(conf.ReadOnly, "/opt/tools/bin") || !domain.Contains(conf.ReadOnly, filepath.Dir(bin)) || conf.Command != "sh -c go test" {
		t.Fatalf("the program's folder and Barnraise's are readable: %+v", conf)
	}
	none := &Confiner{Binary: bin, probe: func() (int, error) { return 0, nil }, lookPath: c.lookPath}
	if _, _, err := none.Wrap(domain.Confinement{}, "sh", nil); err == nil {
		t.Fatal("no Landlock: no wrapper")
	}
	nobin := &Confiner{probe: c.probe, lookPath: c.lookPath}
	if _, _, err := nobin.Wrap(domain.Confinement{}, "sh", nil); err == nil {
		t.Fatal("no binary")
	}
}

func TestExecRefusesBeforeRunning(t *testing.T) {
	if err := Exec("{", []string{"true"}); err == nil {
		t.Fatal("bad spec")
	}
	if err := Exec("{}", nil); err == nil {
		t.Fatal("no command")
	}
	spec, _ := json.Marshal(domain.Confinement{Profile: "check", DenyCommands: []string{`\bgit\s+push\b`}})
	if err := Exec(string(spec), []string{"sh", "-c", "git push origin x"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("denied: %v", err)
	}
	if err := Exec("{}", []string{"/no/such/program-xyz"}); err == nil {
		t.Fatal("missing program")
	}
}
