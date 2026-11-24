// Package reflink copies directory trees copy-on-write: each file of the
// copy shares its blocks with the original until one of them changes, so a
// multi-gigabyte build directory is "copied" in a moment and costs no disk.
// Linux clones file by file with the FICLONE ioctl (Btrfs, XFS, bcachefs,
// overlay on those); macOS clones the whole tree with clonefile (APFS).
// Elsewhere, or on a filesystem without it, nothing is copied unless a full
// copy is asked for.
package reflink

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// Cloner implements ports.TreeCloner.
type Cloner struct{}

var _ ports.TreeCloner = Cloner{}

// New returns the cloner of this platform.
func New() Cloner { return Cloner{} }

// errNoClone is returned by cloneFile when the filesystem cannot clone.
var errNoClone = errors.New("no clone")

// CloneTree copies src to dst (which must not exist) copy-on-write. With
// fullCopy, files the filesystem cannot clone are copied in full; without
// it, dst is removed and ports.ErrCloneUnsupported returned.
func (Cloner) CloneTree(ctx context.Context, src, dst string, fullCopy bool) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("reflink: %s is not a directory", src)
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("reflink: %s already exists", dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if done, err := cloneTreeNative(src, dst); done {
		return err
	}
	copying := false
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case d.Type()&fs.ModeSymlink != 0:
			// A link stays a link: what it points to is never read here.
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case !d.Type().IsRegular():
			return nil // sockets, pipes, devices: not build artifacts
		}
		if !copying {
			err := cloneFile(p, target, info.Mode().Perm())
			if err == nil {
				return nil
			}
			if !errors.Is(err, errNoClone) {
				return err
			}
			if !fullCopy {
				return ports.ErrCloneUnsupported
			}
			copying = true
		}
		return copyFile(p, target, info.Mode().Perm())
	})
	if err != nil {
		_ = os.RemoveAll(dst)
		return err
	}
	return nil
}

// copyFile copies one file in full.
func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
