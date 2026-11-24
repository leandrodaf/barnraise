package toolinstall

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

type entry struct {
	name, body, link string
	typ              byte
	mode             int64
}

func tarGz(t *testing.T, ents []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range ents {
		h := &tar.Header{Name: e.name, Mode: e.mode, Typeflag: e.typ, Linkname: e.link, Size: int64(len(e.body))}
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Mode == 0 {
			h.Mode = 0o755
		}
		if h.Typeflag != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			_, _ = tw.Write([]byte(e.body))
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func zipOf(t *testing.T, ents []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range ents {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		switch {
		case e.link != "":
			h.SetMode(os.ModeSymlink | 0o777)
			e.body = e.link
		case strings.HasSuffix(e.name, "/"):
			h.SetMode(os.ModeDir | 0o755)
		default:
			h.SetMode(0o755)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(e.body))
	}
	_ = zw.Close()
	return buf.Bytes()
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

const script = "#!/bin/sh\necho tool 1.0.0\n"

// server serves files by path.
func server(t *testing.T, files map[string][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func recipe(id, version, bin, url, digest, format, strip, guest string) domain.ToolRecipe {
	return domain.ToolRecipe{ID: id, Title: id, Version: version, Bin: bin, GuestTree: guest,
		Assets: []domain.ToolAsset{{OS: runtime.GOOS, Arch: runtime.GOARCH, URL: url, SHA256: digest, Format: format, Strip: strip}}}
}

func newInstaller(t *testing.T, srv *httptest.Server, recipes ...domain.ToolRecipe) (*Installer, string) {
	root := filepath.Join(t.TempDir(), "tools")
	return New(Options{Root: root, Client: srv.Client(), Recipes: func() []domain.ToolRecipe { return recipes }}), root
}

func TestInstallUpgradeRemove(t *testing.T) {
	v1 := tarGz(t, []entry{{name: "pkg-1/", typ: tar.TypeDir}, {name: "pkg-1/tool", body: script}, {name: "pkg-1/lib/x.so", body: "x", mode: 0o4755},
		{name: "pkg-1/lib/y.so", typ: tar.TypeSymlink, link: "x.so"}, {name: "pkg-1/rootfs/bin/sh", typ: tar.TypeSymlink, link: "/bin/busybox"}})
	v2 := tarGz(t, []entry{{name: "tool", body: strings.Replace(script, "1.0.0", "2.0.0", 1)}})
	srv := server(t, map[string][]byte{"/v1.tgz": v1, "/v2.tgz": v2})
	r1 := recipe("tool", "1.0.0", "tool", srv.URL+"/v1.tgz", sum(v1), domain.FormatTarGz, "pkg-1", "rootfs")
	in, root := newInstaller(t, srv, r1)
	ctx := context.Background()
	res := in.Install(ctx, "tool")
	if !res.OK || !res.Changed || res.Error != "" || res.Previous != "" {
		t.Fatalf("install %+v", res)
	}
	link := filepath.Join(in.BinDir(), "tool")
	if out, err := exec.Command(link).Output(); err != nil || !strings.Contains(string(out), "1.0.0") {
		t.Fatalf("run %q %v", out, err)
	}
	if st, _ := os.Stat(filepath.Join(root, "tool", "1.0.0", "lib", "x.so")); st.Mode()&os.ModeSetuid != 0 || st.Mode().Perm() != 0o755 {
		t.Fatalf("setuid kept: %v", st.Mode())
	}
	if l, _ := os.Readlink(filepath.Join(root, "tool", "1.0.0", "rootfs", "bin", "sh")); l != "/bin/busybox" {
		t.Fatalf("guest link %q", l)
	}
	if in.Installed("tool") != "1.0.0" {
		t.Fatal("installed")
	}
	// Again: nothing to do.
	if res := in.Install(ctx, "tool"); !res.OK || res.Changed {
		t.Fatalf("second install %+v", res)
	}
	// A newer pin replaces the old version.
	r2 := recipe("tool", "2.0.0", "tool", srv.URL+"/v2.tgz", sum(v2), domain.FormatTarGz, "", "")
	in.o.Recipes = func() []domain.ToolRecipe { return []domain.ToolRecipe{r2} }
	res = in.Install(ctx, "tool")
	if !res.OK || !res.Changed || res.Previous != "1.0.0" {
		t.Fatalf("upgrade %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "tool", "1.0.0")); !os.IsNotExist(err) {
		t.Fatal("old version kept")
	}
	if out, _ := exec.Command(link).Output(); !strings.Contains(string(out), "2.0.0") {
		t.Fatalf("upgraded link runs %q", out)
	}
	res = in.Remove(ctx, "tool")
	if !res.OK || !res.Changed || res.Version != "2.0.0" {
		t.Fatalf("remove %+v", res)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatal("link kept")
	}
	if res := in.Remove(ctx, "tool"); !res.OK || res.Changed {
		t.Fatalf("second remove %+v", res)
	}
	if res := in.Remove(ctx, "nope"); res.OK || !strings.Contains(res.Error, "unknown tool") {
		t.Fatalf("unknown %+v", res)
	}
}

func TestRemoveKeepsAProgramItDidNotInstall(t *testing.T) {
	srv := server(t, nil)
	in, _ := newInstaller(t, srv, recipe("tool", "1", "tool", srv.URL+"/x", strings.Repeat("0", 64), domain.FormatRaw, "", ""))
	_ = os.MkdirAll(in.BinDir(), 0o755)
	other := filepath.Join(t.TempDir(), "tool")
	_ = os.WriteFile(other, []byte(script), 0o755)
	_ = os.Symlink(other, filepath.Join(in.BinDir(), "tool"))
	if res := in.Remove(context.Background(), "tool"); !res.OK || res.Changed {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Lstat(filepath.Join(in.BinDir(), "tool")); err != nil {
		t.Fatal("a link Barnraise did not make was removed")
	}
}

func TestZipAndRaw(t *testing.T) {
	z := zipOf(t, []entry{{name: "bin/"}, {name: "bin/ast-grep", body: script}, {name: "bin/sg", link: "ast-grep"}})
	raw := []byte(script)
	srv := server(t, map[string][]byte{"/a.zip": z, "/raw": raw})
	in, root := newInstaller(t, srv,
		domain.ToolRecipe{ID: "ag", Version: "1", Bin: "ast-grep", Path: "bin/ast-grep",
			Assets: []domain.ToolAsset{{OS: runtime.GOOS, Arch: runtime.GOARCH, URL: srv.URL + "/a.zip", SHA256: sum(z), Format: domain.FormatZip}}},
		recipe("raw", "1", "rawtool", srv.URL+"/raw", sum(raw), domain.FormatRaw, "", ""))
	for _, id := range []string{"ag", "raw"} {
		if res := in.Install(context.Background(), id); !res.OK {
			t.Fatalf("%s %+v", id, res)
		}
	}
	if l, _ := os.Readlink(filepath.Join(root, "ag", "1", "bin", "sg")); l != "ast-grep" {
		t.Fatalf("zip link %q", l)
	}
	if out, err := exec.Command(filepath.Join(in.BinDir(), "rawtool")).Output(); err != nil || !strings.Contains(string(out), "1.0.0") {
		t.Fatalf("raw %q %v", out, err)
	}
}

func TestRefusals(t *testing.T) {
	good := tarGz(t, []entry{{name: "tool", body: script}})
	cases := []struct {
		name    string
		body    []byte
		format  string
		strip   string
		guest   string
		digest  string // default: the body's
		url     string // default: the server's
		opts    func(*Options)
		wantErr string
	}{
		{name: "checksum mismatch", body: good, digest: strings.Repeat("ab", 32), wantErr: "sha256 mismatch"},
		{name: "no digest", body: good, digest: "x", wantErr: "pins no sha256"},
		{name: "plain http", body: good, url: "http://example.invalid/x", wantErr: "HTTPS only"},
		{name: "not found", body: nil, url: "/missing", wantErr: "404"},
		{name: "parent path", body: tarGz(t, []entry{{name: "../evil", body: "x"}, {name: "tool", body: script}}), wantErr: "climbs out"},
		{name: "absolute path", body: tarGz(t, []entry{{name: "/etc/evil", body: "x"}}), wantErr: "absolute"},
		{name: "escaping link", body: tarGz(t, []entry{{name: "l", typ: tar.TypeSymlink, link: "../../../etc"}}), wantErr: "outside the tool's folder"},
		{name: "absolute link outside the guest", body: tarGz(t, []entry{{name: "l", typ: tar.TypeSymlink, link: "/etc/passwd"}}), wantErr: "outside the tool's folder"},
		{name: "relative link leaving the guest", body: tarGz(t, []entry{{name: "g/l", typ: tar.TypeSymlink, link: "../tool"}}), guest: "g", wantErr: "leaves the guest"},
		{name: "write through a link", body: tarGz(t, []entry{{name: "d", typ: tar.TypeDir}, {name: "s", typ: tar.TypeSymlink, link: "d"}, {name: "s/tool", body: script}}),
			wantErr: "not a folder"},
		{name: "file over a link", body: tarGz(t, []entry{{name: "tool", typ: tar.TypeSymlink, link: "x"}, {name: "tool", body: script}}), wantErr: "exists"},
		{name: "hard link", body: tarGz(t, []entry{{name: "tool", body: script}, {name: "h", typ: tar.TypeLink, link: "tool"}}), wantErr: "neither"},
		{name: "device", body: tarGz(t, []entry{{name: "dev", typ: tar.TypeChar}}), wantErr: "neither"},
		{name: "outside the strip folder", body: tarGz(t, []entry{{name: "other/tool", body: script}}), strip: "pkg", wantErr: "outside pkg/"},
		{name: "no program", body: tarGz(t, []entry{{name: "README", body: "x"}}), wantErr: "has no program"},
		{name: "program not executable", body: tarGz(t, []entry{{name: "tool", body: "x", mode: 0o644}}), wantErr: "has no program"},
		{name: "download too large", body: good, opts: func(o *Options) { o.MaxDownload = 10 }, wantErr: "larger than 10"},
		{name: "file too large", body: good, opts: func(o *Options) { o.MaxFile = 5 }, wantErr: "larger than 5"},
		{name: "too much unpacked", body: tarGz(t, []entry{{name: "a", body: "123456"}, {name: "tool", body: script}}),
			opts: func(o *Options) { o.MaxExtract = 10 }, wantErr: "bytes unpacked"},
		{name: "too many entries", body: tarGz(t, []entry{{name: "a", body: "1"}, {name: "b", body: "1"}, {name: "tool", body: script}}),
			opts: func(o *Options) { o.MaxEntries = 2 }, wantErr: "more than 2 entries"},
		{name: "not gzip", body: []byte("plain"), wantErr: "unpack asset"},
		{name: "zip traversal", body: zipOf(t, []entry{{name: "../x", body: "1"}}), format: domain.FormatZip, wantErr: "climbs out"},
		{name: "zip escaping link", body: zipOf(t, []entry{{name: "l", link: "/etc"}}), format: domain.FormatZip, wantErr: "outside the tool's folder"},
		{name: "bad zip", body: []byte("nope"), format: domain.FormatZip, wantErr: "zip"},
		{name: "unknown format", body: good, format: "rar", wantErr: "unknown archive format"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := server(t, map[string][]byte{"/asset": tc.body})
			url := srv.URL + "/asset"
			if tc.url != "" {
				url = tc.url
				if strings.HasPrefix(tc.url, "/") {
					url = srv.URL + tc.url
				}
			}
			digest := tc.digest
			if digest == "" {
				digest = sum(tc.body)
			}
			format := tc.format
			if format == "" {
				format = domain.FormatTarGz
			}
			r := recipe("tool", "1", "tool", url, digest, format, tc.strip, tc.guest)
			root := filepath.Join(t.TempDir(), "tools")
			o := Options{Root: root, Client: srv.Client(), Recipes: func() []domain.ToolRecipe { return []domain.ToolRecipe{r} }}
			if tc.opts != nil {
				tc.opts(&o)
			}
			in := New(o)
			res := in.Install(context.Background(), "tool")
			if res.OK || !strings.Contains(res.Error, tc.wantErr) {
				t.Fatalf("want %q, got %+v", tc.wantErr, res)
			}
			// Nothing is left: no version folder, no link, no download.
			if _, err := os.Lstat(filepath.Join(in.BinDir(), "tool")); !os.IsNotExist(err) {
				t.Fatal("link left")
			}
			ents, _ := os.ReadDir(filepath.Join(root, "tool"))
			if len(ents) != 0 {
				t.Fatalf("left %v", ents)
			}
			top, _ := os.ReadDir(root)
			for _, e := range top {
				if strings.HasPrefix(e.Name(), ".download-") {
					t.Fatal("download left")
				}
			}
			if _, err := os.Stat("/etc/evil"); err == nil {
				t.Fatal("wrote outside")
			}
		})
	}
}

func TestUnknownAndUnsupported(t *testing.T) {
	srv := server(t, nil)
	r := domain.ToolRecipe{ID: "tool", Version: "1", Bin: "tool", Assets: []domain.ToolAsset{{OS: "plan9", Arch: "mips"}}}
	in, _ := newInstaller(t, srv, r)
	if res := in.Install(context.Background(), "tool"); res.OK || !strings.Contains(res.Error, "no build for") {
		t.Fatalf("%+v", res)
	}
	if res := in.Install(context.Background(), "nope"); res.OK || !strings.Contains(res.Error, "known: tool") {
		t.Fatalf("%+v", res)
	}
	if in.Installed("tool") != "" {
		t.Fatal("installed")
	}
}

func TestDefaultsAndRedirects(t *testing.T) {
	in := New(Options{Root: t.TempDir()})
	if in.o.Client == nil || len(in.o.Recipes()) == 0 || in.o.MaxEntries != DefaultMaxEntries {
		t.Fatal("defaults")
	}
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/x", nil)
	if httpsOnly(req, nil) == nil {
		t.Fatal("a redirect to plain HTTP is refused")
	}
	req, _ = http.NewRequest(http.MethodGet, "https://example.com/x", nil)
	if httpsOnly(req, nil) != nil || httpsOnly(req, make([]*http.Request, 10)) == nil {
		t.Fatal("redirects")
	}
	if !within("/a/b", "/a") || within("/ab", "/a") || within("/a/../b", "/a") {
		t.Fatal("within")
	}
}

// TestRealInstalls downloads every pinned tool of this platform from its
// release page into a temporary data folder and runs it. Network only:
// BARNRAISE_REAL_NET=1.
func TestRealInstalls(t *testing.T) {
	if os.Getenv("BARNRAISE_REAL_NET") != "1" {
		t.Skip("set BARNRAISE_REAL_NET=1 to download the pinned tools")
	}
	in := New(Options{Root: filepath.Join(t.TempDir(), "tools")})
	for _, r := range domain.ToolRecipes() {
		if _, ok := r.Asset(runtime.GOOS, runtime.GOARCH); !ok {
			continue
		}
		t.Run(r.ID, func(t *testing.T) {
			res := in.Install(context.Background(), r.ID)
			if !res.OK {
				t.Fatalf("%+v", res)
			}
			out, err := exec.Command(filepath.Join(in.BinDir(), r.Bin), "--version").CombinedOutput()
			if err != nil || !strings.Contains(string(out), r.Version) {
				t.Fatalf("%s --version: %v %q", r.Bin, err, out)
			}
		})
	}
}
