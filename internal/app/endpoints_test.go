package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

const orKey = "sk-or-v1-secret"

// openrouterEnv routes every class to OpenRouter and lets the fake provider
// stand in for the Codex CLI that runs its sessions.
func openrouterEnv(t *testing.T, secrets map[string]string) *env {
	e := newEnv(t, withCfg(func(c *domain.ProjectConfig) {
		for class := range c.Models.Classes {
			c.Models.Classes[class] = []domain.ModelChoice{{Provider: "openrouter", Model: "deepseek/deepseek-chat"}}
		}
	}))
	e.svc.d.Providers[EndpointCLI] = e.prov
	e.svc.Runtime().NoSandboxProviders = map[string]string{}
	e.svc.d.ResolveSecret = func(ref string) (string, error) {
		if v, ok := secrets[ref]; ok {
			return v, nil
		}
		return "", errors.New("not found")
	}
	return e
}

func TestRuntimeRunsAnAPIAccountThroughTheCodexCLI(t *testing.T) {
	e := openrouterEnv(t, map[string]string{"keychain:account/openrouter-1/api-key": orKey})
	if _, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: "openrouter-1", Provider: "openrouter", Plan: "api"}); err != nil {
		t.Fatal(err)
	}
	it := e.ready("simple", "cheap research", "x")
	e.fill()
	e.idle()
	specs := e.prov.Specs()
	if len(specs) == 0 {
		t.Fatalf("no session for %s", it.ID)
	}
	spec := specs[0]
	if spec.Endpoint == nil || spec.Endpoint.Name != "openrouter" || spec.Endpoint.BaseURL != "https://openrouter.ai/api/v1" {
		t.Fatalf("endpoint = %+v", spec.Endpoint)
	}
	if spec.Model != "deepseek/deepseek-chat" || spec.Account.ID != "openrouter-1" || spec.Env[ports.EndpointKeyEnv] != orKey {
		t.Fatalf("spec = %+v", spec)
	}
	// The files Barnraise writes for the session never hold the key.
	sessDir := filepath.Dir(spec.Env["BARNRAISE_POLICY"])
	ents, err := os.ReadDir(sessDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range ents {
		b, _ := os.ReadFile(filepath.Join(sessDir, en.Name()))
		if strings.Contains(string(b), orKey) {
			t.Fatalf("%s holds the API key", en.Name())
		}
	}
	data, _ := json.Marshal(e.p.Snapshot().Accounts)
	if strings.Contains(string(data), orKey) {
		t.Fatal("the key reached the event log")
	}
}

func TestRuntimeAPIAccountWithoutKeyDoesNotStart(t *testing.T) {
	e := openrouterEnv(t, nil)
	if _, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: "openrouter-1", Provider: "openrouter"}); err != nil {
		t.Fatal(err)
	}
	e.ready("simple", "no key", "x")
	e.fill()
	e.idle()
	if n := len(e.prov.Specs()); n != 0 {
		t.Fatalf("%d sessions started without a key", n)
	}
	if si := e.p.Snapshot().Sessions["implementer-m1-1"]; si == nil || !strings.Contains(si.Detail, "no API key") {
		t.Fatalf("session %+v", si)
	}
	e.svc.d.ResolveSecret = nil
	if _, err := e.svc.apiKey(&domain.Account{ID: "openrouter-1"}); err == nil {
		t.Fatal("no secret store, no key")
	}
}

func TestUpsertAccountKnowsTheAPIProviders(t *testing.T) {
	e := newEnv(t)
	// Without the Codex CLI there is nothing to run an API account with.
	_, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: "openrouter-1", Provider: "openrouter"})
	if !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), "openrouter,") {
		t.Fatalf("err = %v", err)
	}
	e.svc.d.Providers[EndpointCLI] = e.prov
	if _, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: "openrouter-1", Provider: "openrouter"}); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: "mystery-1", Provider: "mystery"})
	if err == nil || !strings.Contains(err.Error(), "openrouter") {
		t.Fatalf("err = %v", err)
	}
	acc := e.p.Snapshot().Accounts["openrouter-1"]
	// No 5-hour or weekly window: an API account is available until the
	// endpoint itself says no.
	if acc == nil || !acc.Available(e.clock.Now()) || acc.Load() != 0 {
		t.Fatalf("account %+v", acc)
	}
}

// The machine's sandbox rule for the Codex CLI covers the providers it runs.
func TestRuntimeAPIProviderFollowsTheCodexSandboxRule(t *testing.T) {
	e := openrouterEnv(t, map[string]string{"keychain:account/openrouter-1/api-key": orKey})
	e.svc.Runtime().NoSandboxProviders[EndpointCLI] = "no bubblewrap here"
	if _, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: "openrouter-1", Provider: "openrouter"}); err != nil {
		t.Fatal(err)
	}
	e.ready("simple", "sandboxed", "x")
	e.fill()
	e.idle()
	if n := len(e.prov.Specs()); n != 0 {
		t.Fatalf("%d sessions started", n)
	}
	if si := e.p.Snapshot().Sessions["implementer-m1-1"]; si == nil || !strings.Contains(si.Detail, "no bubblewrap here") {
		t.Fatalf("session %+v", si)
	}
}
