package config

import (
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// acpProvider reads a `providers:` entry of `type: acp`. It returns nil for
// a key-based provider (no type, or base_url only), which the caller reads
// as before, and an error for an unknown type or mixed keys.
func acpProvider(name, typ, baseURL, command string, args []string, env map[string]string) (*domain.ACPProviderDef, error) {
	typ = strings.TrimSpace(typ)
	switch typ {
	case "":
		if command != "" || len(args) > 0 || len(env) > 0 {
			return nil, fmt.Errorf("providers.%s: command, args and env belong to an agent program; add `type: acp`", name)
		}
		return nil, nil
	case domain.ProviderTypeACP:
	default:
		return nil, fmt.Errorf("providers.%s: type %q is not known (acp, or no type for an endpoint with base_url)", name, typ)
	}
	if strings.TrimSpace(baseURL) != "" {
		return nil, fmt.Errorf("providers.%s: a provider of type acp runs a program (command), it has no base_url", name)
	}
	d := &domain.ACPProviderDef{Command: strings.TrimSpace(command), Args: append([]string(nil), args...)}
	if len(env) > 0 {
		d.Env = maps.Clone(env)
	}
	return d, nil
}

// acpView shows the ACP providers without the values of their environment:
// they may be secrets written in place.
func acpView(cfg *domain.ProjectConfig) map[string]any {
	out := map[string]any{}
	for name, d := range cfg.ACPProviders {
		var env []string
		for k := range d.Env {
			env = append(env, k)
		}
		sort.Strings(env)
		out[name] = map[string]any{"command": d.Command, "args": d.Args, "env": env}
	}
	return out
}
