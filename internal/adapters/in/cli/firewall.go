package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/leandrodaf/barnraise/internal/adapters/out/egress"
)

func (e *env) firewallCmd() *cobra.Command {
	c := &cobra.Command{Use: "egress", Short: "Restricted network for agents: allow-list proxy and nftables rules per agent user"}
	var listen string
	var allow []string
	proxy := &cobra.Command{Use: "proxy", Short: "Run the allow-list proxy (the daemon runs it with --egress)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			l, err := net.Listen("tcp", listen)
			if err != nil {
				return err
			}
			p := egress.New(append(append([]string(nil), egress.DefaultAllow...), allow...), nil)
			fmt.Fprintf(e.out, "Allow-list proxy on %s (%d hosts). Agents use HTTPS_PROXY=http://%s\n", l.Addr(), len(egress.DefaultAllow)+len(allow), l.Addr())
			ctx, stop := signalContext()
			defer stop()
			return p.Serve(ctx, l)
		}}
	proxy.Flags().StringVar(&listen, "listen", "127.0.0.1:7421", "address")
	proxy.Flags().StringSliceVar(&allow, "allow", nil, "extra allowed hosts")
	var uname string
	var port int
	var extra []int
	var apply bool
	rules := &cobra.Command{Use: "rules", Short: "Print (or apply with sudo) nftables rules forcing an agent user through the proxy", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if uname == "" {
				return fmt.Errorf("--user is required (the isolated agent user, e.g. barnraise-claude-1)")
			}
			u, err := user.Lookup(uname)
			if err != nil {
				return err
			}
			uid, _ := strconv.Atoi(u.Uid)
			r := egress.NFTRules(uid, port, extra)
			if !apply {
				fmt.Fprint(e.out, r)
				return nil
			}
			c := exec.CommandContext(context.Background(), "sudo", "nft", "-f", "-")
			c.Stdin, c.Stdout, c.Stderr = strings.NewReader(r), os.Stdout, os.Stderr
			if err := c.Run(); err != nil {
				return err
			}
			fmt.Fprintf(e.out, "✔ %s can now reach only the proxy on 127.0.0.1:%d.\n", uname, port)
			return nil
		}}
	rules.Flags().StringVar(&uname, "user", "", "agent Linux user")
	rules.Flags().IntVar(&port, "proxy-port", 7421, "proxy port")
	rules.Flags().IntSliceVar(&extra, "allow-local-port", nil, "loopback ports to allow (emulator: 5554,5555)")
	rules.Flags().BoolVar(&apply, "apply", false, "apply with sudo nft")
	c.AddCommand(proxy, rules)
	return c
}
