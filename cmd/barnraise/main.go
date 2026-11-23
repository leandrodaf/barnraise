// Command barnraise is the composition root: it wires adapters to ports in
// one binary that is the daemon, the CLI, the MCP server and the hooks.
package main

import (
	"os"

	"github.com/leandrodaf/barnraise/internal/adapters/in/cli"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "0.1.0-dev"

func main() {
	os.Exit(cli.Main(os.Args[1:], cli.Hooks{Version: version, RunDaemon: runDaemon}))
}
