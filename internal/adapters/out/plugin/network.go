package plugin

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// NetworkSelf is this machine as a network plugin reports it.
type NetworkSelf struct {
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
	Tags      []string `json:"tags,omitempty"`
}

// NetworkPeer is another machine of the network.
type NetworkPeer struct {
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
	Online    bool     `json:"online"`
	Tags      []string `json:"tags,omitempty"`
}

// NetworkStatus is what network.status answers.
type NetworkStatus struct {
	Self  NetworkSelf   `json:"self"`
	Peers []NetworkPeer `json:"peers"`
}

// NetworkStatus asks a network plugin for this machine's address and the
// other machines it can reach. The answer is checked: every address must
// be an IP address, this machine needs at least one, and every peer a name.
func (h *Host) NetworkStatus(ctx context.Context) (NetworkStatus, error) {
	if err := h.require(ctx, domain.PluginNetwork); err != nil {
		return NetworkStatus{}, err
	}
	var out NetworkStatus
	if err := h.Call(ctx, "network.status", map[string]any{}, &out, 0); err != nil {
		return NetworkStatus{}, err
	}
	bad := func(format string, args ...any) error {
		return fmt.Errorf("plugin %s answered network.status with %s", h.o.Name, fmt.Sprintf(format, args...))
	}
	if strings.TrimSpace(out.Self.Name) == "" {
		return NetworkStatus{}, bad("no self.name")
	}
	if len(out.Self.Addresses) == 0 {
		return NetworkStatus{}, bad("no address for this machine (self.addresses is empty)")
	}
	if a := badAddress(out.Self.Addresses); a != "" {
		return NetworkStatus{}, bad("self address %q, which is not an IP address", a)
	}
	for i, p := range out.Peers {
		if strings.TrimSpace(p.Name) == "" {
			return NetworkStatus{}, bad("a peer without a name (peer %d)", i+1)
		}
		if a := badAddress(p.Addresses); a != "" {
			return NetworkStatus{}, bad("address %q for peer %s, which is not an IP address", a, p.Name)
		}
	}
	return out, nil
}

// badAddress returns the first entry that is not an IP address, or "".
func badAddress(addrs []string) string {
	for _, a := range addrs {
		if net.ParseIP(a) == nil {
			return a
		}
	}
	return ""
}
