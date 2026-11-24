package app

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

type fakeBroker struct {
	mu       sync.Mutex
	hosts    map[string]bool
	sealed   map[string]string
	released int
	bundle   []byte
}

func (b *fakeBroker) Seal(host, key string) (string, func(), bool) {
	if !b.hosts[host] {
		return "", func() {}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	ph := "sealed-" + host
	b.sealed[ph] = key
	var once sync.Once
	return ph, func() {
		once.Do(func() {
			b.mu.Lock()
			b.released++
			b.mu.Unlock()
		})
	}, true
}

func (b *fakeBroker) CABundle() []byte { return b.bundle }

func (b *fakeBroker) releases() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.released
}

func TestRuntimeSealsTheProviderKeyBehindTheEgressProxy(t *testing.T) {
	e := openrouterEnv(t, map[string]string{"keychain:account/openrouter-1/api-key": orKey})
	br := &fakeBroker{hosts: map[string]bool{"openrouter.ai": true}, sealed: map[string]string{}, bundle: []byte("-----BEGIN CERTIFICATE-----\n")}
	e.svc.Runtime().EgressProxy = "127.0.0.1:3128"
	e.svc.Runtime().KeyBroker = br
	if _, err := e.svc.UpsertAccount(e.ctx, "demo", owner(), domain.Account{ID: "openrouter-1", Provider: "openrouter", Plan: "api"}); err != nil {
		t.Fatal(err)
	}
	e.ready("simple", "cheap research", "x")
	e.fill()
	e.idle()
	specs := e.prov.Specs()
	if len(specs) == 0 {
		t.Fatal("no session")
	}
	env := specs[0].Env
	if env[ports.EndpointKeyEnv] != "sealed-openrouter.ai" || br.sealed["sealed-openrouter.ai"] != orKey {
		t.Fatalf("session env carries %q", env[ports.EndpointKeyEnv])
	}
	for k, v := range env {
		if strings.Contains(v, orKey) {
			t.Fatalf("%s carries the real key", k)
		}
	}
	ca := env["SSL_CERT_FILE"]
	if filepath.Base(ca) != EgressCAFile || env["NODE_EXTRA_CA_CERTS"] != ca || env["GIT_SSL_CAINFO"] != ca {
		t.Fatalf("trust env: %v", env)
	}
	if data, err := os.ReadFile(ca); err != nil || string(data) != string(br.bundle) {
		t.Fatal(err, string(data))
	}
	if br.releases() != 1 {
		t.Fatalf("placeholder not released when the session ended: %d", br.releases())
	}
}

func TestSealKeyFallsBackToTheKey(t *testing.T) {
	r := &Runtime{}
	ep := &ports.Endpoint{Name: "x", BaseURL: "https://api.x.test/v1"}
	if k, _ := r.sealKey(ep, "k"); k != "k" {
		t.Fatal("sealed without the proxy")
	}
	r.EgressProxy = "127.0.0.1:1"
	if k, _ := r.sealKey(ep, "k"); k != "k" {
		t.Fatal("sealed without a broker")
	}
	r.KeyBroker = &fakeBroker{hosts: map[string]bool{}}
	if k, _ := r.sealKey(ep, "k"); k != "k" {
		t.Fatal("sealed for a host the broker does not guard")
	}
	if k, _ := r.sealKey(&ports.Endpoint{BaseURL: "::bad"}, "k"); k != "k" {
		t.Fatal("bad URL")
	}
	env := map[string]string{}
	if err := r.egressTrust(env, t.TempDir()); err != nil || len(env) != 0 {
		t.Fatal("trust env without a bundle", env, err)
	}
	r.KeyBroker = &fakeBroker{bundle: []byte("pem")}
	if err := r.egressTrust(env, filepath.Join(t.TempDir(), "f\x00")); err == nil {
		t.Fatal("bad folder accepted")
	}
}
