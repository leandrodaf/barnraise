// Package daemoncfg is the machine's own configuration: which projects the
// daemon drives, the cluster peers, the machine key and how it reaches the
// other machines. It lives in the
// config dir (0600), never in a repository; people do not edit it by hand —
// `barnraise up`, `machine invite/join/leave` write it.
package daemoncfg

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/leandrodaf/barnraise/internal/domain"
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

// TSNet configures the embedded Tailscale (`network: tsnet`). The auth key
// is never here: it lives in the keychain or the environment.
type TSNet struct {
	// ControlURL is the coordination server; empty is Tailscale's own.
	// A Headscale URL self-hosts it.
	ControlURL string `yaml:"control_url,omitempty"`
	// Tags the device advertises (default tag:barnraise).
	Tags []string `yaml:"tags,omitempty"`
}

// NetworkPlugin is the network plugin of `network: plugin`: a program that
// tells Barnraise where this machine and the others are on another overlay
// network (docs/plugins.md, kind network). Same keys and rules as a
// project's plugins; secrets are references, never values.
type NetworkPlugin struct {
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args,omitempty"`
	Env     []string          `yaml:"env,omitempty"`     // names of variables passed through
	Secrets map[string]string `yaml:"secrets,omitempty"` // variable -> keychain:<key> or env:<NAME>
	Timeout string            `yaml:"timeout,omitempty"` // each call (default 10s)
	Options map[string]any    `yaml:"options,omitempty"` // sent in initialize
}

// NetworkPluginTimeout is a network plugin's default call timeout.
const NetworkPluginTimeout = 10 * time.Second

// Def returns the plugin's definition, checked. A relative command must
// be absolute or start with ~/: the machine has no repository to anchor it.
func (n *NetworkPlugin) Def() (domain.PluginDef, error) {
	if n == nil {
		return domain.PluginDef{}, errors.New("network_plugin is not set in daemon.yaml")
	}
	d := domain.PluginDef{Command: strings.TrimSpace(n.Command), Args: n.Args, Env: n.Env, Secrets: n.Secrets, Options: n.Options, Timeout: NetworkPluginTimeout}
	var errs []error
	if n.Timeout != "" {
		t, err := domain.ParseDuration(n.Timeout)
		if err != nil {
			errs = append(errs, fmt.Errorf("network_plugin.timeout: %w", err))
		} else {
			d.Timeout = t
		}
	}
	errs = append(errs, d.Validate("network_plugin")...)
	if c := d.Command; strings.Contains(c, "/") && !filepath.IsAbs(c) && !strings.HasPrefix(c, "~/") {
		errs = append(errs, fmt.Errorf("network_plugin: command %q must be a name on the PATH, an absolute path or ~/…", c))
	}
	return d, errors.Join(errs...)
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
	// Standby keeps the daemon from starting any work on this machine
	// (`barnraise standby on`); StandbyWhy is the owner's note.
	Standby    bool   `yaml:"standby,omitempty"`
	StandbyWhy string `yaml:"standby_why,omitempty"`
	Network    string `yaml:"network,omitempty"` // empty (auto), tailscale, tsnet, plugin, lan
	TSNet      TSNet  `yaml:"tsnet,omitempty"`
	// NetworkPlugin is used with `network: plugin`.
	NetworkPlugin *NetworkPlugin `yaml:"network_plugin,omitempty"`
	Notify        []struct {
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
	if slices.Contains(f.Projects, root) {
		return false
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
