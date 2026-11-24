package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadVerifiedPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	data := []byte(`{"deny_write":[".env"]}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	want := hex.EncodeToString(sum[:])
	if p, err := LoadVerifiedPolicy(path, want); err != nil || len(p.DenyWrite) != 1 {
		t.Fatalf("intact: %v", err)
	}
	if _, err := LoadVerifiedPolicy(path, ""); err != nil {
		t.Fatalf("no hash given: %v", err)
	}
	// The agent rewrites its own policy to allow everything.
	_ = os.WriteFile(path, []byte(`{}`), 0o600)
	if _, err := LoadVerifiedPolicy(path, want); !errors.Is(err, ErrPolicyTampered) {
		t.Fatalf("edited: %v", err)
	}
	// Or deletes it, which without the hash looks like "not a session".
	_ = os.Remove(path)
	if _, err := LoadVerifiedPolicy(path, want); !errors.Is(err, ErrPolicyTampered) {
		t.Fatalf("removed: %v", err)
	}
	if _, err := LoadVerifiedPolicy(path, ""); err == nil || errors.Is(err, ErrPolicyTampered) {
		t.Fatalf("removed without hash: %v", err)
	}
	res := DenyAll(ErrPolicyTampered)
	out := res.Output["hookSpecificOutput"].(map[string]any)
	if res.Decision.Allow || res.Decision.Rule != "policy_tampered" || out["permissionDecision"] != "deny" {
		t.Fatalf("deny all: %+v", res)
	}
}
