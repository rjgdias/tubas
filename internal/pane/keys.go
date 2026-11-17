package pane

import (
	"strings"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
)

var keyNames = map[string]rune{
	"enter": uv.KeyEnter, "tab": uv.KeyTab, "backspace": uv.KeyBackspace, "esc": uv.KeyEscape,
	"escape": uv.KeyEscape, "space": uv.KeySpace, " ": uv.KeySpace,
	"up": uv.KeyUp, "down": uv.KeyDown, "left": uv.KeyLeft, "right": uv.KeyRight,
	"home": uv.KeyHome, "end": uv.KeyEnd, "pgup": uv.KeyPgUp, "pgdown": uv.KeyPgDown,
	"insert": uv.KeyInsert, "delete": uv.KeyDelete,
	"f1": uv.KeyF1, "f2": uv.KeyF2, "f3": uv.KeyF3, "f4": uv.KeyF4, "f5": uv.KeyF5, "f6": uv.KeyF6,
	"f7": uv.KeyF7, "f8": uv.KeyF8, "f9": uv.KeyF9, "f10": uv.KeyF10, "f11": uv.KeyF11, "f12": uv.KeyF12,
}

// ParseKey reads a key the way Bubble Tea names it ("ctrl+c", "alt+up",
// "shift+tab", "a") into the emulator's key event.
func ParseKey(name string) (uv.KeyPressEvent, bool) {
	var mod uv.KeyMod
	for {
		switch {
		case strings.HasPrefix(name, "ctrl+") && len(name) > 5:
			mod |= uv.ModCtrl
			name = name[5:]
			continue
		case strings.HasPrefix(name, "alt+") && len(name) > 4:
			mod |= uv.ModAlt
			name = name[4:]
			continue
		case strings.HasPrefix(name, "shift+") && len(name) > 6:
			mod |= uv.ModShift
			name = name[6:]
			continue
		}
		break
	}
	if code, ok := keyNames[name]; ok {
		return uv.KeyPressEvent{Code: code, Mod: mod}, true
	}
	if r, size := utf8.DecodeRuneInString(name); size == len(name) && r != utf8.RuneError {
		ev := uv.KeyPressEvent{Code: r, Mod: mod}
		if mod&uv.ModCtrl == 0 {
			ev.Text = name
		}
		return ev, true
	}
	return uv.KeyPressEvent{}, false
}
