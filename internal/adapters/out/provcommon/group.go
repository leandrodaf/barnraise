package provcommon

import "os/exec"

// SetGroup gives cmd its own process group (before Start), so the whole
// tree of the CLI can be signalled at once.
func SetGroup(cmd *exec.Cmd) { setGroup(cmd) }

// InterruptGroup asks the process group of cmd to stop (SIGINT).
func InterruptGroup(cmd *exec.Cmd) error { return signalGroup(cmd, interruptSignal) }

// KillGroup kills the process group of cmd (SIGKILL).
func KillGroup(cmd *exec.Cmd) error { return signalGroup(cmd, killSignal) }
