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

func TestNetworkPlugin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.yaml")
	yml := "network: plugin\nnetwork_plugin:\n  command: /usr/local/bin/zt-plugin\n  args: [--network, 8056c2e21c000001]\n  env: [ZT_HOME]\n  secrets: {ZT_TOKEN: \"keychain:zerotier\"}\n  timeout: 5s\n  options: {tag_all: true}\n"
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	d, err := f.NetworkPlugin.Def()
	if err != nil || d.Command != "/usr/local/bin/zt-plugin" || len(d.Args) != 2 || d.Env[0] != "ZT_HOME" || d.Secrets["ZT_TOKEN"] != "keychain:zerotier" || d.Timeout.Seconds() != 5 || d.Options["tag_all"] != true {
		t.Fatalf("%+v %v", d, err)
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	if g, err := LoadFrom(path); err != nil || g.NetworkPlugin == nil || g.NetworkPlugin.Secrets["ZT_TOKEN"] != "keychain:zerotier" {
		t.Fatal(g, err)
	}
	if d, err := (&NetworkPlugin{Command: "~/bin/net"}).Def(); err != nil || d.Timeout != NetworkPluginTimeout {
		t.Fatal(d, err)
	}
	if _, err := (*NetworkPlugin)(nil).Def(); err == nil {
		t.Fatal("no plugin accepted")
	}
	_, err = (&NetworkPlugin{Command: "plugins/net.py", Secrets: map[string]string{"TOKEN": "abc"}, Timeout: "soon"}).Def()
	for _, want := range []string{"must be a name on the PATH", "secret TOKEN must be a reference", "network_plugin.timeout"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}
