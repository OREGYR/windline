<#
.SYNOPSIS
    Builds windline.exe, with its icon, version information and manifest embedded.

.DESCRIPTION
    Everything here uses only the Go toolchain: no C compiler, no Windows SDK and
    no third-party packages. The two build tools are part of this repository
    (tools/mkrsrc) or of the app itself (--mkicon), so a clone builds anywhere
    Go is installed.

    The steps, in order:

      1. go vet with -unsafeptr=false. The FFI layer converts uintptr back into
         pointers on purpose, which is exactly what vet's unsafeptr check exists
         to flag. Every such conversion is confined to ffi.go, where the reasons
         are written down.
      2. gofmt check, so an unformatted file cannot be committed unnoticed.
      3. --mkicon draws assets/windline.ico using the app's own logo code.
      4. mkrsrc compiles the icon, manifest and a version block into
         windline_amd64.syso. The Go linker picks that file up automatically
         because it sits in the package directory.
      5. go build, with -H windowsgui so no console window appears and -s -w to
         drop the symbol table and DWARF data.
      6. Tests: --selftest always runs, because it is headless and fast. The
         screen checks in --livetest need a desktop, so they are opt-in.

.PARAMETER SkipTests
    Build only; do not run --selftest.

.PARAMETER LiveTest
    Also run --livetest. It reveals the real line over whatever is on screen for
    a couple of seconds and compares captures of the desktop, so do not use it
    while you are doing something that must not be interrupted.

.PARAMETER Out
    Output path for the executable. Defaults to windline.exe next to the sources.

.EXAMPLE
    pwsh -File build.ps1
    pwsh -File build.ps1 -LiveTest
#>
[CmdletBinding()]
param(
    [switch]$SkipTests,
    [switch]$LiveTest,
    [string]$Out = "windline.exe"
)

$ErrorActionPreference = "Stop"
$root = $PSScriptRoot
Push-Location $root
try {
    function Step($name) { Write-Host "==> $name" -ForegroundColor Cyan }

    $go = Get-Command go -ErrorAction SilentlyContinue
    if (-not $go) {
        throw "go was not found on PATH. Install Go from https://go.dev/dl/ and try again."
    }
    Write-Host "go: $($go.Source)"
    Write-Host "version: $(& go version)"

    Step "vetting"
    # -unsafeptr=false is required, not optional: see the note at the top.
    & go vet -unsafeptr=false ./...
    if ($LASTEXITCODE -ne 0) { throw "go vet failed" }

    Step "checking formatting"
    $unformatted = & gofmt -l .
    if ($unformatted) {
        Write-Host "these files are not gofmt-clean:" -ForegroundColor Yellow
        $unformatted | ForEach-Object { Write-Host "  $_" }
        throw "run: gofmt -w ."
    }

    Step "building the resource compiler"
    & go build -o mkrsrc.exe ./tools/mkrsrc
    if ($LASTEXITCODE -ne 0) { throw "building mkrsrc failed" }

    Step "drawing the application icon"
    # This needs the app built first, since --mkicon is a mode of the app itself.
    & go build -o .windline-mkicon.exe .
    if ($LASTEXITCODE -ne 0) { throw "building the icon generator failed" }
    & .\.windline-mkicon.exe --mkicon --out=assets/windline.ico
    if ($LASTEXITCODE -ne 0) { throw "generating the icon failed" }
    Remove-Item .windline-mkicon.exe -ErrorAction SilentlyContinue

    Step "compiling resources"
    & .\mkrsrc.exe -ico assets/windline.ico -manifest windline.manifest -o windline_amd64.syso
    if ($LASTEXITCODE -ne 0) { throw "compiling resources failed" }

    Step "building $Out"
    & go build -trimpath -ldflags="-s -w -H windowsgui" -o $Out .
    if ($LASTEXITCODE -ne 0) { throw "go build failed" }

    $exe = Get-Item $Out
    Write-Host ("built {0} ({1:N0} bytes)" -f $exe.Name, $exe.Length) -ForegroundColor Green

    # Prove the resources really landed, rather than assuming they did: a bad
    # resource tree is accepted by the linker without complaint and only shows up
    # as a blank icon in Explorer.
    $vi = [System.Diagnostics.FileVersionInfo]::GetVersionInfo($exe.FullName)
    if (-not $vi.ProductName) {
        throw "the executable has no version information; the resource tree is wrong"
    }
    Write-Host ("version: {0} {1}" -f $vi.ProductName, $vi.FileVersion)
    Add-Type -AssemblyName System.Drawing
    $icon = [System.Drawing.Icon]::ExtractAssociatedIcon($exe.FullName)
    if (-not $icon) { throw "the executable has no icon" }
    Write-Host ("icon: {0}x{1}" -f $icon.Width, $icon.Height)

    if (-not $SkipTests) {
        Step "running the headless tests"
        $out = Join-Path $root "_selftest"
        New-Item -ItemType Directory -Force -Path $out | Out-Null
        & $exe.FullName --selftest "--out=$out"
        if ($LASTEXITCODE -ne 0) { throw "the selftest reported failures (see $out)" }
    }

    if ($LiveTest) {
        Step "running the screen tests"
        Write-Host "the line will come down over whatever is on screen for a moment" -ForegroundColor Yellow
        $out = Join-Path $root "_livetest"
        New-Item -ItemType Directory -Force -Path $out | Out-Null
        & $exe.FullName --livetest "--out=$out"
        if ($LASTEXITCODE -ne 0) { throw "the livetest reported failures (see $out)" }
    }

    Step "done"
} finally {
    Pop-Location
}