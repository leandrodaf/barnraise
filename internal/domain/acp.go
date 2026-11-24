package domain

import (
	"fmt"
	"regexp"
	"sort"
)

// ProviderTypeACP is the `type` of a provider whose sessions run an agent
// program that speaks the Agent Client Protocol over stdio.
const ProviderTypeACP = "acp"

// ACPProviderDef is an agent program that speaks the Agent Client Protocol
// (JSON-RPC 2.0 over stdin and stdout). Barnraise is the protocol's client:
// it answers the agent's permission requests with its policy, reads and
// writes files for it under the same rules and runs its terminals.
type ACPProviderDef struct {
	Command string   `json:"command" yaml:"command"`
	Args    []string `json:"args,omitempty" yaml:"args"`
	// Env is added to the session's environment. Values may be secret
	// references (keychain:…, env:…), resolved when a session starts.
	Env map[string]string `json:"env,omitempty" yaml:"env"`
}

var reEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedEnv are variables Barnraise sets for every session; an agent's
// definition cannot replace them.
var reservedEnv = map[string]bool{"BARNRAISE_TOKEN": true, "BARNRAISE_SOCKET": true, "BARNRAISE_POLICY": true,
	"BARNRAISE_WORKTREE": true, "BARNRAISE_PROJECT": true, "BARNRAISE_ITEM": true, "BARNRAISE_STEP": true,
	"BARNRAISE_AGENT": true, "BARNRAISE_ROLE": true, "BARNRAISE_ACCOUNT": true, "BARNRAISE_PROVIDER_KEY": true}

func (c *ProjectConfig) validateACPProviders() []error {
	var errs []error
	names := make([]string, 0, len(c.ACPProviders))
	for name := range c.ACPProviders {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		d := c.ACPProviders[name]
		switch {
		case !reAPIProviderName.MatchString(name):
			errs = append(errs, fmt.Errorf("providers.%s: a provider name is lower-case letters and digits, starting with a letter", name))
			continue
		case Contains(CLIProviders, name) || name == ProviderTypeACP:
			errs = append(errs, fmt.Errorf("providers: the name %s is taken by a provider of Barnraise", name))
			continue
		case d.Command == "":
			errs = append(errs, fmt.Errorf("providers.%s (type acp) needs a command: the agent program that speaks the Agent Client Protocol on stdio", name))
		}
		if _, ok := c.APIProviders[name]; ok {
			errs = append(errs, fmt.Errorf("providers.%s: the name is used by a key-based provider too", name))
		}
		envNames := make([]string, 0, len(d.Env))
		for k := range d.Env {
			envNames = append(envNames, k)
		}
		sort.Strings(envNames)
		for _, k := range envNames {
			if !reEnvName.MatchString(k) {
				errs = append(errs, fmt.Errorf("providers.%s.env: %q is not a variable name", name, k))
			} else if reservedEnv[k] {
				errs = append(errs, fmt.Errorf("providers.%s.env: %s is set by Barnraise for every session", name, k))
			}
		}
	}
	return errs
}
