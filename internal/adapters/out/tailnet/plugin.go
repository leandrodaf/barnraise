package tailnet

import (
	"context"
	"errors"
	"net"
	"os"
	"sort"
	"sync/atomic"

	"github.com/leandrodaf/barnraise/internal/adapters/out/plugin"
)

// ErrNoPlugin means `network: plugin` without a network_plugin command.
var ErrNoPlugin = errors.New("tailnet: network: plugin needs network_plugin.command in daemon.yaml (`barnraise machine network plugin --command <program>`)")

// Plugin is a network that a network plugin describes (docs/plugins.md,
// kind network): ZeroTier, Nebula, WireGuard, a company VPN. The overlay
// already routes its addresses for the whole machine, so the plugin only
// reports this machine's address and the peers; listening and dialing are
// plain TCP, as with the installed Tailscale.
type Plugin struct {
	h      *plugin.Host
	closed atomic.Bool
}

var _ Tailnet = (*Plugin)(nil)

// NewPlugin wraps a plugin host; Close stops the program.
func NewPlugin(h *plugin.Host) *Plugin { return &Plugin{h: h} }

// openPlugin starts the network plugin and asks for a first status: a
// plugin that is missing, answers badly or too late leaves the machine on
// the local network.
func openPlugin(ctx context.Context, c Config) (Tailnet, error) {
	o := c.Plugin
	if o.Name == "" {
		o.Name = "network"
	}
	if o.Root != "" {
		// The working directory (the config dir) may not exist yet on a new machine.
		if err := os.MkdirAll(o.Root, 0o700); err != nil {
			return nil, err
		}
	}
	p := NewPlugin(plugin.New(o))
	if _, _, err := p.Status(ctx); err != nil {
		_ = p.Close()
		return nil, err
	}
	return p, nil
}

// pluginDown is ErrUnavailable for a network plugin, with its reason.
type pluginDown struct{ err error }

func (e pluginDown) Error() string {
	return "tailnet: the network plugin is not available: " + e.err.Error()
}
func (e pluginDown) Unwrap() error        { return e.err }
func (e pluginDown) Is(target error) bool { return target == ErrUnavailable }

// Mode is ModePlugin.
func (p *Plugin) Mode() Mode { return ModePlugin }

// Available reports whether the plugin answers with an address for this machine.
func (p *Plugin) Available() bool {
	if p.closed.Load() {
		return false
	}
	self, _, err := p.Status(context.Background())
	return err == nil && len(self.IPs) > 0
}

// Status asks the plugin (network.status) and converts its answer.
func (p *Plugin) Status(ctx context.Context) (Self, []Peer, error) {
	if p.closed.Load() {
		return Self{}, nil, pluginDown{errors.New("it is stopped")}
	}
	st, err := p.h.NetworkStatus(ctx)
	if err != nil {
		return Self{}, nil, pluginDown{err}
	}
	self, peers := FromPlugin(st)
	return self, peers, nil
}

// FromPlugin converts a network.status answer into the shape every caller
// reads (the one ParseStatus returns). This machine is online since the
// plugin answered; peers are sorted by name.
func FromPlugin(st plugin.NetworkStatus) (Self, []Peer) {
	self := Self{Name: st.Self.Name, IPs: st.Self.Addresses, Online: true, Tags: st.Self.Tags}
	peers := make([]Peer, 0, len(st.Peers))
	for _, p := range st.Peers {
		peers = append(peers, Peer{Name: p.Name, IPs: p.Addresses, Online: p.Online, Tags: p.Tags})
	}
	sort.SliceStable(peers, func(i, j int) bool { return peers[i].Name < peers[j].Name })
	return self, peers
}

// Listen listens on this machine's address in the network: ":port" binds
// the address the plugin reports (its first IPv4, else its first), never
// every interface; an explicit host is used as given.
func (p *Plugin) Listen(ctx context.Context, addr string) (net.Listener, error) {
	return listenOn(ctx, addr, p.Status)
}

// Dial connects with the system dialer: the overlay routes its addresses.
func (p *Plugin) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

// Close stops the plugin.
func (p *Plugin) Close() error {
	if p.closed.Swap(true) {
		return nil
	}
	return p.h.Close()
}
