//go:build linux

package main

/*
#cgo pkg-config: glib-2.0
#include <stdlib.h>
#include <glib.h>
*/
import "C"

import "unsafe"

// Wails sets the program name only after GTK has started, too late for the
// X11 window class, which then reads "barnraise-desktop". Setting it before
// GTK starts gives the window class and the Wayland app id the app's id, so
// the desktop matches the window to its .desktop entry and its icon.
func init() {
	name := C.CString(appID)
	defer C.free(unsafe.Pointer(name))
	C.g_set_prgname(name) // copies the string
}
