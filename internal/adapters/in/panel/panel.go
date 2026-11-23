// Package panel holds the owner's web panel: plain HTML, CSS and JavaScript
// modules served by the daemon at /. The panel talks to the daemon only
// through the public API (Connect JSON) and the event stream (/v1/events).
package panel

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

// FS returns the static files with index.html at the root.
func FS() fs.FS {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err) // the directory is embedded at build time
	}
	return sub
}
