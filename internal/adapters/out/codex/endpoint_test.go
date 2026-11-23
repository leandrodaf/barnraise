package codex

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/adapters/out/provcommon"
	"github.com/leandrodaf/barnraise/internal/adapters/out/provcommon/fakecli"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

const testKey = "sk-or-v1-0123456789abcdef"

func openrouterSpec(dir string) ports.SessionSpec {
	return ports.SessionSpec{
		Model: "deepseek/deepseek-chat", Workdir: dir, Prompt: "hi",
		Account:  domain.Account{ID: "openrouter-1", Provider: "openrouter", ConfigDir: "/cfg/openrouter-1"},
		Endpoint: &ports.Endpoint{Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1"},
		Env:      map[string]string{ports.EndpointKeyEnv: testKey, "BARNRAISE_ITEM": "it-1"},
	}
}

func TestEndpointArgs(t *testing.T) {
	got := EndpointArgs(ports.Endpoint{Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1"})
	want := []string{
		"-c", `model_provider="barnraise-openrouter"`,
		"-c", `model_providers.barnraise-openrouter.name="openrouter"`,
		"-c", `model_providers.barnraise-openrouter.base_url="https://openrouter.ai/api/v1"`,
		"-c", `model_providers.barnraise-openrouter.env_key="BARNRAISE_PROVIDER_KEY"`,
		"-c", `model_providers.barnraise-openrouter.wire_api="responses"`,
		"-c", `shell_environment_policy.exclude=["BARNRAISE_PROVIDER_KEY"]`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("args = %q\nwant   %q", got, want)
	}
}

// A session on a key-based provider runs the same codex command with the
// endpoint's overrides; the key is in the environment by value and nowhere
// on the command line.
func TestOpenOnEndpoint(t *testing.T) {
	dir := t.TempDir()
	rec := filepath.Join(dir, "call.json")
	p := fakeProvider(fakecli.Script{Fixture: "testdata/session_basic.jsonl", Record: rec})
	s, err := p.Open(context.Background(), openrouterSpec(dir))
	if err != nil {
		t.Fatal(err)
	}
	var usage []domain.TokenUsage
	for e := range s.Events() {
		if e.Usage != nil {
			usage = append(usage, *e.Usage)
		}
	}
	res, err := s.Wait()
	if err != nil || res.Text != "pong" {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if len(usage) == 0 || usage[0].Provider != "openrouter" || usage[0].Account != "openrouter-1" || usage[0].Model != "deepseek/deepseek-chat" {
		t.Fatalf("usage = %+v", usage)
	}
	call, err := fakecli.ReadCall(rec)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(call.Args, " ")
	if strings.Contains(joined, testKey) {
		t.Fatalf("the key is on the command line: %q", call.Args)
	}
	for _, w := range []string{"--model deepseek/deepseek-chat", `model_provider="barnraise-openrouter"`, `env_key="BARNRAISE_PROVIDER_KEY"`, "--sandbox workspace-write"} {
		if !strings.Contains(joined, w) {
			t.Fatalf("args lack %s: %q", w, call.Args)
		}
	}
	if !slices.Contains(call.Env, ports.EndpointKeyEnv+"="+testKey) || !slices.Contains(call.Env, "CODEX_HOME=/cfg/openrouter-1") {
		t.Fatalf("env = %v", call.Env)
	}
	// Nothing codex was given to read holds the key: no config file is written.
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || path == rec {
			return nil
		}
		if b, _ := os.ReadFile(path); strings.Contains(string(b), testKey) {
			t.Errorf("%s holds the key", path)
		}
		return nil
	})
}

func TestResumeOnEndpoint(t *testing.T) {
	spec := openrouterSpec("/w")
	spec.ResumeID = "th-1"
	args, err := New(Options{}).Args(spec, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, " "), `model_provider="barnraise-openrouter"`) || args[len(args)-2] != "th-1" {
		t.Fatalf("args = %q", args)
	}
}

func TestEndpointWithoutKeyDoesNotStart(t *testing.T) {
	spec := openrouterSpec(t.TempDir())
	delete(spec.Env, ports.EndpointKeyEnv)
	if _, err := fakeProvider(fakecli.Script{}).Open(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "no API key") {
		t.Fatalf("err = %v", err)
	}
	spec = openrouterSpec(t.TempDir())
	spec.Endpoint.BaseURL = ""
	if _, err := fakeProvider(fakecli.Script{}).Open(context.Background(), spec); err == nil {
		t.Fatal("an endpoint without a base URL must not start")
	}
}

// In a container and under sudo the key is passed by name only: its value
// travels in the engine's (or sudo's) own environment.
func TestEndpointKeyByNameInContainerAndSudo(t *testing.T) {
	p := New(Options{})
	spec := openrouterSpec("/w")
	env := p.Env(spec)
	args, err := p.Args(spec, "")
	if err != nil {
		t.Fatal(err)
	}
	_, box := provcommon.ContainerArgs(&ports.ContainerSpec{Image: "agent"}, env, "/w", "codex", args)
	if strings.Contains(strings.Join(box, " "), testKey) || !slices.Contains(box, ports.EndpointKeyEnv) {
		t.Fatalf("container args = %q", box)
	}
	_, sudo := provcommon.SudoArgs("barnraise-openrouter-1", env, "codex", args)
	if strings.Contains(strings.Join(sudo, " "), testKey) || !strings.Contains(strings.Join(sudo, " "), ports.EndpointKeyEnv) {
		t.Fatalf("sudo args = %q", sudo)
	}
}
