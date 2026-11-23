//go:build !unix

package provcommon

import (
	"os"
	"os/exec"
)

var (
	interruptSignal = os.Interrupt
	killSignal      = os.Kill
)

func setGroup(*exec.Cmd) {}

func signalGroup(cmd *exec.Cmd, sig os.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	if sig == os.Interrupt {
		// Interrupt is not available everywhere; kill instead.
		return cmd.Process.Kill()
	}
	return cmd.Process.Signal(sig)
}
