// Package toolinstall installs the optional tools of domain.ToolRecipes
// without root (ADR-0046): it downloads the release asset of this system
// over HTTPS, straight from the daemon (never through the agents' egress
// proxy), checks its sha256 against the digest pinned in the recipe before
// anything is unpacked, unpacks it safely (no absolute or parent paths, no
// writes through a symbolic link, no links that leave the tool's folder,
// size limits) into <tools>/<id>/<version>/, and links the program into
// <tools>/bin, the folder Barnraise puts first on its PATH. A newer pinned
// version replaces the old one, which is removed. Nothing that needs root
// is ever run.
package toolinstall

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Limits of one install.
const (
	DefaultMaxDownload = 256 << 20  // bytes of the asset
	DefaultMaxExtract  = 1024 << 20 // bytes unpacked
	DefaultMaxFile     = 512 << 20  // bytes of one file
	DefaultMaxEntries  = 20000
)

// marker is written last in an installed version folder.
const marker = ".barnraise-installed"

// Options configure the installer.
type Options struct {
	// Root is <data dir>/tools.
	Root string
	// Client downloads the assets (default: HTTPS only, no proxy, 10 minutes).
	Client *http.Client
	// Recipes are the known tools (default domain.ToolRecipes).
	Recipes     func() []domain.ToolRecipe
	GOOS        string
	GOARCH      string
	MaxDownload int64
	MaxExtract  int64
	MaxFile     int64
	MaxEntries  int
}

// Installer implements ports.ToolInstaller.
type Installer struct {
	o  Options
	mu sync.Mutex
}

var _ ports.ToolInstaller = (*Installer)(nil)

// New returns an installer.
func New(o Options) *Installer {
	if o.Client == nil {
		o.Client = DefaultClient()
	}
	if o.Recipes == nil {
		o.Recipes = domain.ToolRecipes
	}
	if o.GOOS == "" {
		o.GOOS, o.GOARCH = goruntime.GOOS, goruntime.GOARCH
	}
	if o.MaxDownload <= 0 {
		o.MaxDownload = DefaultMaxDownload
	}
	if o.MaxExtract <= 0 {
		o.MaxExtract = DefaultMaxExtract
	}
	if o.MaxFile <= 0 {
		o.MaxFile = DefaultMaxFile
	}
	if o.MaxEntries <= 0 {
		o.MaxEntries = DefaultMaxEntries
	}
	return &Installer{o: o}
}

// DefaultClient downloads directly (no proxy from the environment: the
// agents' egress proxy is not the daemon's) and follows HTTPS redirects only.
func DefaultClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	return &http.Client{Transport: tr, Timeout: 10 * time.Minute, CheckRedirect: httpsOnly}
}

func httpsOnly(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" {
		return fmt.Errorf("refused a redirect to %s: downloads are HTTPS only", req.URL.Redacted())
	}
	if len(via) >= 10 {
		return errors.New("too many redirects")
	}
	return nil
}

// BinDir is <tools>/bin.
func (in *Installer) BinDir() string { return BinDir(in.o.Root) }

// BinDir is the folder of the installed programs under a tools root.
func BinDir(root string) string { return filepath.Join(root, "bin") }

func (in *Installer) recipe(id string) (domain.ToolRecipe, error) {
	for _, r := range in.o.Recipes() {
		if r.ID == id {
			return r, nil
		}
	}
	return domain.ToolRecipe{}, fmt.Errorf("unknown tool %q (known: %s)", id, strings.Join(in.ids(), ", "))
}

func (in *Installer) ids() []string {
	var out []string
	for _, r := range in.o.Recipes() {
		out = append(out, r.ID)
	}
	return out
}

// Installed returns the version of a tool installed here, "" when none.
func (in *Installer) Installed(id string) string {
	ents, err := os.ReadDir(filepath.Join(in.o.Root, id))
	if err != nil {
		return ""
	}
	for _, e := range ents {
		if _, err := os.Stat(filepath.Join(in.o.Root, id, e.Name(), marker)); err == nil {
			return e.Name()
		}
	}
	return ""
}

// Install installs the pinned version of a tool (or does nothing when it
// is already there).
func (in *Installer) Install(ctx context.Context, id string) domain.ToolResult {
	in.mu.Lock()
	defer in.mu.Unlock()
	res := domain.ToolResult{Tool: id}
	r, err := in.recipe(id)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Version = r.Version
	asset, ok := r.Asset(in.o.GOOS, in.o.GOARCH)
	if !ok {
		res.Error = fmt.Sprintf("%s ships no build for %s/%s", id, in.o.GOOS, in.o.GOARCH)
		return res
	}
	prev := in.Installed(id)
	dir := filepath.Join(in.o.Root, id, r.Version)
	link := filepath.Join(in.BinDir(), r.Bin)
	target := filepath.Join(dir, filepath.FromSlash(r.BinPathIn(asset)))
	if prev == r.Version {
		if cur, err := os.Readlink(link); err == nil && cur == target {
			res.OK, res.Path = true, link
			return res
		}
	}
	if err := in.install(ctx, r, asset, dir); err != nil {
		res.Error = err.Error()
		return res
	}
	if err := linkBin(target, link); err != nil {
		res.Error = err.Error()
		return res
	}
	in.removeOthers(id, r.Version)
	res.OK, res.Changed, res.Path = true, true, link
	if prev != r.Version {
		res.Previous = prev
	}
	return res
}

// Remove removes a tool installed by Barnraise and its link.
func (in *Installer) Remove(_ context.Context, id string) domain.ToolResult {
	in.mu.Lock()
	defer in.mu.Unlock()
	res := domain.ToolResult{Tool: id}
	r, err := in.recipe(id)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Version = in.Installed(id)
	link := filepath.Join(in.BinDir(), r.Bin)
	toolDir := filepath.Join(in.o.Root, id)
	if cur, err := os.Readlink(link); err == nil && within(cur, toolDir) {
		if err := os.Remove(link); err != nil {
			res.Error = err.Error()
			return res
		}
		res.Changed = true
	}
	if _, err := os.Lstat(toolDir); err == nil {
		if err := os.RemoveAll(toolDir); err != nil {
			res.Error = err.Error()
			return res
		}
		res.Changed = true
	}
	res.OK = true
	return res
}

func (in *Installer) removeOthers(id, keep string) {
	ents, _ := os.ReadDir(filepath.Join(in.o.Root, id))
	for _, e := range ents {
		if e.Name() != keep {
			_ = os.RemoveAll(filepath.Join(in.o.Root, id, e.Name()))
		}
	}
}

func (in *Installer) install(ctx context.Context, r domain.ToolRecipe, a domain.ToolAsset, dir string) error {
	if err := os.MkdirAll(filepath.Join(in.o.Root, r.ID), 0o755); err != nil {
		return err
	}
	file, err := in.download(ctx, a)
	if err != nil {
		return err
	}
	defer os.Remove(file)
	staging := filepath.Join(in.o.Root, r.ID, ".staging-"+randHex())
	if err := os.Mkdir(staging, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	x := &extractor{root: staging, strip: a.Strip, guest: r.GuestTree, o: in.o}
	switch a.Format {
	case domain.FormatTarGz:
		err = x.tarGz(file)
	case domain.FormatZip:
		err = x.zip(file)
	case domain.FormatRaw:
		err = x.raw(file, r.BinPathIn(a))
	default:
		err = fmt.Errorf("unknown archive format %q", a.Format)
	}
	if err != nil {
		return fmt.Errorf("unpack %s: %w", path.Base(a.URL), err)
	}
	bin := filepath.Join(staging, filepath.FromSlash(r.BinPathIn(a)))
	st, err := os.Lstat(bin)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0o100 == 0 {
		return fmt.Errorf("the archive has no program %s", r.BinPathIn(a))
	}
	if err := os.WriteFile(filepath.Join(staging, marker), []byte(a.SHA256+"\n"), 0o644); err != nil {
		return err
	}
	_ = os.RemoveAll(dir) // a half-installed folder of an earlier attempt
	return os.Rename(staging, dir)
}

// download saves the asset to a temporary file and checks its digest.
func (in *Installer) download(ctx context.Context, a domain.ToolAsset) (string, error) {
	if !strings.HasPrefix(a.URL, "https://") {
		return "", fmt.Errorf("refused %s: downloads are HTTPS only", a.URL)
	}
	if len(a.SHA256) != 64 {
		return "", errors.New("the recipe pins no sha256")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "barnraise-toolinstall")
	resp, err := in.o.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", a.URL, resp.Status)
	}
	f, err := os.CreateTemp(in.o.Root, ".download-")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, in.o.MaxDownload+1))
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err == nil && n > in.o.MaxDownload {
		err = fmt.Errorf("the asset is larger than %d bytes", in.o.MaxDownload)
	}
	if err == nil {
		if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, a.SHA256) {
			err = fmt.Errorf("sha256 mismatch for %s: got %s, pinned %s; nothing was installed", path.Base(a.URL), got, a.SHA256)
		}
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// linkBin points link at target, replacing what was there atomically.
func linkBin(target, link string) error {
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	tmp := link + ".tmp-" + randHex()
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func randHex() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// within reports whether p is base or inside it.
func within(p, base string) bool {
	rel, err := filepath.Rel(base, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
