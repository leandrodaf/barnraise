//go:build !unix

package execx

import (
	"os/exec"
	"time"
)

// KillGroupOnCancel kills the direct child on cancellation; process groups
// are a unix feature.
func KillGroupOnCancel(cmd *exec.Cmd, grace time.Duration) {
	cmd.WaitDelay = grace
}
