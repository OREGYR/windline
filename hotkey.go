package main

// hotkey.go is HotKey.swift: one global shortcut that brings the line down.
//
// The macOS original registers a hot key through Carbon. On Windows the
// equivalent is RegisterHotKey, which delivers WM_HOTKEY to the panel's window
// while it is running, no matter which app has focus. WM_HOTKEY does not need
// the window to be visible or focused, so the shortcut works with the line
// tucked away.

const hotkeyToggle = 1

// The default shortcut: Ctrl+Alt+T. Alt+T alone is common in other apps, and
// Ctrl+Shift+T is "reopen closed tab"; Ctrl+Alt+T is used by few things and is
// comfortable with the left hand.
const (
	modAlt      = 0x0001
	modControl  = 0x0002
	modShift    = 0x0004
	modWin      = 0x0008
	modNoRepeat = 0x4000
)

// defaultShortcut is what a fresh install gets.
type shortcut struct {
	Modifiers uint32
	Key       uint32
}

var defaultShortcutKey = shortcut{Modifiers: modControl | modAlt, Key: 'T'}

// currentShortcut reads the stored shortcut, falling back to the default.
func currentShortcut(set *settings) shortcut {
	mods := set.getInt(keyShortcutMods, -1)
	key := set.getInt(keyShortcutKey, -1)
	if mods < 0 || key <= 0 {
		return defaultShortcutKey
	}
	return shortcut{Modifiers: uint32(mods), Key: uint32(key)}
}

// registerHotkey arms the shortcut on the panel's window. A failure is logged and
// not fatal: another app may hold the same combination, and the line is still
// reachable through the tray menu.
func registerHotkey(hwnd uintptr) {
	sc := currentShortcut(appSettings)
	r := call(pRegisterHotKey, hwnd, hotkeyToggle, u(int(sc.Modifiers|modNoRepeat)), u(int(sc.Key)))
	if r == 0 {
		logf("could not register the shortcut %s: another app holds it", sc.Describe())
		return
	}
	logf("shortcut registered: %s", sc.Describe())
}

func unregisterHotkey(hwnd uintptr) {
	if hwnd != 0 {
		call(pUnregisterHotKey, hwnd, hotkeyToggle)
	}
}

// reRegisterHotkey swaps the shortcut when the tray menu changes it.
func reRegisterHotkey(hwnd uintptr, sc shortcut) bool {
	call(pUnregisterHotKey, hwnd, hotkeyToggle)
	if call(pRegisterHotKey, hwnd, hotkeyToggle, u(int(sc.Modifiers|modNoRepeat)), u(int(sc.Key))) == 0 {
		// Put the old one back, so a rejected change does not lose the shortcut.
		old := currentShortcut(appSettings)
		call(pRegisterHotKey, hwnd, hotkeyToggle, u(int(old.Modifiers|modNoRepeat)), u(int(old.Key)))
		return false
	}
	appSettings.setInt(keyShortcutMods, int(sc.Modifiers))
	appSettings.setInt(keyShortcutKey, int(sc.Key))
	logf("shortcut changed to %s", sc.Describe())
	return true
}

// Describe renders a shortcut the way Windows does: "Ctrl+Alt+T".
func (s shortcut) Describe() string {
	out := ""
	if s.Modifiers&modControl != 0 {
		out += "Ctrl+"
	}
	if s.Modifiers&modAlt != 0 {
		out += "Alt+"
	}
	if s.Modifiers&modShift != 0 {
		out += "Shift+"
	}
	if s.Modifiers&modWin != 0 {
		out += "Win+"
	}
	return out + keyName(s.Key)
}

// keyName is the display name of a virtual key code.
func keyName(vk uint32) string {
	switch {
	case vk >= 'A' && vk <= 'Z':
		return string(rune(vk))
	case vk >= '0' && vk <= '9':
		return string(rune(vk))
	case vk >= 0x70 && vk <= 0x87: // F1..F24
		return "F" + itoa(int(vk-0x6F))
	}
	switch vk {
	case 0x20:
		return "Space"
	case 0x0D:
		return "Enter"
	case 0x1B:
		return "Esc"
	case 0x09:
		return "Tab"
	case 0x25:
		return "Left"
	case 0x26:
		return "Up"
	case 0x27:
		return "Right"
	case 0x28:
		return "Down"
	case 0x2C:
		return "PrtScn"
	case 0x60:
		return "Num 0"
	}
	return "key " + itoa(int(vk))
}
