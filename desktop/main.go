// Command barnraise-desktop is Barnraise's desktop app: a window on the
// panel of a Barnraise daemon. It is its own Go module (Wails needs cgo and
// WebKit; the barnraise binary stays pure Go) and never runs the daemon
// inside itself: it finds the daemon of this machine, starts it as its own
// process when the owner asks, or connects to another machine's. Closing or
// updating the app never stops the agents.
package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
)

// appVersion is set at build time with -ldflags "-X main.appVersion=…".
var appVersion = "dev"

//go:embed all:frontend/dist
var assets embed.FS

// appID names the app to the desktop: the window's class and Wayland app id
// match build/linux/io.github.leandrodaf.barnraise.desktop, so the dock and
// the task switcher show the app's own icon and name.
const appID = "io.github.leandrodaf.barnraise"

// icon is the window's icon on X11 (Wayland and the dock use the .desktop
// entry's). macOS and Windows take theirs from the bundle and the .exe.
//
//go:embed build/linux/icons/hicolor/256x256/apps/io.github.leandrodaf.barnraise.png
var icon []byte

func main() {
	app := NewApp()
	err := wails.Run(&options.App{
		Title:            "Barnraise",
		Width:            1360,
		Height:           880,
		MinWidth:         900,
		MinHeight:        600,
		AssetServer:      &assetserver.Options{Assets: assets},
		BackgroundColour: &options.RGBA{R: 9, G: 9, B: 11, A: 1},
		OnStartup:        app.startup,
		Bind:             []any{app},
		Linux:            &linux.Options{ProgramName: appID, Icon: icon, WebviewGpuPolicy: linux.WebviewGpuPolicyOnDemand},
	})
	if err != nil {
		log.Fatal(err)
	}
}
