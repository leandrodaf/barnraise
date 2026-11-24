package cli

import (
	"github.com/leandrodaf/barnraise/internal/config"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// ACPPlan marks an account of a provider of `type: acp`: the agent program
// keeps its own login, Barnraise only rotates the work between accounts.
const ACPPlan = "acp"

// acpProviders are the agent programs of `type: acp` in the barnraise.yaml
// of the current directory, when there is one.
func acpProviders() map[string]domain.ACPProviderDef {
	if root, err := findRoot(); err == nil {
		if cfg, err := config.Load(root); err == nil {
			return cfg.ACPProviders
		}
	}
	return nil
}
