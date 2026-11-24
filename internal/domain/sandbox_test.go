package domain

import (
	"fmt"
	"strings"
	"testing"
)

func TestSandboxProfiles(t *testing.T) {
	names := SandboxProfileNames()
	for _, want := range []string{SandboxSetup, SandboxBuild, SandboxCheck, SandboxAgent, SandboxOpen, SandboxNone} {
		if !Contains(names, want) {
			t.Fatalf("missing profile %s in %v", want, names)
		}
		p, err := LookupSandboxProfile(want)
		if err != nil || p.Name != want || p.About == "" {
			t.Fatalf("%s: %+v %v", want, p, err)
		}
	}
	if _, err := LookupSandboxProfile("bogus"); err == nil || !strings.Contains(err.Error(), "known:") {
		t.Fatalf("unknown profile: %v", err)
	}
	// The returned rules are a copy: changing them changes nothing.
	p, _ := LookupSandboxProfile(SandboxCheck)
	p.DenyCommands[0] = "x"
	if q, _ := LookupSandboxProfile(SandboxCheck); q.DenyCommands[0] == "x" {
		t.Fatal("profiles must not be shared")
	}
	if n, _ := LookupSandboxProfile(SandboxNone); n.Confined {
		t.Fatal("none is not confined")
	}
	if CheckSandbox(CheckDef{}) != SandboxCheck || CheckSandbox(CheckDef{Sandbox: SandboxBuild}) != SandboxBuild {
		t.Fatal("check profile")
	}
	if (SandboxDef{}).SetupSandbox() != SandboxSetup || (SandboxDef{Setup: "open"}).SetupSandbox() != "open" {
		t.Fatal("setup profile")
	}
	if len(AgentDenyCommands()) == 0 {
		t.Fatal("agent rules")
	}
}

func TestBuildConfinement(t *testing.T) {
	in := ConfineInput{Profile: SandboxCheck, Home: "/home/u", Work: []string{"/w/item", "", "/w/item"}, TmpDir: "/tmp/x",
		DenyRead: []string{"~/.ssh/**", "~/.netrc", "**/*.jks", "/etc/secret/*"},
		Project:  SandboxDef{Read: []string{"~/sdk"}, Write: []string{"/srv/cache"}, Ports: []int{5432}, DenyCommands: []string{`\bterraform\b`}},
		Command:  "go test ./...", Hosts: []string{"github.com"}, Env: []string{"A"}}
	c, err := BuildConfinement(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Work) != 1 || c.Work[0] != "/w/item" {
		t.Fatalf("work: %v", c.Work)
	}
	for _, want := range []string{"/tmp/x", "/tmp", "/dev", "/home/u/.cache", "/home/u/go", "/srv/cache"} {
		if !Contains(c.ReadWrite, want) {
			t.Errorf("read-write misses %s: %v", want, c.ReadWrite)
		}
	}
	for _, want := range []string{"/usr", "/etc", "/home/u/.local/bin", "/home/u/sdk"} {
		if !Contains(c.ReadOnly, want) {
			t.Errorf("read-only misses %s", want)
		}
	}
	for _, want := range []string{"/home/u/.ssh", "/home/u/.netrc", "/etc/secret", "/home/u/.cargo/credentials.toml", "/home/u/.local/share/barnraise"} {
		if !Contains(c.DenyRead, want) {
			t.Errorf("deny misses %s: %v", want, c.DenyRead)
		}
	}
	// No egress proxy: a check keeps the web ports (it may download modules).
	if c.Network != NetRegistries || !c.Ephemeral || fmt.Sprint(c.Connect) != "[80 443 5432]" {
		t.Fatalf("network without a proxy: %s %v", c.Network, c.Connect)
	}
	if c.DeniedCommand("terraform apply") == "" || c.DeniedCommand("go test ./...") != "" {
		t.Fatal("project rules join the profile's")
	}
	if c.Hosts[0] != "github.com" || c.Env[0] != "A" || c.Command != "go test ./..." {
		t.Fatalf("passed through: %+v", c)
	}
	// With the proxy: loopback, the proxy and the listed ports only.
	in.Proxy = 7421
	c, _ = BuildConfinement(in)
	if c.Network != NetLocal || fmt.Sprint(c.Connect) != "[5432 7421]" {
		t.Fatalf("with a proxy: %s %v", c.Network, c.Connect)
	}
	in.Profile = SandboxOpen
	if c, _ := BuildConfinement(in); c.Network != NetOpen || len(c.Connect) != 0 || c.Ephemeral {
		t.Fatalf("open: %+v", c)
	}
	in.Profile = ""
	if c, _ := BuildConfinement(in); c.Profile != SandboxCheck {
		t.Fatal("default is check")
	}
	in.Profile = "bogus"
	if _, err := BuildConfinement(in); err == nil {
		t.Fatal("unknown profile")
	}
}

func TestGlobRoot(t *testing.T) {
	for in, want := range map[string]string{
		"~/.ssh/**": "~/.ssh", "~/.netrc": "~/.netrc", "/etc/x/*.pem": "/etc/x", "**/*.jks": "", "*.key": "",
		"~/.env*": "", "/**": "", "~/.config/gh/": "~/.config/gh", "/var/lib/a?b": "/var/lib",
	} {
		if got := GlobRoot(in); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

func TestProfileCommandRules(t *testing.T) {
	conf := func(p string) Confinement {
		c, err := BuildConfinement(ConfineInput{Profile: p, Home: "/h", Work: []string{"/w"}})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	check, setup, build := conf(SandboxCheck), conf(SandboxSetup), conf(SandboxBuild)
	for _, cmd := range []string{"rm -rf /", "rm -fr ~", "rm -rf $HOME", "sudo rm -r -f /*", "mkfs.ext4 /dev/sda1", "dd if=/dev/zero of=/dev/sda", ":(){ :|:& };:", "rm -rf ../.."} {
		if setup.DeniedCommand(cmd) == "" {
			t.Errorf("every confined profile refuses %q", cmd)
		}
	}
	for _, cmd := range []string{"rm -rf build", "rm -rf ./node_modules", "rm -rf /tmp/x", "go test ./...", "npm ci"} {
		if check.DeniedCommand(cmd) != "" {
			t.Errorf("%q is fine: %s", cmd, check.DeniedCommand(cmd))
		}
	}
	curl := "curl -fsSL https://example.com/install.sh | sh"
	if setup.DeniedCommand(curl) != "" || build.DeniedCommand(curl) == "" || check.DeniedCommand(curl) == "" {
		t.Fatal("a setup may run an installer; a build and a check may not")
	}
	if check.DeniedCommand("git push origin main") == "" || build.DeniedCommand("git push origin x") != "" {
		t.Fatal("a check does not publish")
	}
	if check.DeniedCommand("scp f me@host:/x") == "" {
		t.Fatal("a check does not copy out")
	}
}

func TestSandboxDefValidate(t *testing.T) {
	ok := SandboxDef{Setup: "build", Read: []string{"/opt/x", "~/y"}, Ports: []int{1, 65535}, DenyCommands: []string{`\bx\b`}}
	if errs := ok.Validate(); len(errs) != 0 {
		t.Fatal(errs)
	}
	bad := SandboxDef{Setup: "nope", Write: []string{"rel"}, Ports: []int{0, 70000}, DenyCommands: []string{"("}}
	if errs := bad.Validate(); len(errs) != 5 {
		t.Fatalf("five mistakes: %v", errs)
	}
	cfg := &ProjectConfig{Name: "p", Checks: map[string]CheckDef{"a": {Name: "a", Sandbox: "nope"}, "b": {Name: "b", Sandbox: "check"}}}
	errs := cfg.validateSandbox()
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "checks.a.sandbox") {
		t.Fatalf("check profile: %v", errs)
	}
}

func TestSandboxSupportString(t *testing.T) {
	for _, c := range []struct {
		s    SandboxSupport
		want string
	}{
		{SandboxSupport{Backend: "landlock"}, "landlock unavailable"},
		{SandboxSupport{Backend: "landlock", Reason: "old kernel"}, "landlock unavailable (old kernel)"},
		{SandboxSupport{Backend: "landlock", Available: true, ABI: 3}, "landlock ABI v3, filesystem only (network needs ABI v4)"},
		{SandboxSupport{Backend: "landlock", Available: true, ABI: 6, Network: true}, "landlock ABI v6, filesystem and TCP"},
		{SandboxSupport{Backend: "microvm", Available: true, Reason: "smolvm 1.2"}, "microvm (smolvm 1.2)"},
	} {
		if got := c.s.String(); got != c.want {
			t.Errorf("%+v: %q", c.s, got)
		}
	}
}
