//go:build !windows

package server

import (
	"os/exec"
	"syscall"
)

// detach starts the server in a session of its own, so closing the
// terminal (or losing the SSH connection) doesn't take it down.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
