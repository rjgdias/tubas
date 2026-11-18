//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// stopTree makes a cancelled command take its children with it: it runs
// in its own process group, and the whole group is killed.
func stopTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
