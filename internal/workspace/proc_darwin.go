//go:build darwin

// Copied from collaborative-work (same author).
package workspace

import (
	"os/exec"
	"syscall"
)

const (
	termSignal = syscall.SIGTERM
	killSignal = syscall.SIGKILL
)

// setProcessGroup puts the command in its own process group. Without this, a
// timed-out check leaves its children running: `go test`, `npm test` and every
// other build tool fork, and killing only the shell frees nothing.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// applyNice is a no-op before exec on Darwin: there is no SysProcAttr field for
// it. The priority is applied to the running process instead.
func applyNice(cmd *exec.Cmd, nice int) {}

// applyNiceRunning lowers the scheduling priority of a process that has already
// started. Lowering priority is always permitted for one's own children, so this
// needs no privilege and cannot fail in a way that matters.
func applyNiceRunning(pid, nice int) {
	if pid <= 0 || nice == 0 {
		return
	}
	// setpriority(which, who, prio): PRIO_PROCESS is 0, then the pid.
	_, _, _ = syscall.Syscall(syscall.SYS_SETPRIORITY, 0, uintptr(pid), uintptr(nice))
}

// signalProcessGroup signals the whole group, falling back to the single
// process when the group is gone.
func signalProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, sig); err == nil {
		return nil
	}
	return cmd.Process.Signal(sig)
}
