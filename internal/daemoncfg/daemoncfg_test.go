package daemoncfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNetworkSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.yaml")
	f, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Network != "" || f.TSNet.ControlURL != "" || f.NATSPort != 4222 {
		t.Fatalf("defaults %+v", f)
	}
	f.Network = "tsnet"
	f.TSNet = TSNet{ControlURL: "https://headscale.example", Tags: []string{"tag:barnraise"}}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "control_url: https://headscale.example") || strings.Contains(strings.ToLower(string(b)), "authkey") {
		t.Fatal(string(b))
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatal(st.Mode())
	}
	g, err := LoadFrom(path)
	if err != nil || g.Network != "tsnet" || g.TSNet.ControlURL != "https://headscale.example" || len(g.TSNet.Tags) != 1 {
		t.Fatalf("%+v %v", g, err)
	}
}
