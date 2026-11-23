//go:build unix

package execx

import (
	"os/exec"
	"syscall"
	"time"
)

// KillGroupOnCancel puts cmd in its own process group and makes context
// cancellation kill the whole group (shells, Gradle daemons started by the
// command, test runners), not only the direct child. grace bounds how long
// Wait keeps waiting for pipes held by stray descendants.
func KillGroupOnCancel(cmd *exec.Cmd, grace time.Duration) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative pid targets the process group.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = grace
}
