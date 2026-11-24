package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// ACPCLI is the name of the adapter that runs the providers of `type: acp`
// (for the machine's sandbox rules, like the CLI behind a provider).
const ACPCLI = "acp"

// acpAgent is the agent program of a provider of `type: acp`, with the
// secret references of its environment resolved; nil for other providers.
func (s *Service) acpAgent(cfg *domain.ProjectConfig, name string) (*ports.ACPAgent, error) {
	def, ok := cfg.ACPProviders[name]
	if !ok {
		return nil, nil
	}
	agent := &ports.ACPAgent{Name: name, Command: def.Command, Args: append([]string(nil), def.Args...)}
	if len(def.Env) > 0 {
		agent.Env = make(map[string]string, len(def.Env))
	}
	for k, v := range def.Env {
		if strings.HasPrefix(v, "keychain:") || strings.HasPrefix(v, "env:") {
			if s.d.ResolveSecret == nil {
				return nil, fmt.Errorf("providers.%s.env.%s: no secret store to read %s from", name, k, v)
			}
			rv, err := s.d.ResolveSecret(v)
			if err != nil {
				return nil, fmt.Errorf("providers.%s.env.%s: %w", name, k, err)
			}
			v = rv
		}
		agent.Env[k] = v
	}
	return agent, nil
}

// providerViolation records what a provider refused in the session itself
// (an ACP agent's permission request, file write or terminal that the
// policy blocked) like a block of the hooks: a policy.violation event and
// the owner's notification.
func (s *Service) providerViolation(ctx context.Context, p *Project, agent, itemID string, ev ports.ProviderEvent) {
	if !ev.IsError {
		return
	}
	var r struct {
		Rule string `json:"rule"`
	}
	_ = json.Unmarshal(ev.Raw, &r)
	var in struct {
		Excerpt string `json:"excerpt"`
	}
	_ = json.Unmarshal(ev.Input, &in)
	if in.Excerpt == "" {
		in.Excerpt = string(ev.Input)
	}
	_ = s.RecordViolation(ctx, p.Name, agent, itemID, ev.Tool, r.Rule, ev.Text, in.Excerpt)
}
