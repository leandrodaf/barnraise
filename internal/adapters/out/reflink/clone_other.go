//go:build !linux && !darwin

package reflink

import "io/fs"

// cloneTreeNative: no copy-on-write clone on this platform.
func cloneTreeNative(_, _ string) (bool, error) { return false, nil }

// cloneFile always reports that the filesystem cannot clone.
func cloneFile(_, _ string, _ fs.FileMode) error { return errNoClone }
