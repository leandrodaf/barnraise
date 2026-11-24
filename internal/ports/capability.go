package ports

import (
	"context"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// ProbeRequest is what a capability probe looks at.
type ProbeRequest struct {
	// Programs are looked up on the PATH (with their version).
	Programs []string
	// Refresh probes again instead of returning the cached answer.
	Refresh bool
}

// CapabilityProber finds what this machine has (programs and their
// versions, kernel and filesystem features) for the capability catalog
// (ADR-0046). Answers are cached for a while.
type CapabilityProber interface {
	Probe(ctx context.Context, r ProbeRequest) domain.MachineProbe
	// Invalidate drops the cached answer (after an install).
	Invalidate()
}

// ToolInstaller installs the tools of domain.ToolRecipes into Barnraise's
// data folder, without root, and removes them.
type ToolInstaller interface {
	Install(ctx context.Context, id string) domain.ToolResult
	Remove(ctx context.Context, id string) domain.ToolResult
	// BinDir is the folder on the PATH that holds the installed programs.
	BinDir() string
}
