// Package tailnet finds this machine and its peers on the Tailscale network,
// listens on it and dials through it.
//
// Two implementations share the Tailnet interface:
//
//   - Network drives the installed `tailscale` command. The operating
//     system routes the Tailscale addresses, so listening and dialing are
//     plain TCP.
//   - Embedded (built with the `tsnet` build tag) makes Barnraise itself a
//     device of the tailnet with tailscale.com/tsnet: nothing else to
//     install. Its addresses exist only inside the process, so every
//     listener and every connection to a peer must go through it.
//
// tsnet pulls the whole tailscale.com module (hundreds of packages, about
// 13 MB of binary) in, so the default build leaves it out; release builds
// carry it. Config.Resolve picks the implementation (see select.go).
package tailnet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"time"
)

// Tag is the ACL tag every Barnraise machine carries.
const Tag = "tag:barnraise"

// PairingPort is the TCP port of `barnraise machine invite`.
const PairingPort = 47900

// ErrUnavailable means Tailscale is not installed or not running.
var ErrUnavailable = errors.New("tailnet: tailscale is not available")

// Self is this machine on the tailnet.
type Self struct {
	Name    string   `json:"name"`
	DNSName string   `json:"dns_name"`
	IPs     []string `json:"ips"`
	Online  bool     `json:"online"`
	Tags    []string `json:"tags,omitempty"`
}

// Peer is another device on the tailnet.
type Peer struct {
	Name     string    `json:"name"`
	DNSName  string    `json:"dns_name"`
	IPs      []string  `json:"ips"`
	OS       string    `json:"os,omitempty"`
	Online   bool      `json:"online"`
	Tags     []string  `json:"tags,omitempty"`
	LastSeen time.Time `json:"last_seen"`
}

// HasTag reports whether the peer carries a tag.
func (p Peer) HasTag(tag string) bool {
	return slices.Contains(p.Tags, tag)
}

// IPv4 returns the first IPv4 address, or "".
func IPv4(ips []string) string {
	for _, s := range ips {
		if ip := net.ParseIP(s); ip != nil && ip.To4() != nil {
			return s
		}
	}
	return ""
}

// Tailnet is this machine's view of the Tailscale network, whichever way it
// is joined.
type Tailnet interface {
	// Mode says which implementation this is (ModeTailscale or ModeTsnet).
	Mode() Mode
	// Available reports whether the tailnet is up and this machine has an address.
	Available() bool
	// Status returns this machine and the other devices of the tailnet.
	Status(ctx context.Context) (Self, []Peer, error)
	// Listen listens on this machine's tailnet address (":port").
	Listen(ctx context.Context, addr string) (net.Listener, error)
	// Dial connects to a tailnet address (or any other address).
	Dial(ctx context.Context, network, addr string) (net.Conn, error)
	// Close leaves the tailnet when this process joined it itself.
	Close() error
}

var _ Tailnet = (*Network)(nil)

// Runner runs a command and returns its stdout.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Network talks to the local Tailscale daemon through its command.
type Network struct {
	// Bin is the tailscale command (default "tailscale" on PATH).
	Bin string
	// Run executes commands; tests replace it.
	Run Runner
}

// New returns a Network using the tailscale command on PATH.
func New() *Network { return &Network{} }

func (n *Network) bin() string {
	if n.Bin != "" {
		return n.Bin
	}
	return "tailscale"
}

func (n *Network) run(ctx context.Context, args ...string) ([]byte, error) {
	if n.Run != nil {
		return n.Run(ctx, n.bin(), args...)
	}
	path, err := exec.LookPath(n.bin())
	if err != nil {
		return nil, ErrUnavailable
	}
	out, err := exec.CommandContext(ctx, path, args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return out, fmt.Errorf("tailnet: %s %s: %w: %s", n.bin(), strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return out, fmt.Errorf("tailnet: %s %s: %w", n.bin(), strings.Join(args, " "), err)
	}
	return out, nil
}

// Status reads `tailscale status --json`.
func (n *Network) Status(ctx context.Context) (Self, []Peer, error) {
	out, err := n.run(ctx, "status", "--json")
	if err != nil && len(out) == 0 {
		return Self{}, nil, err
	}
	return ParseStatus(out)
}

// Available reports whether Tailscale is installed, logged in and running.
func (n *Network) Available() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := n.run(ctx, "status", "--json")
	if err != nil && len(out) == 0 {
		return false
	}
	var st rawStatus
	if json.Unmarshal(out, &st) != nil {
		return false
	}
	return st.BackendState == "Running"
}

// Listen listens on the Tailscale address of this machine. An addr with an
// empty host (":4222") binds the machine's Tailscale IPv4, so the port is
// not exposed on other interfaces; an explicit host is used as given.
func (n *Network) Listen(ctx context.Context, addr string) (net.Listener, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if host == "" {
		self, _, err := n.Status(ctx)
		if err != nil {
			return nil, err
		}
		if host = IPv4(self.IPs); host == "" {
			if len(self.IPs) == 0 {
				return nil, fmt.Errorf("%w: no tailscale address", ErrUnavailable)
			}
			host = self.IPs[0]
		}
	}
	var lc net.ListenConfig
	return lc.Listen(ctx, "tcp", net.JoinHostPort(host, port))
}

// Dial connects with the system dialer: the installed Tailscale routes the
// tailnet addresses for the whole machine.
func (n *Network) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

// Mode is ModeTailscale.
func (n *Network) Mode() Mode { return ModeTailscale }

// Close does nothing: the installed Tailscale outlives this process.
func (n *Network) Close() error { return nil }

type rawStatus struct {
	BackendState string             `json:"BackendState"`
	TailscaleIPs []string           `json:"TailscaleIPs"`
	Self         *rawPeer           `json:"Self"`
	Peer         map[string]rawPeer `json:"Peer"`
}

type rawPeer struct {
	HostName     string    `json:"HostName"`
	DNSName      string    `json:"DNSName"`
	OS           string    `json:"OS"`
	TailscaleIPs []string  `json:"TailscaleIPs"`
	Online       bool      `json:"Online"`
	Tags         []string  `json:"Tags"`
	LastSeen     time.Time `json:"LastSeen"`
}

// ParseStatus decodes the JSON of `tailscale status --json`. Peers are
// sorted by name.
func ParseStatus(data []byte) (Self, []Peer, error) {
	var st rawStatus
	if err := json.Unmarshal(data, &st); err != nil {
		return Self{}, nil, fmt.Errorf("tailnet: status: %w", err)
	}
	if st.Self == nil {
		return Self{}, nil, fmt.Errorf("%w: state %q", ErrUnavailable, st.BackendState)
	}
	ips := st.Self.TailscaleIPs
	if len(ips) == 0 {
		ips = st.TailscaleIPs
	}
	self := Self{
		Name:    st.Self.HostName,
		DNSName: strings.TrimSuffix(st.Self.DNSName, "."),
		IPs:     ips,
		Online:  st.BackendState == "Running" && st.Self.Online,
		Tags:    st.Self.Tags,
	}
	peers := make([]Peer, 0, len(st.Peer))
	for _, p := range st.Peer {
		peers = append(peers, Peer{
			Name:     p.HostName,
			DNSName:  strings.TrimSuffix(p.DNSName, "."),
			IPs:      p.TailscaleIPs,
			OS:       p.OS,
			Online:   p.Online,
			Tags:     p.Tags,
			LastSeen: p.LastSeen,
		})
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].Name < peers[j].Name })
	if st.BackendState != "Running" {
		return self, peers, fmt.Errorf("%w: state %q", ErrUnavailable, st.BackendState)
	}
	return self, peers, nil
}

// BarnraisePeers keeps the peers that carry the Barnraise tag.
func BarnraisePeers(peers []Peer) []Peer {
	var out []Peer
	for _, p := range peers {
		if p.HasTag(Tag) {
			out = append(out, p)
		}
	}
	return out
}

// ACLSnippet is the recommended tailnet policy for the owner: machines
// tagged tag:barnraise reach each other only on the Barnraise ports, and
// nothing else in the owner's tailnet is reachable from them. ports are the
// TCP ports to open (NATS, pairing, API); defaults to 4222 (NATS) and
// 47900 (pairing).
func ACLSnippet(owner string, ports ...int) string {
	if owner == "" {
		owner = "autogroup:admin"
	}
	if len(ports) == 0 {
		ports = []int{4222, PairingPort}
	}
	dst := make([]string, len(ports))
	for i, p := range ports {
		dst[i] = fmt.Sprintf("%s:%d", Tag, p)
	}
	policy := map[string]any{
		"tagOwners": map[string][]string{Tag: {owner}},
		"acls": []map[string]any{{
			"action": "accept",
			"src":    []string{Tag},
			"dst":    dst,
		}},
		"tests": []map[string]any{{
			"src":    Tag,
			"accept": dst,
		}},
	}
	b, _ := json.MarshalIndent(policy, "", "  ")
	return string(b)
}
