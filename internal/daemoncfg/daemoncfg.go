// Package daemoncfg is the machine's own configuration: which projects the
// daemon drives, the cluster peers and the machine key. It lives in the
// config dir (0600), never in a repository; people do not edit it by hand —
// `barnraise up`, `machine invite/join/leave` write it.
package daemoncfg

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/leandrodaf/barnraise/internal/paths"
)

// Peer is another machine of the cluster.
type Peer struct {
	Name      string    `yaml:"name"`
	URL       string    `yaml:"url"`            // nats://host:4222
	PublicKey string    `yaml:"public_key"`     // its NATS nkey
	Seed      string    `yaml:"seed,omitempty"` // our credential on its server (its server authorizes our key)
	Added     time.Time `yaml:"added"`
}

// File is daemon.yaml.
type File struct {
	Machine    string   `yaml:"machine"`
	Projects   []string `yaml:"projects"` // repository roots
	Listen     string   `yaml:"listen"`   // TCP address for the API (default 127.0.0.1:7420)
	NATSHost   string   `yaml:"nats_host"`
	NATSPort   int      `yaml:"nats_port"`
	NKeySeed   string   `yaml:"nkey_seed,omitempty"`
	NKeyPublic string   `yaml:"nkey_public,omitempty"`
	Peers      []Peer   `yaml:"peers,omitempty"`
	Network    string   `yaml:"network,omitempty"` // tailscale, lan
	Notify     []struct {
		Type   string `yaml:"type"`
		Target string `yaml:"target"`
		Secret string `yaml:"secret,omitempty"`
	} `yaml:"notify,omitempty"`
	path string
}

// Load reads daemon.yaml (an empty file if missing).
func Load() (*File, error) { return LoadFrom(paths.DaemonConfig()) }

// LoadFrom reads a given path.
func LoadFrom(path string) (*File, error) {
	f := &File{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			f.defaults()
			return f, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(data, f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	f.path = path
	f.defaults()
	return f, nil
}

func (f *File) defaults() {
	if f.Machine == "" {
		h, _ := os.Hostname()
		f.Machine = strings.ToLower(strings.Split(h, ".")[0])
		if f.Machine == "" {
			f.Machine = "local"
		}
	}
	if f.Listen == "" {
		f.Listen = "127.0.0.1:7420"
	}
	if f.NATSPort == 0 {
		f.NATSPort = 4222
	}
	if f.NATSHost == "" {
		f.NATSHost = "127.0.0.1"
	}
}

// Save writes daemon.yaml atomically with 0600.
func (f *File) Save() error {
	if f.path == "" {
		f.path = paths.DaemonConfig()
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	sort.Strings(f.Projects)
	data, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, append([]byte("# Written by barnraise; change it with barnraise commands.\n"), data...), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

// Path of the file.
func (f *File) Path() string { return f.path }

// AddProject registers a repository root; it reports whether it was new.
func (f *File) AddProject(root string) bool {
	abs, err := filepath.Abs(root)
	if err == nil {
		root = abs
	}
	for _, p := range f.Projects {
		if p == root {
			return false
		}
	}
	f.Projects = append(f.Projects, root)
	return true
}

// RemoveProject unregisters a root.
func (f *File) RemoveProject(root string) {
	abs, _ := filepath.Abs(root)
	var out []string
	for _, p := range f.Projects {
		if p != root && p != abs {
			out = append(out, p)
		}
	}
	f.Projects = out
}

// UpsertPeer adds or replaces a peer by name.
func (f *File) UpsertPeer(p Peer) {
	for i := range f.Peers {
		if f.Peers[i].Name == p.Name {
			f.Peers[i] = p
			return
		}
	}
	f.Peers = append(f.Peers, p)
}

// RemovePeer drops a peer.
func (f *File) RemovePeer(name string) {
	var out []Peer
	for _, p := range f.Peers {
		if p.Name != name {
			out = append(out, p)
		}
	}
	f.Peers = out
}

// ModTime returns the file's modification time (for reload).
func (f *File) ModTime() time.Time {
	st, err := os.Stat(f.path)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}
