//go:build !tsnet

package tailnet

import "context"

// Embeddable reports whether this binary carries the embedded Tailscale.
const Embeddable = false

func openEmbedded(context.Context, Config) (Tailnet, error) { return nil, ErrNotBuiltIn }
