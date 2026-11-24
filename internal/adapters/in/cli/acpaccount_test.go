package cli

import (
	"os"
	"testing"
)

func TestACPProvidersFromTheProject(t *testing.T) {
	t.Chdir(t.TempDir())
	if acps := acpProviders(); len(acps) != 0 {
		t.Fatalf("outside a project = %v", acps)
	}
	if err := os.WriteFile("barnraise.yaml", []byte("providers:\n  myagent:\n    type: acp\n    command: my-agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if acps := acpProviders(); acps["myagent"].Command != "my-agent" {
		t.Fatalf("in a project = %v", acps)
	}
	if _, isAPI := apiProviders()["myagent"]; isAPI {
		t.Fatal("an ACP agent is not a key-based provider")
	}
}
