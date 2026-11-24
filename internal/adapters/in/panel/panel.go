// Package panel holds the owner's web panel, built from web/ (Svelte) into
// dist/ and embedded in the daemon, which serves it at /. The panel talks to
// the daemon only through the public API (Connect JSON) and the event stream
// (/v1/events). dist/ is committed, so `go build` needs no JavaScript toolchain; `make web`
// rebuilds it and CI checks that it matches web/.
package panel

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var files embed.FS

// FS returns the built files with index.html at the root.
func FS() fs.FS {
	sub, err := fs.Sub(files, "dist")
	if err != nil {
		panic(err) // the directory is embedded at build time
	}
	return sub
}
