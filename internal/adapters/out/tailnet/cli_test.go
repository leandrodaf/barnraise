package tailnet

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLookCLI(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "Tailscale.app", "Contents", "MacOS", "Tailscale")
	if err := os.MkdirAll(filepath.Dir(app), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(app, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(dir, "not-executable")
	if err := os.WriteFile(plain, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	notFound := errors.New("not found")
	none := func(string) (string, error) { return "", notFound }

	// On the PATH: the PATH wins.
	if p, err := lookCLI("", found, []string{app}); err != nil || p != "/usr/bin/tailscale" {
		t.Fatal(p, err)
	}
	// Off the PATH (the macOS app bundle): the known place is found; a
	// directory or a file nobody may run is skipped.
	if p, err := lookCLI("tailscale", none, []string{filepath.Join(dir, "missing"), dir, plain, app}); err != nil || p != app {
		t.Fatal(p, err)
	}
	// Nowhere: the PATH's error.
	if _, err := lookCLI("tailscale", none, []string{plain}); !errors.Is(err, notFound) {
		t.Fatal(err)
	}
	// A configured command other than the default is looked up as given.
	if _, err := lookCLI("my-tailscale", none, []string{app}); !errors.Is(err, notFound) {
		t.Fatal(err)
	}
	// ~/ is the home directory.
	t.Setenv("HOME", dir)
	if p, err := lookCLI("tailscale", none, []string{"~/Tailscale.app/Contents/MacOS/Tailscale"}); err != nil || p != app {
		t.Fatal(p, err)
	}
}

func TestOpenKeepsTheFoundCommand(t *testing.T) {
	look := func(string) (string, error) { return "/Applications/Tailscale.app/Contents/MacOS/Tailscale", nil }
	tn, m, err := Open(t.Context(), Config{LookPath: look})
	if err != nil || m != ModeTailscale {
		t.Fatal(m, err)
	}
	if n, ok := tn.(*Network); !ok || n.Bin != "/Applications/Tailscale.app/Contents/MacOS/Tailscale" {
		t.Fatalf("%#v", tn)
	}
}
