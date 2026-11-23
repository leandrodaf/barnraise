package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// A new Gemini account signs in with a Google account; settings already
// there are kept.
func TestGeminiSelectGoogleLogin(t *testing.T) {
	if err := geminiSelectGoogleLogin(""); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := geminiSelectGoogleLogin(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".gemini", "settings.json")
	b, err := os.ReadFile(path)
	if err != nil || string(b) != `{"security":{"auth":{"selectedType":"oauth-personal"}}}`+"\n" {
		t.Fatalf("settings %q %v", b, err)
	}
	if err := os.WriteFile(path, []byte(`{"mine":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := geminiSelectGoogleLogin(dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != `{"mine":true}` {
		t.Fatalf("settings overwritten: %s", b)
	}
}
