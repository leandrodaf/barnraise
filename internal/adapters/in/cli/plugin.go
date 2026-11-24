package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/leandrodaf/barnraise/internal/adapters/out/otlp"
)

// pluginCmd groups the plugins shipped inside the binary: the daemon runs
// them as external plugins (`command: barnraise`, `args: [plugin, <name>]`),
// so they work like any other plugin and stay out of the daemon's process.
func (e *env) pluginCmd() *cobra.Command {
	c := &cobra.Command{Use: "plugin", Short: "Plugins shipped with Barnraise (run by the daemon, not by hand)"}
	c.AddCommand(&cobra.Command{
		Use:   "otel",
		Short: "Hook plugin: export items, steps, checks and the token and cost ledger as OpenTelemetry spans (OTLP/HTTP)",
		Long: `Speaks the plugin protocol on standard input and output. Declare it in barnraise.yaml:

  plugins:
    otel:
      command: barnraise
      args: [plugin, otel]
      events: ["item.*", agent.usage, check.completed]
      options: {endpoint: "http://localhost:4318"}
      secrets: {OTEL_EXPORTER_OTLP_HEADERS: "keychain:otel-headers"}`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return otlp.Serve(ctx, e.in, e.out, otlp.ServeOptions{Version: e.hooks.Version})
		},
	})
	return c
}
