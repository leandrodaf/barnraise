//go:build !unix

package tailnet

// lockDir does nothing where flock does not exist; Barnraise ships for
// Linux and macOS only.
func lockDir(string) (func(), error) { return func() {}, nil }
