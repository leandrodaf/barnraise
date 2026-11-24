// Command barnraise is the composition root: it wires adapters to ports in
// one binary that is the daemon, the CLI, the MCP server and the hooks.
package main

import (
	"os"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/in/cli"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/adapters/out/toolinstall"
	"github.com/leandrodaf/barnraise/internal/paths"
)

// version and buildTime are set at build time with
// -ldflags "-X main.version=… -X main.buildTime=…" (the commit's time, RFC 3339).
var (
	version   = "0.1.0-dev"
	buildTime = ""
)

// selfBuild is this binary's build, to compare with the other machines'.
func selfBuild() domain.Build {
	t, _ := time.Parse(time.RFC3339, buildTime)
	return domain.Build{Version: version, Time: t.UTC()}
}

func main() {
	// The tools Barnraise installed (barnraise tools install) come first on
	// the PATH of every lookup and of every program it starts.
	toolinstall.UsePATH(toolinstall.Root(paths.DataDir()))
	os.Exit(cli.Main(os.Args[1:], cli.Hooks{Version: version, RunDaemon: runDaemon}))
}
