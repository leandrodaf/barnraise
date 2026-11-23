package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/leandrodaf/barnraise/internal/adapters/out/keychain"
	"github.com/leandrodaf/barnraise/internal/adapters/out/tailnet"
	"github.com/leandrodaf/barnraise/internal/daemoncfg"
	"github.com/leandrodaf/barnraise/internal/paths"
)

// TailnetStateDir is where the embedded Tailscale keeps its keys: under the
// data dir, 0700.
func TailnetStateDir() string { return filepath.Join(paths.DataDir(), "tsnet") }

// TailnetConfig is this machine's tailnet configuration, from daemon.yaml,
// the environment and the keychain. The auth key is read only when the
// embedded Tailscale logs in, and never logged.
func TailnetConfig(df *daemoncfg.File, logf func(string, ...any)) tailnet.Config {
	mode, err := tailnet.ParseMode(df.Network)
	if err != nil {
		mode = tailnet.Mode(df.Network) // Resolve reports it
	}
	return tailnet.Config{
		Mode:       mode,
		Disabled:   os.Getenv("BARNRAISE_NO_TAILSCALE") != "",
		Hostname:   df.Machine,
		StateDir:   TailnetStateDir(),
		ControlURL: df.TSNet.ControlURL,
		Tags:       df.TSNet.Tags,
		AuthKey:    tailnetAuthKey,
		Logf:       logf,
	}
}

func tailnetAuthKey() string {
	return tailnet.LookupAuthKey(os.Getenv, keychain.New("barnraise", filepath.Join(paths.ConfigDir(), "secrets")).Get)
}

// pairNet is the tailnet a pairing command works through.
type pairNet struct {
	tn      tailnet.Tailnet // nil: the local network only
	host    string          // where peers reach this machine's NATS
	own     bool            // tn is this process's own embedded device
	cleanup func()
}

func (p *pairNet) Close() {
	if p.tn != nil {
		_ = p.tn.Close()
		p.tn = nil
	}
	if p.cleanup != nil {
		p.cleanup()
		p.cleanup = nil
	}
}

// pairingNet joins the tailnet for `machine invite` and `machine join`.
// While the daemon holds the embedded device, the command cannot open it:
// the daemon relays the pairing port to this command and its address is
// read from the state directory; a joining command opens a short-lived
// ephemeral device instead when it has an auth key.
func (e *env) pairingNet(ctx context.Context, df *daemoncfg.File, joining bool) (*pairNet, error) {
	logf := func(format string, args ...any) { fmt.Fprintf(e.errOut, "tailscale: "+format+"\n", args...) }
	cfg := TailnetConfig(df, logf)
	p := &pairNet{host: df.NATSHost}
	tn, mode, err := tailnet.Open(ctx, cfg)
	switch {
	case errors.Is(err, tailnet.ErrBusy):
		if self, err := tailnet.ReadSelf(cfg.StateDir); err == nil {
			if ip := tailnet.IPv4(self.IPs); ip != "" {
				p.host = ip
			}
		}
		if joining && cfg.AuthKey() != "" {
			eph := cfg
			eph.Mode, eph.Ephemeral = tailnet.ModeTsnet, true
			eph.StateDir = cfg.StateDir + "-pairing"
			eph.Hostname = df.Machine + "-pairing"
			if t, _, err := tailnet.Open(ctx, eph); err == nil {
				p.tn = t
				p.cleanup = func() { os.RemoveAll(eph.StateDir) }
			} else {
				fmt.Fprintf(e.errOut, "The embedded Tailscale is in use by the daemon and a pairing device could not start (%v); joining over the local network.\n", err)
			}
		}
		return p, nil
	case errors.Is(err, tailnet.ErrNotBuiltIn):
		return nil, err
	case err != nil:
		if mode == "" {
			return nil, err
		}
		fmt.Fprintf(e.errOut, "Tailscale is not available (%v); pairing over the local network.\n", err)
		return p, nil
	case tn == nil:
		return p, nil
	}
	if !tn.Available() {
		_ = tn.Close()
		return p, nil
	}
	p.tn, p.own = tn, mode == tailnet.ModeTsnet
	if self, _, err := tn.Status(ctx); err == nil {
		if ip := tailnet.IPv4(self.IPs); ip != "" {
			p.host = ip
		}
	}
	return p, nil
}

// candidates are the pairing addresses of the online Barnraise machines of
// the tailnet, other than this one.
func (p *pairNet) candidates(ctx context.Context, self string) []string {
	if p.tn == nil {
		return nil
	}
	_, peers, err := p.tn.Status(ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, peer := range tailnet.BarnraisePeers(peers) {
		ip := tailnet.IPv4(peer.IPs)
		if !peer.Online || ip == "" || ip == p.host || peer.Name == self || peer.Name == self+"-pairing" {
			continue
		}
		out = append(out, net.JoinHostPort(ip, strconv.Itoa(tailnet.PairingPort)))
	}
	return out
}

// networkCmds are `machine network` and `machine network-key`.
func (e *env) networkCmds() []*cobra.Command {
	var controlURL string
	network := &cobra.Command{Use: "network [auto|tailscale|tsnet|lan]", Short: "Show or choose how this machine reaches the others",
		Long: "auto uses the installed tailscale, then the embedded Tailscale when it has an auth key or logged in before, then the local network.\n" +
			"tsnet makes barnraise itself a device of the tailnet (release builds); --control-url points it at a Headscale server.\n" +
			"lan uses no tailnet: the local network or a direct address.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			df, err := daemoncfg.Load()
			if err != nil {
				return err
			}
			if len(args) == 1 || cmd.Flags().Changed("control-url") {
				if len(args) == 1 {
					m, err := tailnet.ParseMode(args[0])
					if err != nil {
						return err
					}
					if m == tailnet.ModeTsnet && !tailnet.Embeddable {
						return tailnet.ErrNotBuiltIn
					}
					df.Network = string(m)
				}
				if cmd.Flags().Changed("control-url") {
					df.TSNet.ControlURL = strings.TrimSpace(controlURL)
				}
				if err := df.Save(); err != nil {
					return err
				}
			}
			cfg := TailnetConfig(df, nil)
			m, err := cfg.Resolve()
			if err != nil {
				return err
			}
			fmt.Fprintf(e.out, "network: %s (configured: %s)\n", m, def(df.Network, "auto"))
			if m == tailnet.ModeTsnet || df.Network == string(tailnet.ModeTsnet) {
				fmt.Fprintf(e.out, "control server: %s\nstate: %s\nauth key: %s\n", def(df.TSNet.ControlURL, "Tailscale"), cfg.StateDir, map[bool]string{true: "set", false: "not set"}[cfg.AuthKey() != ""])
			}
			if len(args) == 1 && e.daemonRunning() {
				fmt.Fprintln(e.out, "Restart the daemon to switch: `barnraise down`, then `barnraise up`.")
			}
			return nil
		}}
	network.Flags().StringVar(&controlURL, "control-url", "", "coordination server of the embedded Tailscale (a Headscale URL; empty: Tailscale's)")
	var forget bool
	key := &cobra.Command{Use: "network-key", Short: "Store the Tailscale auth key the embedded Tailscale logs in with (read from standard input)",
		Long: "Reads a Tailscale (or Headscale) auth key from standard input and keeps it in the system keychain.\n" +
			"Create it tagged tag:barnraise (reusable when several machines use it). It is never written to daemon.yaml or logged.\n" +
			"BARNRAISE_TS_AUTHKEY in the environment takes precedence.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ks := keychain.New("barnraise", filepath.Join(paths.ConfigDir(), "secrets"))
			if forget {
				if err := ks.Delete(tailnet.AuthKeySecret); err != nil {
					return err
				}
				fmt.Fprintln(e.out, "✔ Auth key removed.")
				return nil
			}
			fmt.Fprint(e.out, "Auth key: ")
			var v string
			sc := bufio.NewScanner(e.in)
			if sc.Scan() {
				v = strings.TrimSpace(sc.Text())
			}
			if v == "" {
				return errors.New("no auth key given")
			}
			if err := ks.Set(tailnet.AuthKeySecret, v); err != nil {
				return err
			}
			fmt.Fprintf(e.out, "\n✔ Auth key stored in the %s.\n", ks.Backend())
			return nil
		}}
	key.Flags().BoolVar(&forget, "forget", false, "remove the stored auth key")
	return []*cobra.Command{network, key}
}

// pairingTimeout bounds a join, the embedded Tailscale's login included.
const pairingTimeout = 2 * time.Minute
