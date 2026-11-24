package reflink

import (
	"errors"
	"os"
	"path/filepath"
)

// Supported reports whether the filesystem of dir clones files: it clones
// a small temporary file there and removes both copies. A false answer
// with a nil error means the filesystem has no clones.
func Supported(dir string) (bool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	tmp, err := os.MkdirTemp(dir, ".reflink-probe-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmp)
	src := filepath.Join(tmp, "a")
	if err := os.WriteFile(src, []byte("barnraise reflink probe\n"), 0o600); err != nil {
		return false, err
	}
	switch err := cloneFile(src, filepath.Join(tmp, "b"), 0o600); {
	case err == nil:
		return true, nil
	case errors.Is(err, errNoClone):
		return false, nil
	default:
		return false, err
	}
}
