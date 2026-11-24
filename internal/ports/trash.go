package ports

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// TrashPrefix starts the name of a directory on its way out. Removing a
// worktree with its dependencies means deleting hundreds of thousands of
// files: the directory is renamed first, which is immediate, and deleted in
// the background. A leftover (the process ended in the middle) is deleted by
// the next sweep of its parent.
const TrashPrefix = ".trash-"

// IsTrash reports whether a directory name is one made by MoveToTrash.
func IsTrash(name string) bool { return strings.HasPrefix(name, TrashPrefix) }

// MoveToTrash renames dir to a trash name beside it and returns that name.
// The rename is atomic: from then on dir does not exist.
func MoveToTrash(dir string) (string, error) {
	trash := filepath.Join(filepath.Dir(dir), TrashPrefix+filepath.Base(dir)+"-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	if err := os.Rename(dir, trash); err != nil {
		return "", err
	}
	return trash, nil
}

// RemoveTree deletes dir and everything below it. Some tools leave
// directories without write permission (Go's module cache does); those are
// opened up and the removal is tried once more.
func RemoveTree(dir string) error {
	err := os.RemoveAll(dir)
	if err == nil {
		return nil
	}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, werr error) error {
		if werr == nil && d.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	return os.RemoveAll(dir)
}
