//go:build linux

package reflink

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// cloneTreeNative: Linux has no tree clone; files are cloned one by one.
func cloneTreeNative(_, _ string) (bool, error) { return false, nil }

// cloneFile clones one file with the FICLONE ioctl. errNoClone means the
// filesystem (or the pair of filesystems) cannot share blocks.
func cloneFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	cerr := unix.IoctlFileClone(int(out.Fd()), int(in.Fd()))
	if err := out.Close(); err != nil && cerr == nil {
		cerr = err
	}
	if cerr != nil {
		_ = os.Remove(dst)
		if unsupported(cerr) {
			return errNoClone
		}
		return cerr
	}
	return nil
}

// unsupported are the answers of a filesystem without reflinks, or of a
// clone across two filesystems.
func unsupported(err error) bool {
	for _, e := range []error{unix.EOPNOTSUPP, unix.ENOTSUP, unix.EXDEV, unix.EINVAL, unix.ENOTTY, unix.ENOSYS, unix.EPERM} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}
