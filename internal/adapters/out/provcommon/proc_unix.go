//go:build unix

package provcommon

import (
	"os/exec"
	"syscall"
)

var (
	interruptSignal = syscall.SIGINT
	killSignal      = syscall.SIGKILL
)

func setGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// signalGroup signals the whole process group of cmd.
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	if err := syscall.Kill(-pid, sig); err != nil {
		return cmd.Process.Signal(sig)
	}
	return nil
}
