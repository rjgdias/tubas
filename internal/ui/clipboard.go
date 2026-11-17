package ui

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"unicode/utf16"

	osc52 "github.com/aymanbagabas/go-osc52/v2"
	tea "github.com/charmbracelet/bubbletea"
)

// copyText puts text on the clipboard: through the terminal (OSC 52,
// which also works over SSH in terminals that allow it) and through the
// system's own tool (clip, pbcopy, wl-copy or xclip).
func copyText(text string) tea.Cmd {
	return func() tea.Msg {
		if text == "" {
			return noticeMsg(dimStyle.Render("nothing to copy"))
		}
		os.Stdout.WriteString(osc52.New(text).String())
		if err := systemClipboard(text); err != nil {
			return noticeMsg(warnStyle.Render("copied through the terminal only: " + err.Error()))
		}
		return noticeMsg(copiedNotice(text))
	}
}

func systemClipboard(text string) error {
	var cmd *exec.Cmd
	var input []byte
	switch runtime.GOOS {
	case "windows":
		// Set-Clipboard takes the text as it is. (clip.exe needs a byte
		// order mark to read accents, and then puts that mark on the
		// clipboard too: an invisible character in front of every paste.)
		script := "[Console]::InputEncoding = [Text.Encoding]::UTF8; Set-Clipboard -Value ([Console]::In.ReadToEnd())"
		cmd = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
		input = []byte(strings.ReplaceAll(text, "\n", "\r\n"))
		if err := runClipboard(cmd, input); err == nil {
			return nil
		}
		u := utf16.Encode([]rune(strings.ReplaceAll(text, "\n", "\r\n")))
		var b bytes.Buffer
		b.Write([]byte{0xFF, 0xFE})
		binary.Write(&b, binary.LittleEndian, u)
		cmd, input = exec.Command("clip.exe"), b.Bytes()
	case "darwin":
		cmd, input = exec.Command("pbcopy"), []byte(text)
	default:
		for _, c := range [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}} {
			if _, err := exec.LookPath(c[0]); err == nil {
				cmd, input = exec.Command(c[0], c[1:]...), []byte(text)
				break
			}
		}
		if cmd == nil {
			return errors.New("no clipboard tool (wl-copy, xclip or xsel)")
		}
	}
	return runClipboard(cmd, input)
}

func runClipboard(cmd *exec.Cmd, input []byte) error {
	cmd.Stdin = bytes.NewReader(input)
	hideWindow(cmd)
	return cmd.Run()
}
