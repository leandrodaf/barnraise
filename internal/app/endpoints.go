package app

import (
	"fmt"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// EndpointCLI is the provider adapter that runs the sessions of key-based
// providers (`providers:` in barnraise.yaml): the Codex CLI, pointed at the
// provider's endpoint. Barnraise never runs its own agent loop over an API.
const EndpointCLI = "codex"

// providerFor returns the adapter that runs a provider's sessions, the name
// of the CLI behind it (for the machine's sandbox rules), and the endpoint
// when the provider is reached with an API key.
func (s *Service) providerFor(cfg *domain.ProjectConfig, name string) (ports.Provider, string, *ports.Endpoint, bool) {
	if prov, ok := s.d.Providers[name]; ok {
		return prov, name, nil, true
	}
	if _, ok := cfg.ACPProviders[name]; ok {
		if s.d.ACP == nil {
			return nil, "", nil, false
		}
		return s.d.ACP, ACPCLI, nil, true
	}
	def, ok := cfg.APIProviders[name]
	if !ok {
		return nil, "", nil, false
	}
	prov, ok := s.d.Providers[EndpointCLI]
	if !ok {
		return nil, "", nil, false
	}
	return prov, EndpointCLI, &ports.Endpoint{Name: name, BaseURL: def.BaseURL}, true
}

// apiKey reads the key of an account of a key-based provider from the
// keychain. It never goes into a file: only into the session's environment.
func (s *Service) apiKey(acc *domain.Account) (string, error) {
	ref := "keychain:" + domain.APIKeySecret(acc.ID)
	if s.d.ResolveSecret == nil {
		return "", fmt.Errorf("account %s: no secret store to read its API key from", acc.ID)
	}
	key, err := s.d.ResolveSecret(ref)
	if err != nil || key == "" {
		return "", fmt.Errorf("account %s has no API key in this machine's keychain (`barnraise account add %s` stores it): %v", acc.ID, acc.Provider, err)
	}
	return key, nil
}
