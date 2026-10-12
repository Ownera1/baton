// Copied from collaborative-work (same author).
package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// A new session survives CLI exit, terminal closure and MCP disconnection.
func (s *Service) spawnDetached(id, dir string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "worker.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer devnull.Close()
	cmd := exec.Command(self, "_worker", id)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, f, f
	cmd.Env = append(os.Environ(), "BATON_HOME="+s.Home, DepthEnv+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start detached worker: %w", err)
	}
	fmt.Fprintf(f, "baton: worker pid %d started for %s\n", cmd.Process.Pid, id)
	// Lets status detect a worker that died before writing its first task.json.
	if err := os.WriteFile(filepath.Join(dir, "worker.pid"), []byte(fmt.Sprint(cmd.Process.Pid)), 0600); err != nil {
		fmt.Fprintln(f, "record worker pid:", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
