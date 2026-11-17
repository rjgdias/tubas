//go:build windows

package server

import (
	"os/exec"
	"syscall"
)

// detach starts the server in its own process group, with no console, so
// closing the terminal doesn't take it down.
func detach(cmd *exec.Cmd) {
	const createNewProcessGroup, detachedProcess = 0x00000200, 0x00000008
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess, HideWindow: true}
}
