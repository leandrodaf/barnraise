package toolinstall

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
)

// extractor unpacks one archive into root.
type extractor struct {
	root  string
	strip string // leading folder removed from every entry
	guest string // folder whose absolute links point inside a guest
	o     Options
	total int64
	count int
}

// name turns an entry's name into a clean relative path, "" to skip it.
func (x *extractor) name(raw string) (string, error) {
	n := strings.ReplaceAll(raw, "\\", "/")
	if strings.HasPrefix(n, "/") || strings.Contains(n, "\x00") {
		return "", fmt.Errorf("refused the absolute entry %q", raw)
	}
	for _, part := range strings.Split(n, "/") {
		if part == ".." {
			return "", fmt.Errorf("refused the entry %q: it climbs out of the folder", raw)
		}
	}
	n = path.Clean(n)
	if x.strip != "" {
		if n == x.strip {
			return "", nil
		}
		rest, ok := strings.CutPrefix(n, x.strip+"/")
		if !ok {
			return "", fmt.Errorf("refused the entry %q outside %s/", raw, x.strip)
		}
		n = rest
	}
	if n == "." || n == "" {
		return "", nil
	}
	return n, nil
}

func (x *extractor) account(size int64) error {
	x.count++
	if x.count > x.o.MaxEntries {
		return fmt.Errorf("more than %d entries", x.o.MaxEntries)
	}
	if size > x.o.MaxFile {
		return fmt.Errorf("a file is larger than %d bytes", x.o.MaxFile)
	}
	x.total += size
	if x.total > x.o.MaxExtract {
		return fmt.Errorf("more than %d bytes unpacked", x.o.MaxExtract)
	}
	return nil
}

// mkdirs creates the folders of rel inside root, refusing to go through
// anything that is not a real folder (a symbolic link of the archive).
func (x *extractor) mkdirs(rel string) error {
	cur := x.root
	if rel == "." || rel == "" {
		return nil
	}
	for _, part := range strings.Split(rel, "/") {
		cur = filepath.Join(cur, part)
		st, err := os.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := os.Mkdir(cur, 0o755); err != nil {
				return err
			}
		case err != nil:
			return err
		case !st.IsDir():
			return fmt.Errorf("refused to write through %s: it is not a folder", strings.TrimPrefix(cur, x.root+string(filepath.Separator)))
		}
	}
	return nil
}

func (x *extractor) file(rel string, mode fs.FileMode, r io.Reader) error {
	if err := x.mkdirs(path.Dir(rel)); err != nil {
		return err
	}
	perm := fs.FileMode(0o644)
	if mode&0o111 != 0 {
		perm = 0o755
	}
	f, err := os.OpenFile(filepath.Join(x.root, filepath.FromSlash(rel)), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, perm)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(r, x.o.MaxFile+1))
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err == nil && n > x.o.MaxFile {
		err = fmt.Errorf("%s is larger than %d bytes", rel, x.o.MaxFile)
	}
	return err
}

// link creates a symbolic link. A relative target must stay inside the
// tool's folder; an absolute one is kept only inside the guest tree,
// where it names a path of the guest's own file system.
func (x *extractor) link(rel, target string) error {
	if target == "" || strings.Contains(target, "\x00") {
		return fmt.Errorf("refused the link %s: empty target", rel)
	}
	inGuest := x.guest != "" && (rel == x.guest || strings.HasPrefix(rel, x.guest+"/"))
	if path.IsAbs(target) {
		if !inGuest {
			return fmt.Errorf("refused the link %s -> %s: it points outside the tool's folder", rel, target)
		}
	} else {
		res := path.Join(path.Dir(rel), target)
		if res == ".." || strings.HasPrefix(res, "../") {
			return fmt.Errorf("refused the link %s -> %s: it points outside the tool's folder", rel, target)
		}
		if inGuest && res != x.guest && !strings.HasPrefix(res, x.guest+"/") {
			return fmt.Errorf("refused the link %s -> %s: it leaves the guest tree", rel, target)
		}
	}
	if err := x.mkdirs(path.Dir(rel)); err != nil {
		return err
	}
	return os.Symlink(target, filepath.Join(x.root, filepath.FromSlash(rel)))
}

func (x *extractor) tarGz(file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		rel, err := x.name(h.Name)
		if err != nil {
			return err
		}
		if err := x.account(h.Size); err != nil {
			return err
		}
		if rel == "" {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = x.mkdirs(rel)
		case tar.TypeReg:
			err = x.file(rel, fs.FileMode(h.Mode), tr)
		case tar.TypeSymlink:
			err = x.link(rel, h.Linkname)
		case tar.TypeXGlobalHeader:
		default:
			err = fmt.Errorf("refused the entry %s: type %q is neither a file, a folder nor a link", h.Name, string(h.Typeflag))
		}
		if err != nil {
			return err
		}
	}
}

func (x *extractor) zip(file string) error {
	zr, err := zip.OpenReader(file)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()
	for _, zf := range zr.File {
		rel, err := x.name(zf.Name)
		if err != nil {
			return err
		}
		if err := x.account(int64(zf.UncompressedSize64)); err != nil {
			return err
		}
		if rel == "" {
			continue
		}
		mode := zf.Mode()
		switch {
		case mode.IsDir():
			err = x.mkdirs(rel)
		case mode&fs.ModeSymlink != 0:
			var target []byte
			target, err = readSmall(zf)
			if err == nil {
				err = x.link(rel, string(target))
			}
		case mode.IsRegular():
			var rc io.ReadCloser
			if rc, err = zf.Open(); err == nil {
				err = x.file(rel, mode, rc)
				rc.Close()
			}
		default:
			err = fmt.Errorf("refused the entry %s: it is neither a file, a folder nor a link", zf.Name)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func readSmall(zf *zip.File) ([]byte, error) {
	rc, err := zf.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, 4097))
	if err == nil && len(b) > 4096 {
		err = errors.New("link target too long")
	}
	return b, err
}

// raw installs an asset that is the program itself.
func (x *extractor) raw(file, bin string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if err := x.account(st.Size()); err != nil {
		return err
	}
	rel, err := x.name(bin)
	if err != nil || rel == "" {
		return fmt.Errorf("bad program path %q", bin)
	}
	return x.file(rel, 0o755, f)
}
