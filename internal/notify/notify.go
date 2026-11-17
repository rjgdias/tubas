// Package notify shows a desktop notification: a balloon on Windows,
// Notification Center on macOS, notify-send on Linux. tubas uses it when
// an agent needs you and no window is open.
package notify

import (
	"os/exec"
	"runtime"
	"strings"
)

// Send shows a notification and returns without waiting for it.
func Send(title, body string) error {
	cmd := command(title, body)
	if cmd == nil {
		return nil
	}
	hide(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

func command(title, body string) *exec.Cmd {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("powershell.exe", "-NoProfile", "-WindowStyle", "Hidden", "-Command", Script(title, body))
	case "darwin":
		return exec.Command("osascript", "-e", `display notification "`+quote(body, `"`, `\"`)+`" with title "`+quote(title, `"`, `\"`)+`"`)
	default:
		if _, err := exec.LookPath("notify-send"); err != nil {
			return nil
		}
		return exec.Command("notify-send", title, body)
	}
}

// Script is the PowerShell that shows a balloon from the notification
// area for a few seconds.
func Script(title, body string) string {
	q := func(s string) string { return "'" + quote(s, "'", "''") + "'" }
	return strings.Join([]string{
		"Add-Type -AssemblyName System.Windows.Forms",
		"Add-Type -AssemblyName System.Drawing",
		"$n = New-Object System.Windows.Forms.NotifyIcon",
		"$n.Icon = [System.Drawing.SystemIcons]::Information",
		"$n.Text = 'tubas'",
		"$n.Visible = $true",
		"$n.ShowBalloonTip(8000, " + q(title) + ", " + q(body) + ", 'Info')",
		"Start-Sleep -Seconds 9",
		"$n.Dispose()",
	}, "; ")
}

func quote(s, old, repl string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, old, repl)
}
