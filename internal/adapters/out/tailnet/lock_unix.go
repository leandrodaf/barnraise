//go:build unix

package tailnet

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// lockDir takes an exclusive lock on dir, so two processes never run the
// same tailnet device at once. It fails with ErrBusy while another holds it.
func lockDir(dir string) (unlock func(), err error) {
	f, err := os.OpenFile(filepath.Join(dir, "barnraise.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
