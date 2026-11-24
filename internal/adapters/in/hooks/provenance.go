package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// PolicyHashEnv carries the SHA-256 of the session's policy.json (ADR-0030).
// The hook runs with the provider CLI's environment, which the agent
// cannot change for it.
const PolicyHashEnv = domain.PolicyHashEnv

// ErrPolicyTampered: the policy file is not the one the daemon wrote.
var ErrPolicyTampered = errors.New("the session's policy file is not the one Barnraise wrote")

// LoadVerifiedPolicy reads the session's policy and, when the daemon gave
// its hash, refuses a file that does not match it, or that is gone: in a
// session Barnraise started, a missing or edited policy must fail closed,
// never open.
func LoadVerifiedPolicy(path, wantSHA string) (*domain.Policy, error) {
	if wantSHA == "" {
		return LoadPolicy(path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPolicyTampered, err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != wantSHA {
		return nil, fmt.Errorf("%w: %s has sha256 %s, not %s", ErrPolicyTampered, path, got, wantSHA)
	}
	return LoadPolicy(path)
}

// DenyAll is the hook's answer while the policy cannot be trusted.
func DenyAll(why error) PreToolResult {
	d := domain.Decision{Allow: false, Rule: "policy_tampered", Reason: why.Error()}
	return PreToolResult{Decision: d, Output: map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PreToolUse", "permissionDecision": "deny",
		"permissionDecisionReason": "Barnraise policy: " + d.Reason + ". Every tool call is blocked until the session is restarted; deliver BLOCKED with this reason.",
	}}}
}
