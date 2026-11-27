package main

import (
	"os"
	"os/exec"
	"testing"
)

func TestDecodeWSL(t *testing.T) {
	utf16 := []byte{0xff, 0xfe, 'U', 0, 'b', 0, '\r', 0, '\n', 0}
	if got := decodeWSL(utf16); got != "Ub\n" {
		t.Fatalf("%q", got)
	}
	if got := decodeWSL([]byte("Ubuntu\r\n")); got != "Ubuntu\n" {
		t.Fatalf("%q", got)
	}
}

func TestPickDistro(t *testing.T) {
	out := "  NOME              ESTADO          VERSÃO\n* docker-desktop    Running         2\n  Ubuntu-24.04      Stopped         2\n  Debian            Running         2\n"
	ds := parseDistros(out)
	if len(ds) != 3 || !ds[0].Default || ds[1].Name != "Ubuntu-24.04" || ds[2].Version != "2" {
		t.Fatalf("%+v", ds)
	}
	// A helper distribution is never chosen, even as the default.
	if d, err := pickDistro(ds, ""); err != nil || d != "Ubuntu-24.04" {
		t.Fatalf("%q %v", d, err)
	}
	if d, err := pickDistro(ds, "debian"); err != nil || d != "Debian" {
		t.Fatalf("%q %v", d, err)
	}
	if _, err := pickDistro(ds, "Arch"); err == nil {
		t.Fatal("a distribution that is not installed must be an error")
	}
	if _, err := pickDistro(parseDistros("  NAME STATE VERSION\n* docker-desktop Running 2\n"), ""); err != errNoDistro {
		t.Fatalf("%v", err)
	}
	if d, _ := pickDistro(parseDistros("  NAME STATE VERSION\n  Debian Stopped 2\n* Ubuntu Running 2\n"), ""); d != "Ubuntu" {
		t.Fatalf("the default one first, got %q", d)
	}
}

func TestParseProbe(t *testing.T) {
	p := parseProbe("bin=/home/a/.local/bin/barnraise\r\nuid=1000\nservice=1\nlisten='127.0.0.1:7500'\ntoken=bro_x\nalive=1\ndl=1\n")
	if p.Bin != "/home/a/.local/bin/barnraise" || p.Root || !p.Service || p.Listen != "127.0.0.1:7500" || p.Token != "bro_x" || !p.Alive || !p.DL {
		t.Fatalf("%+v", p)
	}
	if p := parseProbe("bin=\nuid=0\nlisten=\ntoken=\n"); p.Bin != "" || !p.Root || p.Token != "" || p.Service {
		t.Fatalf("%+v", p)
	}
}

// The scripts run in the distribution's sh: they must at least parse, and
// the probe must read a daemon's files where internal/paths puts them.
func TestWSLScripts(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	for name, s := range map[string]string{"probe": wslProbe, "run": wslRunDaemon, "keep": wslKeepService, "log": wslLogTail, "install": installScript} {
		if out, err := exec.Command("sh", "-n", "-c", s).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v %s", name, err, out)
		}
	}
	cfg := t.TempDir()
	if err := writeFile(cfg+"/daemon.yaml", "# Written by barnraise\nmachine: x\nlisten: 127.0.0.1:7555\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(cfg+"/owner.token", "bro_secret\n"); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", wslProbe)
	cmd.Env = append(cmd.Environ(), "BARNRAISE_CONFIG_DIR="+cfg, "PATH=/usr/bin:/bin", "HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	p := parseProbe(string(out))
	if p.Listen != "127.0.0.1:7555" || p.Token != "bro_secret" || p.Bin != "" {
		t.Fatalf("%+v\n%s", p, out)
	}
}

func writeFile(p, s string) error { return os.WriteFile(p, []byte(s), 0o600) }
