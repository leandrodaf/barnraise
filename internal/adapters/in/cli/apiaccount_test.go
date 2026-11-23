package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/adapters/out/keychain"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

const cliKey = "sk-or-v1-feedface"

func TestStoreAPIAccount(t *testing.T) {
	secrets := t.TempDir()
	ks := keychain.New("barnraise-test", secrets, keychain.ForceFile())
	dir := filepath.Join(t.TempDir(), "accounts", "openrouter-1")
	acc, err := storeAPIAccount(ks, "openrouter-1", "openrouter", cliKey, dir, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if acc.Id != "openrouter-1" || acc.Provider != "openrouter" || acc.Plan != APIPlan || acc.ConfigDir != dir || acc.Machine != "m1" || acc.CeilingPct != 100 || acc.Status != "ok" {
		t.Fatalf("account = %+v", acc)
	}
	if got, err := ks.Get(domain.APIKeySecret("openrouter-1")); err != nil || got != cliKey {
		t.Fatalf("keychain = %q %v", got, err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("account folder %v %v", fi, err)
	}
	// Only the sealed store holds it: not the account's folder, not in clear text anywhere.
	for _, root := range []string{dir, secrets} {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				if b, _ := os.ReadFile(path); bytes.Contains(b, []byte(cliKey)) {
					t.Errorf("%s holds the key in clear text", path)
				}
			}
			return nil
		})
	}
	if _, err := storeAPIAccount(ks, "openrouter-2", "openrouter", cliKey, "/proc/no/such/dir", ""); err == nil {
		t.Fatal("a folder that cannot be made must fail")
	}
}

func TestReadAPIKey(t *testing.T) {
	var out bytes.Buffer
	key, err := readAPIKey(strings.NewReader("  "+cliKey+"  \n"), &out, "openrouter")
	if err != nil || key != cliKey || strings.Contains(out.String(), cliKey) {
		t.Fatalf("key = %q %v, out %q", key, err, out.String())
	}
	if _, err := readAPIKey(strings.NewReader("\n"), &out, "openrouter"); err == nil {
		t.Fatal("an empty key must fail")
	}
}

func TestTestAPICommandKeepsTheKeyOffTheCommandLine(t *testing.T) {
	cmd := testAPICommand(context.Background(), ports.Endpoint{Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1"}, "deepseek/deepseek-chat", "/acc", cliKey)
	if strings.Contains(strings.Join(cmd.Args, " "), cliKey) {
		t.Fatalf("args = %q", cmd.Args)
	}
	for _, w := range []string{"--sandbox read-only", "--model deepseek/deepseek-chat", `model_provider="barnraise-openrouter"`} {
		if !strings.Contains(strings.Join(cmd.Args, " "), w) {
			t.Fatalf("args lack %s: %q", w, cmd.Args)
		}
	}
	if !slices.Contains(cmd.Env, ports.EndpointKeyEnv+"="+cliKey) || !slices.Contains(cmd.Env, "CODEX_HOME=/acc") {
		t.Fatal("the key and the account's folder go in the environment")
	}
}

func TestAPIProvidersFromTheProject(t *testing.T) {
	t.Chdir(t.TempDir())
	if apis := apiProviders(); len(apis) != 1 || apis["openrouter"].BaseURL == "" {
		t.Fatalf("outside a project = %v", apis)
	}
	if err := os.WriteFile("barnraise.yaml", []byte("providers:\n  vllm:\n    base_url: http://127.0.0.1:8000/v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	apis := apiProviders()
	if apis["vllm"].BaseURL != "http://127.0.0.1:8000/v1" || apis["openrouter"].BaseURL == "" {
		t.Fatalf("in a project = %v", apis)
	}
	if got := providerChoices(apis); got != "claude, codex, gemini, openrouter, vllm" {
		t.Fatalf("choices = %q", got)
	}
}
