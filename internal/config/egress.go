package config

import (
	"fmt"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// egressDef validates the method/host/path rules of policy.allow_hosts and
// reads policy.egress (inspect hosts, repo_scope on by default).
func egressDef(allow, inspect []string, repoScope *bool) (domain.EgressDef, error) {
	if _, _, err := domain.SplitAllowHosts(allow); err != nil {
		return domain.EgressDef{}, fmt.Errorf("policy.%w", err)
	}
	out := domain.EgressDef{RepoScope: repoScope == nil || *repoScope}
	for _, h := range inspect {
		r, isRule, err := domain.ParseAllowEntry(h)
		if err != nil || isRule {
			return domain.EgressDef{}, fmt.Errorf("policy.egress.inspect: %q must be a plain host", h)
		}
		out.Inspect = append(out.Inspect, r.Host)
	}
	return out, nil
}
