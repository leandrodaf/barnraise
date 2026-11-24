package domain

import (
	"path"
	"sort"
	"strings"
)

// Event types of the trust layer (ADR-0029, ADR-0030).
const (
	// EvTokenDenied: a session token was used for a call its session is not
	// approved for (another item, another tool, another project).
	EvTokenDenied = "security.token.denied"
	// EvChainBroken: the hash chain of the local event log does not hold.
	EvChainBroken = "security.chain.broken"
	// EvTrustRecorded: the hashes of the files Barnraise trusts in the
	// project root (barnraise.yaml, .barnraise/**) were recorded.
	EvTrustRecorded = "trust.recorded"
	// EvTrustAccepted: the owner accepted the instruction files of an item's
	// worktree that differ from the main branch.
	EvTrustAccepted = "trust.accepted"
	// EvTrustMismatch: a trusted file changed outside the sanctioned path;
	// sessions are refused until the owner re-trusts.
	EvTrustMismatch = "trust.mismatch"
)

// PolicyHashEnv carries the SHA-256 of a session's policy.json, recorded by
// the daemon when it wrote the file; the PreToolUse hook refuses every call
// when the file it reads does not match (ADR-0030).
const PolicyHashEnv = "BARNRAISE_POLICY_SHA256"

// Manifest maps a slash-separated path to the SHA-256 (hex) of its content.
type Manifest map[string]string

// TrustChange is one difference between a recorded manifest and the files.
type TrustChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"` // changed, added, removed
	Want string `json:"want,omitempty"`
	Got  string `json:"got,omitempty"`
}

// Diff lists what differs from m in cur, sorted by path.
func (m Manifest) Diff(cur Manifest) []TrustChange {
	var out []TrustChange
	for p, want := range m {
		got, ok := cur[p]
		switch {
		case !ok:
			out = append(out, TrustChange{Path: p, Kind: "removed", Want: want})
		case got != want:
			out = append(out, TrustChange{Path: p, Kind: "changed", Want: want, Got: got})
		}
	}
	for p, got := range cur {
		if _, ok := m[p]; !ok {
			out = append(out, TrustChange{Path: p, Kind: "added", Got: got})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// TrustPaths summarizes changes for a message ("a (changed), b (added)").
func TrustPaths(changes []TrustChange, max int) string {
	var parts []string
	for i, c := range changes {
		if max > 0 && i == max {
			parts = append(parts, "…")
			break
		}
		parts = append(parts, c.Path+" ("+c.Kind+")")
	}
	return strings.Join(parts, ", ")
}

// instructionNames are files the agent CLIs read as instructions wherever
// they are in the tree.
var instructionNames = map[string]bool{
	"CLAUDE.md": true, "CLAUDE.local.md": true, "AGENTS.md": true, "AGENTS.override.md": true, "GEMINI.md": true,
}

// instructionDirs are folders whose every file configures an agent CLI or
// Barnraise (hooks, settings, commands, MCP servers, roles, rules).
var instructionDirs = []string{".barnraise", ".claude", ".codex", ".gemini"}

// InstructionDir reports whether a top-level folder holds instruction files.
func InstructionDir(name string) bool {
	for _, d := range instructionDirs {
		if name == d {
			return true
		}
	}
	return false
}

// InstructionFile reports whether a worktree path (slash-separated,
// relative) is a file an agent CLI loads as instructions or configuration:
// CLAUDE.md, AGENTS.md, GEMINI.md anywhere, .mcp.json and barnraise.yaml at
// the root, and everything under .barnraise/, .claude/, .codex/, .gemini/.
func InstructionFile(rel string) bool {
	rel = strings.TrimPrefix(path.Clean(rel), "./")
	if instructionNames[path.Base(rel)] {
		return true
	}
	if rel == ".mcp.json" || rel == "barnraise.yaml" {
		return true
	}
	first, _, nested := strings.Cut(rel, "/")
	return nested && InstructionDir(first)
}
