// Package paths resolves where Barnraise keeps its files. Nothing here is
// in the repository: state, secrets and sockets stay on the machine.
package paths

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// ConfigDir is ~/.config/barnraise (owner token, daemon config, templates).
func ConfigDir() string {
	if v := os.Getenv("BARNRAISE_CONFIG_DIR"); v != "" {
		return v
	}
	d, err := os.UserConfigDir()
	if err != nil {
		d = filepath.Join(home(), ".config")
	}
	return filepath.Join(d, "barnraise")
}

// DataDir is ~/.local/share/barnraise (events, worktrees, logs, blobs).
func DataDir() string {
	if v := os.Getenv("BARNRAISE_DATA_DIR"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return filepath.Join(v, "barnraise")
	}
	return filepath.Join(home(), ".local", "share", "barnraise")
}

// Socket is the local API socket.
func Socket() string {
	if v := os.Getenv("BARNRAISE_SOCKET"); v != "" {
		return v
	}
	p := filepath.Join(DataDir(), "barnraise.sock")
	if len(p) < 100 {
		return p
	}
	// Unix socket paths are limited to ~108 bytes: use a short, stable path.
	sum := sha256.Sum256([]byte(DataDir()))
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "barnraise-"+hex.EncodeToString(sum[:6])+".sock")
}

// OwnerTokenFile holds the owner's secret (0600). Agents cannot read it: the
// hook denies ~/.config/barnraise/**.
func OwnerTokenFile() string { return filepath.Join(ConfigDir(), "owner.token") }

// DaemonConfig lists the projects a daemon drives.
func DaemonConfig() string { return filepath.Join(ConfigDir(), "daemon.yaml") }

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// noIndexMarkers are the files desktop indexers look for before walking a
// directory: Tracker and LocalSearch (GNOME) skip a directory that holds
// .trackerignore or .nomedia, with everything below it.
var noIndexMarkers = []string{".trackerignore", ".nomedia"}

// MarkNotIndexed tells desktop file indexers to stay out of dir. Worktrees
// hold hundreds of thousands of files (dependencies, build output): an indexer
// that walks them keeps a core busy and fills its database with nothing a
// person searches for. The markers are empty files; existing ones are kept.
func MarkNotIndexed(dir string) error {
	for _, name := range noIndexMarkers {
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}
