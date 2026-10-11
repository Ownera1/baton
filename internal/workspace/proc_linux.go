//go:build linux

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

func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// applyNice sets the priority before exec, which is how Linux expresses it.
func applyNice(cmd *exec.Cmd, nice int) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Go's SysProcAttr has no Linux Nice field. Apply priority after start.
}

// applyNiceRunning is unnecessary on Linux: the priority is already set.
func applyNiceRunning(pid, nice int) {
	if pid > 0 && nice > 0 {
		_ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, nice)
	}
}

func signalProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, sig); err == nil {
		return nil
	}
	return cmd.Process.Signal(sig)
}
