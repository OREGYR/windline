package main

import (
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// inbox.go is Inbox.swift: the switch that makes Windline look after your
// screenshots instead of letting the system scatter them.
//
// On macOS the original redirects the system's screenshot location
// (com.apple.screencapture location) at its own folder, watches that folder, and
// puts the previous setting back when the feature is turned off. Windows has an
// exact equivalent: Win+PrtScn and the Snipping Tool both resolve the
// Screenshots known folder ({B7BEDE81-DF94-4682-A7D8-57A71709C8E8}) through
// HKCU\...\Explorer\User Shell Folders, so pointing that value at Windline's
// folder sends every new screenshot there, and restoring the old value puts
// things back exactly as they were.
//
// Two things are deliberately not touched. The floating thumbnail preview that
// macOS can disable has no Windows counterpart (the Snipping Tool shows its own
// toast, which the app is not entitled to switch off), and the Desktop is never
// watched: on Windows the Desktop is not where screenshots land.

const (
	// The Screenshots known folder's GUID, as it appears in the registry.
	screenshotsFolderGUID = "{B7BEDE81-DF94-4682-A7D8-57A71709C8E8}"
	// FOLDERID_Screenshots, for the API calls.
)

// previousScreenshotsLocation is what the registry held before Inbox mode took
// over, remembered so it can be put back. It is stored in the app's own settings
// as well, so a crash does not lose it.
func previousScreenshotsFolder() string {
	if v := appSettings.get(keyInboxSaved, ""); v != "" {
		return v
	}
	return ""
}

// applyInbox points the Screenshots known folder at Windline's own folder. The
// previous value is saved first, and only once, so toggling the feature over and
// over cannot lose the original.
func applyInbox() error {
	if appSettings.get(keyInboxSaved, "") == "" {
		// Save what the folder resolves to now, and what the registry literally
		// says (which is usually just "Screenshots" or an absolute path).
		current := screenshotsFolder()
		raw := readShellFolderValue(screenshotsFolderGUID)
		appSettings.set(keyInboxSaved, current)
		appSettings.set(keyInboxRaw, raw)
		logf("inbox: screenshots were going to %q (registry: %q)", current, raw)
	}

	target := inboxFolder()
	if err := writeShellFolderValue(screenshotsFolderGUID, target); err != nil {
		logf("inbox: could not redirect the Screenshots folder: %v", err)
		return err
	}
	logf("inbox: screenshots now go to %s", target)

	// Windows resolves the folder lazily and caches it in the shell, so a
	// SHChangeNotify tells Explorer to re-read it; without that the redirect only
	// takes effect for newly started processes.
	notifyShellFolderChanged()
	return nil
}

// restoreInbox puts the previous screenshot location back.
func restoreInbox() error {
	saved := appSettings.get(keyInboxSaved, "")
	raw := appSettings.get(keyInboxRaw, "")
	if saved == "" {
		return nil // the redirect was never applied, so there is nothing to undo
	}
	if raw == "" {
		// The registry held no value at all before the redirect: the folder resolved
		// from the implicit default. Putting the resolved path back would leave an
		// explicit value where none existed, which resolves to the same folder but is
		// not the state that was found, so the override is removed outright.
		deleteRegValue(hkeyCurrentUser, shellFolderKey, screenshotsFolderGUID)
		logf("inbox: removed the Screenshots override, since there was none before (was going to %q)", saved)
	} else {
		// Otherwise the literal registry value goes back, because that is exactly
		// what was there: it is usually a relative name like "Screenshots", not the
		// absolute path it resolves to.
		if err := writeShellFolderValue(screenshotsFolderGUID, raw); err != nil {
			logf("inbox: could not restore the Screenshots folder: %v", err)
			return err
		}
		logf("inbox: screenshots are going back to %q", raw)
	}
	notifyShellFolderChanged()
	appSettings.set(keyInboxSaved, "")
	appSettings.set(keyInboxRaw, "")
	return nil
}

// ---------------------------------------------------------------- registry

// readShellFolderValue reads the raw value under User Shell Folders.
func readShellFolderValue(guid string) string {
	return readRegString(hkeyCurrentUser, shellFolderKey, guid)
}

// writeShellFolderValue sets the raw value under User Shell Folders. The shell
// stores these as REG_EXPAND_SZ so that %USERPROFILE% still expands; an absolute
// path is equally valid, which is what is written here.
func writeShellFolderValue(guid, value string) error {
	return writeRegString(hkeyCurrentUser, shellFolderKey, guid, value, regExpandSZ)
}

// notifyShellFolderChanged asks the shell to re-read its folder settings.
func notifyShellFolderChanged() {
	const (
		shcneAssocChanged = 0x08000000
		shcnfIDList       = 0x0000
	)
	call(pSHChangeNotify, shcneAssocChanged, shcnfIDList, 0, 0)
}

// ---------------------------------------------------------------- registry API

func readRegString(root uintptr, subkey, name string) string {
	var hkey uintptr
	if call(pRegOpenKeyEx, root, utf16Ptr(subkey), 0, keyQueryValue, uptr(&hkey)) != 0 {
		return ""
	}
	defer call(pRegCloseKey, hkey)
	var typ, size uint32 = 0, 0
	if call(pRegQueryValue, hkey, utf16Ptr(name), 0, uptr(&typ), 0, uptr(&size)) != 0 || size == 0 {
		return ""
	}
	buf := make([]uint16, size/2+1)
	if call(pRegQueryValue, hkey, utf16Ptr(name), 0, uptr(&typ), uintptr(unsafe.Pointer(&buf[0])), uptr(&size)) != 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

func writeRegString(root uintptr, subkey, name, value string, typ uint32) error {
	var hkey uintptr
	var disp uint32
	// RegCreateKeyExW takes nine arguments and every one of them has to be passed:
	// hKey, lpSubKey, Reserved, lpClass, dwOptions, samDesired, lpSecurityAttributes,
	// phkResult, lpdwDisposition. Passing one too few shifts the rest along, so
	// samDesired receives the security-attributes argument — which is 0, giving
	// ERROR_INVALID_PARAMETER (87) — and phkResult is handed the disposition pointer,
	// writing an 8-byte key handle into a 4-byte variable. dwOptions really is 0 here,
	// which is the default (REG_OPTION_NON_VOLATILE).
	if r := call(pRegCreateKeyEx, root, utf16Ptr(subkey), 0, 0, 0,
		u32(keySetValue|keyCreateSub), 0, uptr(&hkey), uptr(&disp)); r != 0 {
		return regErr(r)
	}
	defer call(pRegCloseKey, hkey)
	data := syscall.StringToUTF16(value)
	size := uintptr(len(data) * 2)
	if r := call(pRegSetValueEx, hkey, utf16Ptr(name), 0, u32(typ),
		uintptr(unsafe.Pointer(&data[0])), size); r != 0 {
		return regErr(r)
	}
	return nil
}

func deleteRegValue(root uintptr, subkey, name string) {
	var hkey uintptr
	if call(pRegOpenKeyEx, root, utf16Ptr(subkey), 0, keySetValue, uptr(&hkey)) != 0 {
		return
	}
	defer call(pRegCloseKey, hkey)
	call(pRegDeleteValue, hkey, utf16Ptr(name))
}

type regError uintptr

func (e regError) Error() string { return "registry error " + itoa(int(e)) }

func regErr(r uintptr) error { return regError(r) }

// ---------------------------------------------------------------- open at login

const runValueName = "Windline"

// openAtLoginEnabled reports whether the Run key launches Windline.
func openAtLoginEnabled() bool {
	return readRegString(hkeyCurrentUser, runKeyPath, runValueName) != ""
}

// setOpenAtLogin adds or removes the Run entry. The command is quoted, because
// the executable lives under a path with spaces often enough.
func setOpenAtLogin(on bool) {
	if on {
		exe := exePath()
		if exe == "" {
			return
		}
		cmd := "\"" + exe + "\" --startup"
		if err := writeRegString(hkeyCurrentUser, runKeyPath, runValueName, cmd, regSZ); err != nil {
			logf("could not set Open at login: %v", err)
			return
		}
		logf("open at login set to %s", cmd)
		return
	}
	deleteRegValue(hkeyCurrentUser, runKeyPath, runValueName)
	logf("open at login removed")
}

// ---------------------------------------------------------------- paths

var (
	filepathSeparator = os.PathSeparator
)

func splitPath(p string) []string {
	parts := strings.Split(strings.ReplaceAll(p, "/", string(filepathSeparator)), string(filepathSeparator))
	var out []string
	for _, s := range parts {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func joinPath(parts []string) string {
	return strings.Join(parts, string(filepathSeparator))
}
