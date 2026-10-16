package main

import (
	"syscall"
	"unsafe"
)

// tray.go is the menu, which is where every setting lives in the original: the
// macOS app has a menu bar item with the same list. Windows gets the same list in
// the notification area through Shell_NotifyIcon.
//
// Two Windows specifics worth naming. First, a tray icon must be re-added after
// Explorer restarts: the shell broadcasts "TaskbarCreated", and the icon is
// registered again when that arrives. Second, a popup menu must be dismissed
// properly — SetForegroundWindow before TrackPopupMenu and a WM_NULL posted
// afterwards — or it stays on screen after a click elsewhere.

const (
	// Menu command ids, in the order the menu lists them.
	cmdHideLine = 100 + iota
	cmdTakeEverythingDown
	cmdOpenScreenshotsFolder
	cmdSounds
	cmdOpenAtLogin
	cmdKeepAsManyAsFit
	cmdKeep25
	cmdKeep50
	cmdKeep100
	cmdAddFolder
	cmdRemoveFolder
	cmdFolderBase // 10 folder slots follow
	cmdShortcut
	cmdShortcutNone
	cmdSizeSmall
	cmdSizeMedium
	cmdSizeLarge
	cmdBringDownTopEdge
	cmdBringDownNew
	cmdOverFullScreen
	cmdHangCopiedImages
	cmdInboxMode
	cmdOpenWindlineFolder
	cmdOpenSettingsFile
	cmdAbout
	cmdQuit
	cmdShowLine
)

const (
	// The folder slots in the "Also watch folders" submenu.
	folderSlotCount = 10
	trayIconID      = 1
)

// menuHandler is the app's command entry point, wired so the menu code stays
// free of app internals.
type menuState struct {
	quit func()
	set  *settings
	line *line
	app  *app
}

func (a *app) buildTray() {
	icon := a.logos.icon(16, a.set.clipboardEnabled())
	nid := NOTIFYICONDATA{
		CbSize:           uint32(unsafe.Sizeof(NOTIFYICONDATA{})),
		HWnd:             a.panel.hwnd,
		UID:              trayIconID,
		UFlags:           nifMessage | nifIcon | nifTip,
		UCallbackMessage: wmNotifyIcon,
		HIcon:            icon,
	}
	copyStringToUTF16(nid.SzTip[:], "Windline — screenshots hang on a line")
	if call(pShellNotifyIcon, nimAdd, uptr(&nid)) == 0 {
		logf("could not add the tray icon: %d", lastError())
		return
	}
	trayAdded = true

	// The shell's version, so right-click behaves like a native icon.
	nid.UFlags = nimSetVersion
	nid.UVersion = notifyIconV4
	call(pShellNotifyIcon, nimSetVersion, uptr(&nid))
	logf("tray icon added")
}

var trayAdded bool

// refreshTrayIcon redraws the icon after a setting that changes it.
func (a *app) refreshTrayIcon() {
	if !trayAdded {
		return
	}
	icon := a.logos.icon(16, a.set.clipboardEnabled())
	nid := NOTIFYICONDATA{
		CbSize: uint32(unsafe.Sizeof(NOTIFYICONDATA{})),
		HWnd:   a.panel.hwnd,
		UID:    trayIconID,
		UFlags: nifIcon,
		HIcon:  icon,
	}
	call(pShellNotifyIcon, nimModify, uptr(&nid))
}

func (a *app) removeTray() {
	if !trayAdded {
		return
	}
	trayAdded = false
	nid := NOTIFYICONDATA{
		CbSize: uint32(unsafe.Sizeof(NOTIFYICONDATA{})),
		HWnd:   a.panel.hwnd,
		UID:    trayIconID,
	}
	call(pShellNotifyIcon, nimDelete, uptr(&nid))
}

// onTrayMessage handles clicks on the tray icon.
func (a *app) onTrayMessage(lparam uintptr) {
	switch int(uint32(lparam)) {
	case wmLButtonUp, wmLButtonDblClk:
		// A left click brings the line down, like clicking the menu bar icon.
		a.onHotkey(hotkeyToggle)
	case wmRButtonUp, wmContextMenu:
		a.showTrayMenu()
	}
}

// showTrayMenu builds and shows the whole menu.
func (a *app) showTrayMenu() {
	menu := call(pCreatePopupMenu)
	if menu == 0 {
		return
	}
	defer call(pDestroyMenu, menu)

	lineLive := a.line.liveCount()

	// --- the two things you do most
	addItem(menu, cmdShowLine, L("Bring the line down"), lineLive > 0, false)
	addItem(menu, cmdHideLine, L("Hide the line"), true, a.set.lineHidden())
	addItem(menu, cmdTakeEverythingDown, L("Take everything down"), lineLive > 0, false)
	addSeparator(menu)
	addItem(menu, cmdOpenScreenshotsFolder, L("Open screenshots folder"), true, false)
	addSeparator(menu)

	// --- screenshots and sounds
	addItem(menu, cmdSounds, L("Sounds"), true, a.set.soundOn())
	addItem(menu, cmdHangCopiedImages, L("Hang copied images"), true, a.set.clipboardEnabled())
	addItem(menu, cmdInboxMode, L("Handle screenshots"), true, a.set.inboxEnabled())
	addSeparator(menu)

	// --- the line's behaviour
	addItem(menu, cmdBringDownTopEdge, L("Open at the top edge"), true, a.set.opensFromTop())
	addItem(menu, cmdOverFullScreen, L("Over full screen apps"), true, a.set.showOverFullScreen())
	addItem(menu, cmdOpenAtLogin, L("Open at login"), true, openAtLoginEnabled())
	addSeparator(menu)

	// --- size
	sizeMenu := call(pCreatePopupMenu)
	addItem(sizeMenu, cmdSizeSmall, L("Small"), true, a.set.lineSize() == SizeSmall)
	addItem(sizeMenu, cmdSizeMedium, L("Medium"), true, a.set.lineSize() == SizeMedium)
	addItem(sizeMenu, cmdSizeLarge, L("Large"), true, a.set.lineSize() == SizeLarge)
	addSubmenu(menu, sizeMenu, L("Size"))

	// --- keep on line
	keepMenu := call(pCreatePopupMenu)
	addItem(keepMenu, cmdKeepAsManyAsFit, L("As many as fit"), true, a.set.keepOnLine() == 0)
	for i, n := range keepChoices {
		// The count goes through the translation table, so
		// "25 photos" reads as "25 fotos" in Spanish.
		addItem(keepMenu, cmdKeep25+i, Lf("%d photos", n), true, a.set.keepOnLine() == n)
	}
	addSubmenu(menu, keepMenu, L("Keep on line"))

	// --- extra folders
	folderMenu := call(pCreatePopupMenu)
	folders := a.set.extraFolders()
	for i := 0; i < folderSlotCount; i++ {
		label := L("Nothing yet")
		if i < len(folders) {
			label = shortenPath(folders[i])
		}
		addItem(folderMenu, cmdRemoveFolder+i, label, i < len(folders), false)
	}
	addSeparator(folderMenu)
	addItem(folderMenu, cmdAddFolder, L("Add a folder…"), true, false)
	addSubmenu(menu, folderMenu, L("Also watch folders"))

	// --- shortcut
	scMenu := call(pCreatePopupMenu)
	addItem(scMenu, cmdShortcut, currentShortcut(a.set).Describe(), true, false)
	addSeparator(scMenu)
	addItem(scMenu, cmdShortcutNone, L("Back to the default"), true, false)
	addSubmenu(menu, scMenu, L("Shortcut"))

	addSeparator(menu)
	addItem(menu, cmdOpenWindlineFolder, L("Open Windline's folder"), true, false)
	addItem(menu, cmdOpenSettingsFile, L("Open windline.ini"), true, false)
	addItem(menu, cmdAbout, L("About Windline"), true, false)
	addSeparator(menu)
	addItem(menu, cmdQuit, L("Quit"), true, false)

	// A popup menu needs its owner to be foreground, or it does not dismiss.
	call(pSetForegroundWindow, a.panel.hwnd)
	var pt POINT
	call(pGetCursorPos, uptr(&pt))
	call(pTrackPopupMenu, menu, tpmRightButton|tpmReturnCmd, u(int(pt.X)), u(int(pt.Y)), 0, a.panel.hwnd, 0)
	call(pPostMessage, a.panel.hwnd, wmNull, 0, 0)
	// The submenus are owned by the parent and freed with it.
}

// showItemMenu is the right-click menu on a photo itself.
func (a *app) showItemMenu(it *item, pt POINT) {
	menu := call(pCreatePopupMenu)
	if menu == 0 {
		return
	}
	defer call(pDestroyMenu, menu)

	const (
		itemCopy = 700 + iota
		itemOpen
		itemMarkup
		itemSaveToDesktop
		itemShowInExplorer
		itemTakeDownLeft
		itemTakeDownRight
		itemLetGo
	)
	has := it != nil

	addItem(menu, itemCopy, L("Copy"), has, false)
	addItem(menu, itemOpen, L("Open"), has, false)
	addItem(menu, itemMarkup, L("Edit"), has && !isRecording(itPath(it)), false)
	addItem(menu, itemShowInExplorer, L("Show in Explorer"), has, false)
	addSeparator(menu)
	addItem(menu, itemSaveToDesktop, L("Save to the Desktop"), has, false)
	if has && a.line.isInInbox(it.ID) {
		// A file in Windline's own folder is deleted for real, so that is what
		// the label must say.
		addItem(menu, itemLetGo, L("Move to the Recycle Bin"), true, false)
	} else {
		addItem(menu, itemLetGo, L("Take it off the line"), true, false)
	}
	addSeparator(menu)
	addItem(menu, itemTakeDownLeft, L("Take down the ones before it"), has, false)
	addItem(menu, itemTakeDownRight, L("Take down the ones after it"), has, false)

	call(pSetForegroundWindow, a.panel.hwnd)
	cmd := call(pTrackPopupMenu, menu, tpmRightButton|tpmReturnCmd, u(int(pt.X)), u(int(pt.Y)), 0, a.panel.hwnd, 0)
	call(pPostMessage, a.panel.hwnd, wmNull, 0, 0)

	if it == nil {
		return
	}
	switch int(uint32(cmd)) {
	case itemCopy:
		a.onClick(it.ID)
	case itemOpen:
		a.line.openFile(it.ID)
	case itemMarkup:
		a.line.markup(it.ID)
	case itemShowInExplorer:
		a.line.showInExplorer(it.ID)
	case itemSaveToDesktop:
		a.line.saveToDesktop(it.ID)
		a.repaint(true)
	case itemLetGo:
		a.line.discard(it.ID, false)
		a.orderBackdropRefresh()
	case itemTakeDownLeft:
		a.line.takeDown(it.ID, true)
	case itemTakeDownRight:
		a.line.takeDown(it.ID, false)
	}
}

// onMenuCommand dispatches a tray menu command.
func (a *app) onMenuCommand(cmd int) {
	switch cmd {
	case cmdShowLine:
		a.pinned = true
		a.keepOpen = a.line.liveCount() == 0
		a.reveal()
	case cmdHideLine:
		a.set.setLineHidden(!a.set.lineHidden())
		if a.set.lineHidden() {
			a.tuck()
		} else {
			a.reveal()
		}
	case cmdTakeEverythingDown:
		a.line.clear()
		a.orderBackdropRefresh()
	case cmdOpenScreenshotsFolder:
		shellOpen(a.currentScreenshotFolder())
	case cmdSounds:
		a.set.setSoundOn(!a.set.soundOn())
	case cmdOpenAtLogin:
		setOpenAtLogin(!openAtLoginEnabled())
	case cmdKeepAsManyAsFit:
		a.set.setKeepOnLine(0)
		a.updateLayout()
		a.repaint(true)
	case cmdKeep25, cmdKeep50, cmdKeep100:
		n := keepChoices[cmd-cmdKeep25]
		a.set.setKeepOnLine(n)
		a.updateLayout()
		a.repaint(true)
	case cmdAddFolder:
		if dir := pickFolder(a.panel.hwnd, L("Pick a folder to watch")); dir != "" {
			dirs := dedupePaths(append(a.set.extraFolders(), dir))
			a.set.setExtraFolders(dirs)
			a.rebuildFolderWatchers()
		}
	case cmdShortcut:
		if sc, ok := captureShortcut(a.panel.hwnd); ok {
			if !reRegisterHotkey(a.panel.hwnd, sc) {
				messageBox(a.panel.hwnd, L("That shortcut is already taken by another app."), L("Windline"))
			}
		}
	case cmdShortcutNone:
		if !reRegisterHotkey(a.panel.hwnd, defaultShortcutKey) {
			messageBox(a.panel.hwnd, L("That shortcut is already taken by another app."), L("Windline"))
		}
	case cmdSizeSmall, cmdSizeMedium, cmdSizeLarge:
		size := []Size{SizeSmall, SizeMedium, SizeLarge}[cmd-cmdSizeSmall]
		a.set.setLineSize(size)
		a.layout = Layout{Size: size, Dpi: a.scale}
		a.reloadEverything()
	case cmdBringDownTopEdge:
		a.set.setOpensFromTop(!a.set.opensFromTop())
	case cmdOverFullScreen:
		a.set.setShowOverFullScreen(!a.set.showOverFullScreen())
	case cmdHangCopiedImages:
		on := !a.set.clipboardEnabled()
		a.set.setClipboardEnabled(on)
		if on {
			a.clipboard.start(a.panel.hwnd)
		} else {
			a.clipboard.stop()
		}
		a.refreshTrayIcon()
	case cmdInboxMode:
		on := !a.set.inboxEnabled()
		a.set.setInboxEnabled(on)
		if on {
			applyInbox()
		} else {
			restoreInbox()
		}
		a.startWatchers()
		logf("Handle screenshots set to %v", on)
	case cmdOpenWindlineFolder:
		shellOpen(inboxFolder())
	case cmdOpenSettingsFile:
		shellOpen(a.set.path)
	case cmdAbout:
		messageBox(a.panel.hwnd,
			L("Windline — screenshots hang on a clothesline above your screen. Click one to copy it, press-and-hold to edit it, drag it out to keep it.")+
				"\n\n"+L("A Windows port of the macOS app Tendedero by Alejandro Buján. Windline uses its own name and icon, as that licence requires."),
			L("About Windline"))
	case cmdQuit:
		a.quit()
	default:
		// The folder slots: cmdRemoveFolder..cmdRemoveFolder+folderSlotCount-1
		if cmd >= cmdRemoveFolder && cmd < cmdRemoveFolder+folderSlotCount {
			a.removeFolderAt(cmd - cmdRemoveFolder)
		}
	}
}

func (a *app) removeFolderAt(index int) {
	dirs := a.set.extraFolders()
	if index < 0 || index >= len(dirs) {
		return
	}
	dirs = append(dirs[:index:index], dirs[index+1:]...)
	a.set.setExtraFolders(dirs)
	a.rebuildFolderWatchers()
	logf("stopped watching %s", a.set.extraFolders())
}

// reloadEverything re-decodes every thumbnail after the Size setting changes.
func (a *app) reloadEverything() {
	a.line.reloadThumbnails()
	a.updateLayout()
	a.render.discardAllCards()
	a.render.invalidateBackdrop()
	a.repaint(true)
}

// currentScreenshotFolder is the folder the Open screenshots folder item opens,
// and the one Inbox mode would take over.
func (a *app) currentScreenshotFolder() string {
	if a.set.inboxEnabled() {
		return inboxFolder()
	}
	return screenshotsFolder()
}

func (a *app) quit() {
	a.shutdown()
	call(pPostQuitMessage, 0)
}

// ---------------------------------------------------------------- menu helpers

func addItem(menu uintptr, id int, label string, enabled, checked bool) {
	flags := uintptr(mfString)
	if !enabled {
		flags |= mfGrayed
	}
	if checked {
		flags |= mfChecked
	}
	call(pAppendMenu, menu, flags, u(id), utf16Ptr(label))
}

func addSubmenu(menu, sub uintptr, label string) {
	call(pAppendMenu, menu, mfString|mfPopup, sub, utf16Ptr(label))
}

func addSeparator(menu uintptr) {
	call(pAppendMenu, menu, mfSeparator, 0, 0)
}

// itPath is a small guard for the menu, which has only an *item.
func itPath(it *item) string {
	if it == nil {
		return ""
	}
	return it.Path
}

// shortenPath keeps the last two components of a path, which is what fits in a
// menu and is enough to recognise the folder.
func shortenPath(p string) string {
	sep := string(filepathSeparator)
	parts := splitPath(p)
	if len(parts) <= 2 {
		return p
	}
	return "…" + sep + joinPath(parts[len(parts)-2:])
}

func copyStringToUTF16(dst []uint16, s string) {
	u := syscall.StringToUTF16(s)
	copy(dst, u)
	dst[len(dst)-1] = 0
}

func messageBox(hwnd uintptr, text, title string) {
	const mbOKIconInfo = 0x00000040
	call(pMessageBox, hwnd, utf16Ptr(text), utf16Ptr(title), mbOKIconInfo)
}
