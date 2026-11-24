package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// sandbox-exec refuses a denied command before running anything, with exit
// code 126 and the rule; it never runs a command unconfined.
func TestSandboxExecRefuses(t *testing.T) {
	var out, errOut bytes.Buffer
	e := &env{out: &out, errOut: &errOut}
	spec, _ := json.Marshal(domain.Confinement{Profile: "check", DenyCommands: []string{`\bgit\s+push\b`}})
	c := e.sandboxExecCmd()
	c.SetArgs([]string{"--spec", string(spec), "--", "sh", "-c", "git push origin main"})
	c.SetOut(&out)
	c.SetErr(&errOut)
	err := c.Execute()
	if ec, ok := err.(exitCode); !ok || ec != 126 {
		t.Fatalf("exit: %v", err)
	}
	if !strings.Contains(errOut.String(), "refused by the sandbox profile check") {
		t.Fatalf("message: %s", errOut.String())
	}
}

func TestSandboxStatus(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BARNRAISE_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "daemon.yaml"), []byte("machine: m\nisolation:\n  backend: microvm\n  microvm: { runner: no-such-runner-xyz, image: img }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	e := &env{out: &out, errOut: &out}
	c := e.sandboxCmd()
	c.SetArgs([]string{"status"})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"bubblewrap", "landlock", "microvm", "not installed", "check   network local"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	out.Reset()
	e.json = true
	c = e.sandboxCmd()
	c.SetArgs([]string{"status"})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	var st sandboxStatus
	if err := json.Unmarshal(out.Bytes(), &st); err != nil || len(st.Layers) != 2 || len(st.Profiles) != len(domain.SandboxProfileNames()) {
		t.Fatalf("json: %v %s", err, out.String())
	}
}
