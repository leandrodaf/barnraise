//go:build !linux

package landlock

import "errors"

var errNotLinux = errors.New("Landlock is a Linux security module; this system has none")

func abiVersion() (int, error) { return 0, errNotLinux }

func restrict(Ruleset, int) error { return errNotLinux }

func execve(string, []string, []string) error { return errNotLinux }
