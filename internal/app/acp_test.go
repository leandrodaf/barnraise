package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// acpEnv routes every class to an ACP agent and lets the fake provider
// stand in for the ACP adapter.
func acpEnv(t *testing.T, env map[string]string, secrets map[string]string) *env {
	e := newEnv(t, withCfg(func(c *domain.ProjectConfig) {
		c.ACPProviders = map[string]domain.ACPProviderDef{"myagent": {Command: "my-agent", Args: []string{"--acp"}, Env: env}}
		for class := range c.Models.Classes {
			c.Models.Classes[class] = []domain.ModelChoice{{Provider: "myagent", Model: "default"}}
		}
	}))
	e.svc.d.ACP = e.prov
	e.svc.d.ResolveSecret = func(ref string) (string, error) {
		if v, ok := secrets[ref]; ok {
			return v, nil
		}
		return "", errors.New("not found")
	}
	return e
}

func TestRuntimeRunsAnACPAgent(t *testing.T) {
	e := acpEnv(t, map[string]string{"MY_KEY": "keychain:my-agent", "MODE": "fast"}, map[string]string{"keychain:my-agent": "s3cret"})
	e.prov.Handle(func(_ context.Context, spec ports.SessionSpec, emit func(ports.ProviderEvent)) (ports.SessionResult, error) {
		rule, _ := json.Marshal(map[string]string{"rule": "deny_write"})
		in, _ := json.Marshal(map[string]string{"excerpt": ".github/workflows/ci.yml"})
		emit(ports.ProviderEvent{Kind: ports.PEPermission, IsError: true, Tool: "Edit", Text: "writing to .github/workflows/ci.yml is blocked by policy", Input: in, Raw: rule})
		emit(ports.ProviderEvent{Kind: ports.PEPermission, Tool: "Read"}) // an allowed call is no violation
		return ports.SessionResult{Text: "done"}, nil
	})
	if _, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: "myagent-1", Provider: "myagent", Plan: "acp"}); err != nil {
		t.Fatal(err)
	}
	it := e.ready("simple", "through ACP", "x")
	e.fill()
	e.idle()
	specs := e.prov.Specs()
	if len(specs) == 0 {
		t.Fatalf("no session for %s", it.ID)
	}
	spec := specs[0]
	if spec.ACP == nil || spec.ACP.Name != "myagent" || spec.ACP.Command != "my-agent" || spec.ACP.Args[0] != "--acp" {
		t.Fatalf("acp = %+v", spec.ACP)
	}
	if spec.ACP.Env["MY_KEY"] != "s3cret" || spec.ACP.Env["MODE"] != "fast" || spec.Endpoint != nil {
		t.Fatalf("env = %+v", spec.ACP.Env)
	}
	var found *domain.Event
	for _, ev := range e.store.Events() {
		if ev.Type == domain.EvPolicyViolation {
			found = &ev
		}
	}
	if found == nil {
		t.Fatal("no policy.violation event")
	}
	var pl domain.Payload
	_ = json.Unmarshal(found.Data, &pl)
	if pl.Extra["rule"] != "deny_write" || pl.Extra["tool"] != "Edit" || pl.Extra["excerpt"] != ".github/workflows/ci.yml" || found.Actor == "" {
		t.Fatalf("violation = %s", found.Data)
	}
}

func TestRuntimeACPAgentWithoutItsSecretDoesNotStart(t *testing.T) {
	e := acpEnv(t, map[string]string{"MY_KEY": "keychain:missing"}, nil)
	if _, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: "myagent-1", Provider: "myagent"}); err != nil {
		t.Fatal(err)
	}
	e.ready("simple", "no secret", "x")
	e.fill()
	e.idle()
	if n := len(e.prov.Specs()); n != 0 {
		t.Fatalf("%d sessions started", n)
	}
	if si := e.p.Snapshot().Sessions["implementer-m1-1"]; si == nil || !strings.Contains(si.Detail, "providers.myagent.env.MY_KEY") {
		t.Fatalf("session %+v", si)
	}
	e.svc.d.ResolveSecret = nil
	if _, err := e.svc.acpAgent(e.p.Cfg(), "myagent"); err == nil {
		t.Fatal("no secret store, no secret")
	}
	if a, err := e.svc.acpAgent(e.p.Cfg(), "claude"); a != nil || err != nil {
		t.Fatal("not an ACP provider")
	}
}

func TestUpsertAccountKnowsTheACPProviders(t *testing.T) {
	e := acpEnv(t, nil, nil)
	e.svc.d.ACP = nil
	// Without the adapter there is nothing to run the agent with.
	if _, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: "myagent-1", Provider: "myagent"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	e.svc.d.ACP = e.prov
	if _, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: "myagent-1", Provider: "myagent"}); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: "x-1", Provider: "mystery"})
	if err == nil || !strings.Contains(err.Error(), "myagent") {
		t.Fatalf("the known list names the ACP agents: %v", err)
	}
}
