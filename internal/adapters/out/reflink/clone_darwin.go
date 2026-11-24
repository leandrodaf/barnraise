//go:build darwin

package reflink

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// cloneTreeNative clones the whole tree with one clonefile call (APFS
// clones directories recursively). It reports false when the filesystem
// cannot, so the tree is walked (and maybe copied) instead.
func cloneTreeNative(src, dst string) (bool, error) {
	err := unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW)
	if err == nil {
		return true, nil
	}
	if unsupported(err) {
		_ = os.RemoveAll(dst)
		return false, nil
	}
	return true, err
}

// cloneFile clones one file with clonefile.
func cloneFile(src, dst string, perm fs.FileMode) error {
	if err := unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW); err != nil {
		if unsupported(err) {
			return errNoClone
		}
		return err
	}
	return os.Chmod(dst, perm)
}

// unsupported are the answers of a filesystem without clones (HFS+, a
// network share) or of a clone across two volumes.
func unsupported(err error) bool {
	for _, e := range []error{unix.ENOTSUP, unix.EOPNOTSUPP, unix.EXDEV, unix.ENOSYS, unix.EINVAL} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}
