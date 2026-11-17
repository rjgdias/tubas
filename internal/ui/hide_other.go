//go:build !windows

package ui

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
