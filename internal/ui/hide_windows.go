//go:build windows

package ui

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps a helper (the clipboard's PowerShell) from flashing a
// console window.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
