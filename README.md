# Windline

Photos hang on a clothesline above your screen. Rest the pointer on the top edge
of the display and the line glides down; take your pointer away and it lifts back
up out of sight.

Windline is a Windows port of **[Tendedero](https://github.com/alejandrobujan/tendedero)**
by Alejandro Buján, a macOS menu-bar app. The behaviour, the layout arithmetic,
the spring animation and the look are all ported from it; see
[Credits and licence](#credits-and-licence) for why the name and icon differ.

## What it does

- **The line.** New screenshots and images copied to the clipboard are hung on a
  line drawn across the top of the display. Each photo is pinned to the rope with
  two clips and tilts slightly.
- **Reveal and retract.** Move the pointer into the top edge of the screen and the
  line comes down on a spring, then stays while you are using it. Move away and it
  tucks back up. `Ctrl+Alt+T` toggles it (rebindable).
- **Inbox mode.** Windows sends screenshots to `Pictures\Screenshots`. Turn Inbox
  mode on and Windline takes that folder over instead, so every new screenshot
  lands on the line as you take it. Turning it off puts the original setting back
  exactly — including removing the override if there was not one to begin with.
- **Clipboard watcher.** Copy an image anywhere and it appears on the line.
- **Photos on the line** can be opened, dragged out to another app, copied, edited
  (in Paint, the nearest Windows equivalent of Markup), revealed in Explorer,
  saved to the Desktop, or discarded.
- **Tray icon.** Settings, the folders being watched, the shortcut and Quit.
- **Open at login**, through the usual per-user Run key.
- **Full-screen aware.** If a game or a video is full screen on the monitor, the
  line stays out of the way rather than dropping onto it.
- **Nine languages**, matching the original's.

## Requirements

Windows 10 or later, 64-bit. That is all: the executable is self-contained and
has no installer, no service and no runtime dependency.

## Building

You need the [Go toolchain](https://go.dev/dl/) (1.21 or newer) and nothing else.
There is no C compiler, no Windows SDK and **no third-party Go package** — the
Win32, GDI+, shell and registry bindings are all hand-written `syscall` FFI in
this repository.

```powershell
pwsh -File build.ps1
```

That vets, checks formatting, draws the icon, compiles the resources, builds
`windline.exe` and runs the headless tests. Add `-LiveTest` to also run the screen
tests, or `-SkipTests` to build only.

To build by hand:

```powershell
go build -ldflags="-s -w -H windowsgui" -o windline.exe .
```

Note that a hand-built `go build` with no `windline_amd64.syso` present still works;
it just produces an executable with no icon and no version information, which is
why `build.ps1` generates that file first.

### Why `go vet -unsafeptr=false`

The FFI layer converts `uintptr` values back into pointers, which is exactly what
vet's `unsafeptr` check exists to catch. Every such conversion is deliberate and
kept in one file, [`ffi.go`](ffi.go), with the reasoning written out. This is the
only vet check that is disabled.

## Testing

The app is a GUI with no console, so it has two headless modes that make
themselves observable from outside.

```powershell
.\windline.exe --selftest --out=_selftest   # 17 checks, headless, fast
.\windline.exe --livetest --out=_livetest   # reveals the real line and checks the screen
```

`--selftest` builds the app in memory and checks the things that can be checked
without a desktop: that the layout arithmetic still matches the original's
(104 px photo height, 150 px card width, 174 px spacing, 210 px panel height,
9 cards on a 1920 wide screen), that the rope is a parabola with the right sag,
that cards are opaque while the gaps between them are transparent, that the
clipboard round-trips a PNG, that the Inbox redirect writes and restores the
registry value, that the icon renders at every size, and that the reveal spring
settles. It leaves a PNG of a composed frame so the drawing can be inspected.

`--livetest` starts the real application, brings the line down with the real
shortcut, photographs the strip of screen where it appears, tucks it away again
and inspects the pixels. It is deliberately careful about what it claims:

- Opaque pixels are compared against the composition that was drawn to produce
  them, so the check needs no reference to the desktop at all.
- Pixels the window blended with the desktop are checked against an *interval*,
  bracketed by a capture taken immediately before the reveal and one taken
  immediately after the tuck. Those two captures span exactly the interval the
  window was on screen, so the difference between them is this desktop's own
  movement, however slow.
- Anything that moved while the desktop was watched *alone*, before the app was
  brought down, is excluded from the blended check and counted. The number left
  usable is itself asserted, so a restless desktop cannot turn the check into a
  formality by excluding everything.
- After the tuck, every pixel the window had drawn must be showing the desktop
  again, and the process must own no visible window.

Without an image-capable reviewer, visual verification is numeric: the tests
write PNGs to their `--out` directory, and `%LOCALAPPDATA%\Windline\windline.log`
records every run.

## Where things are kept

| What | Where |
| --- | --- |
| Settings | `%APPDATA%\Windline\settings.ini` |
| Log | `%LOCALAPPDATA%\Windline\windline.log` (rolls over at 512 KB) |
| Managed screenshots, Inbox mode only | `%LOCALAPPDATA%\Windline\Screenshots` |

The app writes nothing to the registry except the Inbox redirect and the optional
"Open at login" value, and removes both when you turn those off.

## How it is put together

The source is one package, and each file is a chapter of the original ported to
its Windows equivalent.

| File | What it is |
| --- | --- |
| `main.go` | Entry point, single instance, message loop, test modes |
| `app.go` | State machine: reveal, tuck, peek, hover, full-screen check |
| `panel.go` | The layered window, and the window procedure |
| `render.go` | Composing a frame: the rope, the cards, the hint, the cross |
| `line.go` | The photos on the line and everything you can do to one |
| `layout.go` | The original's geometry, arithmetic for arithmetic |
| `anim.go` | The springs |
| `win32.go`, `gdi32.go`, `gdiplus.go`, `procs_extra.go`, `ffi.go` | The platform bindings |
| `watch.go` | Folder watching, via overlapped `ReadDirectoryChangesW` |
| `clipboard.go` | The clipboard watcher and DIB↔PNG conversion |
| `inbox.go` | Taking over the Screenshots known folder |
| `tray.go`, `dialog.go`, `hotkey.go`, `sound.go` | Shell integration |
| `flight.go`, `drag.go` | Photos flying in and out, and dragging one to another app |
| `i18n.go` | The translations, generated by `tools/gen_i18n.py` |
| `selftest.go`, `livetest.go` | The two test modes |
| `tools/mkrsrc` | The resource compiler used by `build.ps1` |

Each file's header comment explains the Mac feature it came from and the Windows
mechanism it was mapped onto, because that mapping is the interesting part of a
port.

### Windows mappings

Some of the original's behaviour has no direct counterpart, so it was mapped onto
the closest thing Windows actually provides:

- The menu-bar hover becomes **the top edge of whichever monitor the pointer is
  on**, with the taskbar taken into account.
- The screenshot location (`com.apple.screencapture location`) becomes the
  **Screenshots known folder** in `HKCU\...\Explorer\User Shell Folders`.
- "Open at login" becomes the per-user **Run key**.
- Markup becomes **Paint**, via the `edit` verb.
- Quick Look has no exact equivalent; the shell's **preview** is used.
- The floating screenshot thumbnail, which macOS lets an app switch off, has no
  Windows counterpart — the Snipping Tool owns its own toast — so it is left alone.

## Credits and licence

Windline is a port of [Tendedero](https://github.com/alejandrobujan/tendedero) by
**Alejandro Buján**. The idea, the design, the geometry and the animation are his.

Tendedero's source is MIT licensed, and this port is too — see [`LICENSE`](LICENSE),
which carries both copyright notices. The licence covers the source code, but
**not the Tendedero name or its icon**, so this port ships under a different name
with an icon of its own, drawn in code by the app itself (`--mkicon`). If you
build on this, keep that distinction.