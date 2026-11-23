package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// Scopes. A token or role carries a set; "*" is everything.
const (
	ScopeAll            = "*"
	ScopeItemsRead      = "items:read"
	ScopeItemsWrite     = "items:write"
	ScopeClaim          = "items:claim"
	ScopeDeliver        = "deliveries:write"
	ScopeReview         = "reviews:write"
	ScopeChecksReport   = "checks:report"
	ScopeApprove        = "approve"
	ScopeFocus          = "focus"
	ScopePause          = "pause"
	ScopePropose        = "proposals:write"
	ScopeProposalDecide = "proposals:decide"
	ScopeMessage        = "messages:write"
	ScopeAsk            = "owner:ask"
	ScopeAnswer         = "owner:answer"
	ScopeTweak          = "tweaks:write"
	ScopeAccounts       = "accounts:write"
	ScopeMachines       = "machines:write"
	ScopeAdmin          = "admin"
	ScopeEvents         = "events:read"
	ScopeUsage          = "usage:report"
	ScopeJournal        = "journal:write"
)

// AllScopes lists every scope, for docs and validation.
var AllScopes = []string{ScopeItemsRead, ScopeItemsWrite, ScopeClaim, ScopeDeliver, ScopeReview, ScopeChecksReport, ScopeApprove,
	ScopeFocus, ScopePause, ScopePropose, ScopeProposalDecide, ScopeMessage, ScopeAsk, ScopeAnswer, ScopeTweak, ScopeAccounts,
	ScopeMachines, ScopeAdmin, ScopeEvents, ScopeUsage, ScopeJournal}

// ActorKind tells who is calling.
type ActorKind string

const (
	ActorOwner  ActorKind = "owner"
	ActorAgent  ActorKind = "agent"
	ActorToken  ActorKind = "token"
	ActorSystem ActorKind = "system"
)

// Actor is the authenticated caller. Permission is the role's and is
// applied by the daemon: an implementer simply does not see approve.
type Actor struct {
	Kind    ActorKind `json:"kind"`
	Name    string    `json:"name"`
	Role    string    `json:"role,omitempty"`
	Project string    `json:"project,omitempty"` // "" = any project
	ItemID  string    `json:"item_id,omitempty"` // agent: the item of its session
	Step    string    `json:"step,omitempty"`
	Account string    `json:"account,omitempty"`
	Scopes  []string  `json:"scopes"`
	TokenID string    `json:"token_id,omitempty"`
	Family  string    `json:"family,omitempty"`
	Model   string    `json:"model,omitempty"`
	Attempt string    `json:"attempt,omitempty"` // "b" for the second attempt of a risky item
}

// Owner returns the owner actor.
func Owner(name string) Actor {
	if name == "" {
		name = "owner"
	}
	return Actor{Kind: ActorOwner, Name: name, Scopes: []string{ScopeAll}}
}

// System is Barnraise itself.
func System() Actor { return Actor{Kind: ActorSystem, Name: "barnraise", Scopes: []string{ScopeAll}} }

// Can reports whether the actor has a scope for a project.
func (a Actor) Can(scope, project string) bool {
	if a.Project != "" && project != "" && a.Project != project {
		return false
	}
	for _, s := range a.Scopes {
		if s == ScopeAll || s == scope {
			return true
		}
	}
	return false
}

// Require returns ErrForbidden when the scope is missing.
func (a Actor) Require(scope, project string) error {
	if !a.Can(scope, project) {
		return fmt.Errorf("%w: %s %q lacks %s", ErrForbidden, a.Kind, a.Name, scope)
	}
	return nil
}

// IsPerson reports owner or person tokens.
func (a Actor) IsPerson() bool {
	return a.Kind == ActorOwner || (a.Kind == ActorToken && a.Can(ScopeApprove, ""))
}

// RoleScopes are the scopes an agent role gets for its session.
func RoleScopes(role string, reviewer bool) []string {
	base := []string{ScopeItemsRead, ScopePropose, ScopeMessage, ScopeEvents, ScopeJournal, ScopeTweak, ScopeClaim}
	switch {
	case role == "analyst":
		return append(base, ScopeReview, ScopeApprove, ScopeFocus, ScopePause, ScopeAsk, ScopeItemsWrite, ScopeProposalDecide, ScopeDeliver)
	case reviewer:
		return append(base, ScopeReview)
	default:
		// Implementers may ask the owner only by exception; the daemon rate-limits it.
		return append(base, ScopeDeliver, ScopeAsk)
	}
}

// MCPTools returns the MCP tools a role sees.
func MCPTools(scopes []string) []string {
	has := func(s string) bool {
		for _, x := range scopes {
			if x == s || x == ScopeAll {
				return true
			}
		}
		return false
	}
	var out []string
	if has(ScopeClaim) {
		out = append(out, "next_item")
	}
	if has(ScopeDeliver) {
		out = append(out, "deliver")
	}
	if has(ScopeReview) {
		out = append(out, "review")
	}
	if has(ScopePropose) {
		out = append(out, "propose")
	}
	if has(ScopeAsk) {
		out = append(out, "ask_owner")
	}
	if has(ScopeMessage) {
		out = append(out, "message")
	}
	if has(ScopeItemsRead) {
		out = append(out, "status", "context", "find")
	}
	if has(ScopeApprove) {
		out = append(out, "approve")
	}
	if has(ScopeFocus) {
		out = append(out, "focus")
	}
	if has(ScopePause) {
		out = append(out, "pause")
	}
	return out
}

// TokenRegistry holds short-lived session tokens in memory; durable API
// tokens live in the store and are resolved by the HTTP adapter.
type TokenRegistry struct {
	mu     sync.RWMutex
	byHash map[string]sessionToken
}

type sessionToken struct {
	actor   Actor
	expires time.Time
}

// NewTokenRegistry creates an empty registry.
func NewTokenRegistry() *TokenRegistry { return &TokenRegistry{byHash: map[string]sessionToken{}} }

// Issue creates a token for an actor.
func (r *TokenRegistry) Issue(a Actor, ttl time.Duration) string {
	tok := NewSecret("brs_")
	r.mu.Lock()
	r.byHash[HashToken(tok)] = sessionToken{actor: a, expires: time.Now().Add(ttl)}
	r.mu.Unlock()
	return tok
}

// Resolve returns the actor of a token.
func (r *TokenRegistry) Resolve(tok string) (Actor, bool) {
	r.mu.RLock()
	st, ok := r.byHash[HashToken(tok)]
	r.mu.RUnlock()
	if !ok || time.Now().After(st.expires) {
		return Actor{}, false
	}
	return st.actor, true
}

// Revoke removes a token.
func (r *TokenRegistry) Revoke(tok string) {
	r.mu.Lock()
	delete(r.byHash, HashToken(tok))
	r.mu.Unlock()
}

// RevokeAgent removes every token of an agent.
func (r *TokenRegistry) RevokeAgent(agent string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for h, st := range r.byHash {
		if st.actor.Kind == ActorAgent && st.actor.Name == agent {
			delete(r.byHash, h)
		}
	}
}

// NewSecret returns a random token with a prefix.
func NewSecret(prefix string) string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// HashToken hashes a token for storage.
func HashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// ValidateScopes checks scope names.
func ValidateScopes(scopes []string) error {
	known := map[string]bool{ScopeAll: true}
	for _, s := range AllScopes {
		known[s] = true
	}
	for _, s := range scopes {
		if !known[s] {
			return fmt.Errorf("%w: unknown scope %q (known: %s)", ErrInvalid, s, strings.Join(AllScopes, ", "))
		}
	}
	return nil
}

// sortedKeys is a small helper.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// actorName is used in events.
func actorName(a Actor) string {
	if a.Name == "" {
		return string(a.Kind)
	}
	return a.Name
}

// checkItemAccess restricts agents to the item of their session for writes.
func checkItemAccess(a Actor, itemID string) error {
	if a.Kind == ActorAgent && a.ItemID != "" && a.ItemID != itemID {
		return fmt.Errorf("%w: agent %s works on %s, not %s", ErrForbidden, a.Name, a.ItemID, itemID)
	}
	return nil
}

var _ = domain.FormatID
