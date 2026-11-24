package ports

// ACPAgent is an agent program that speaks the Agent Client Protocol
// (JSON-RPC 2.0 over its stdin and stdout), configured in barnraise.yaml
// as a provider of `type: acp`. The session runs Command with Args in the
// item's worktree; Env is added to the session's environment (secret
// references are already resolved).
type ACPAgent struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"-"`
}
