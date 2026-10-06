# run.ps1 - Build and run CloudPrism the way an end user would: one process,
# one port, the UI embedded in the binary, browser opens automatically.
#
# Usage (run from anywhere - paths come from $PSScriptRoot):
#   powershell -ExecutionPolicy Bypass -File scripts\run.ps1
#   powershell -ExecutionPolicy Bypass -File scripts\run.ps1 -RebuildFrontend
#   powershell -ExecutionPolicy Bypass -File scripts\run.ps1 -NoBrowser
#   powershell -ExecutionPolicy Bypass -File scripts\run.ps1 -Stop
#
# How this differs from the other two scripts:
#   dev.ps1   - Vite dev server (:5173) + backend, HMR, coloured log tail.
#               Use while editing frontend code. Needs npm and a second port.
#   build.ps1 - assemble a full portable package under releases\<stamp>\.
#               Use to ship. Produces a directory you copy elsewhere.
#   run.ps1   - this one. Compile and launch in place so you can look at the
#               result. No Vite, no package assembly, one server on one port.
#
# Data goes to WindowsGo\.devdata (gitignored), the same directory dev.ps1 uses,
# so dev.ps1 and run.ps1 share one data home: settings and recent vaults carry
# over between them. It never touches the data of a package in releases\ or a
# real installation. (Not WindowsGo\build\bin\data: the exe lives in build\bin,
# and the default data dir would follow the exe path.)
param(
    [switch]$NoBrowser,
    [switch]$RebuildFrontend,
    [switch]$Stop
)
$ErrorActionPreference = "Stop"

$repo    = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$goDir   = Join-Path $repo "WindowsGo"
$feDir   = Join-Path $goDir "frontend"
$binDir  = Join-Path $goDir "build\bin"
$exeName = "CloudPrismRun.exe"
$exe     = Join-Path $binDir $exeName

# Repo-local Go caches, same as build.ps1: keeps the global cache untouched and
# the tree self-contained. Both are already in .gitignore.
$env:GOCACHE     = Join-Path $repo ".gocache"
$env:GOMODCACHE  = Join-Path $repo ".gomodcache"
$env:CGO_ENABLED = "0"

# Dev data dir, shared with dev.ps1: a run must not read or write the data/
# of a packaged build, and must not drift with the exe location.
$env:CLOUDPRISM_DATA_DIR = Join-Path $goDir ".devdata"

function Fail([string]$msg) {
    Write-Host "[run] FAILED: $msg" -ForegroundColor Red
    exit 1
}
function Step([string]$msg) { Write-Host "[run] $msg" -ForegroundColor Cyan }

# --- Stop mode: kill a previously started run and exit. ---
# Removes the need to hunt for the process by hand when a run is left behind
# (the app lives in the tray, so closing the browser tab does NOT stop it).
if ($Stop) {
    $procs = @(Get-Process -Name ([System.IO.Path]::GetFileNameWithoutExtension($exeName)) -ErrorAction SilentlyContinue)
    if ($procs.Count -eq 0) {
        Write-Host "[run] nothing to stop." -ForegroundColor DarkGray
        exit 0
    }
    # Only kill processes launched from THIS repo: another installation may
    # legitimately be running from a different folder.
    $killed = 0
    foreach ($p in $procs) {
        $path = $null
        try { $path = $p.Path } catch { }
        if ($path -and $path.StartsWith($repo, [System.StringComparison]::OrdinalIgnoreCase)) {
            Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
            Write-Host "[run] stopped pid $($p.Id)" -ForegroundColor DarkGray
            $killed++
        } else {
            Write-Host "[run] leaving pid $($p.Id) alone (not started from this repo)" -ForegroundColor DarkYellow
        }
    }
    if ($killed -eq 0) { Write-Host "[run] nothing from this repo was running." -ForegroundColor DarkGray }
    exit 0
}

# --- 1. Toolchain ---
$env:Path = "C:\Program Files\Go\bin;$env:USERPROFILE\go\bin;" + $env:Path
go version | Out-Null
if ($LASTEXITCODE -ne 0) { Fail "go is not available (install Go 1.27+)" }

# --- 2. Frontend assets ---
# main.go embeds frontend/dist via //go:embed. That folder is NOT in git, so a
# fresh clone has none and the Go compiler would fail with a cryptic
# "pattern all:frontend/dist: no matching files found". Build it when missing,
# or when -RebuildFrontend asks for it.
$distIndex = Join-Path $feDir "dist\index.html"
$needFrontend = $RebuildFrontend -or (-not (Test-Path $distIndex))
if ($needFrontend) {
    if (-not (Get-Command npm -ErrorAction SilentlyContinue)) {
        Fail "npm is required to build frontend\dist (not in git). Install Node >= 20, or point -RebuildFrontend off after building once"
    }
    Step "building frontend (frontend\ -> dist\)"
    Push-Location $feDir
    try {
        if (-not (Test-Path (Join-Path $feDir "node_modules"))) {
            Write-Host "[run] first run: npm ci ..." -ForegroundColor Cyan
            npm ci --no-audit --no-fund
            if ($LASTEXITCODE -ne 0) { Fail "npm ci failed (exit $LASTEXITCODE)" }
        }
        npm run build
        if ($LASTEXITCODE -ne 0) { Fail "npm run build failed (exit $LASTEXITCODE)" }
    } finally { Pop-Location }
} else {
    Write-Host "[run] reusing existing frontend\dist (-RebuildFrontend to rebuild)" -ForegroundColor DarkGray
}

# --- 3. Compile ---
# Same flags as the shipped build (-H windowsgui: tray app, no console window;
# -s -w: strip symbols), so what you run matches what users get.
Step "go build -> build\bin\$exeName"
New-Item -ItemType Directory -Force -Path $binDir | Out-Null
Push-Location $goDir
try {
    go build -ldflags "-s -w -H windowsgui" -o $exe .
    if ($LASTEXITCODE -ne 0) { Fail "go build failed (exit $LASTEXITCODE)" }
} finally { Pop-Location }
if (-not (Test-Path $exe)) { Fail "build output not found: $exe" }

# --- 4. Make sure nothing from THIS repo is already running ---
# The app holds a single-instance lock on its data directory. A leftover tray
# process would make this launch exit immediately (correctly, but confusingly),
# so clear it first and say so.
$stale = @(Get-Process -Name ([System.IO.Path]::GetFileNameWithoutExtension($exeName)) -ErrorAction SilentlyContinue)
foreach ($p in $stale) {
    $path = $null
    try { $path = $p.Path } catch { }
    if ($path -and $path.StartsWith($repo, [System.StringComparison]::OrdinalIgnoreCase)) {
        Write-Host "[run] stopping previous instance (pid $($p.Id))" -ForegroundColor DarkYellow
        Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
    }
}
if ($stale.Count -gt 0) { Start-Sleep -Milliseconds 700 }

# --- 5. Launch ---
# Do not set CP_NO_BROWSER here: opening the browser is the point, and the app
# reads the port it actually bound (it may fall forward if configured ports are
# busy), so letting it decide is more reliable than guessing the URL ourselves.
if ($NoBrowser) { $env:CP_NO_BROWSER = "1" } else { Remove-Item Env:\CP_NO_BROWSER -ErrorAction SilentlyContinue }

# Prefer the configured port for the console message; fall back to the default.
$cfgPath = Join-Path $env:CLOUDPRISM_DATA_DIR "config.json"
$shownPort = 7840
if (Test-Path $cfgPath) {
    try {
        $cfg = Get-Content $cfgPath -Raw | ConvertFrom-Json
        if ($cfg.port) { $shownPort = [int]$cfg.port }
    } catch { }
}

Step "starting (http://127.0.0.1:$shownPort, Ctrl+C to stop)"
Write-Host "[run] data dir : $env:CLOUDPRISM_DATA_DIR" -ForegroundColor DarkGray
Write-Host "[run] note     : the app lives in the tray; Ctrl+C or -Stop ends it" -ForegroundColor DarkGray

# NOTE: do NOT use the call operator (& $exe) here. This binary is built with
# -H windowsgui, i.e. a GUI-subsystem executable, and PowerShell does not wait
# for those -- "& $exe" returns in ~90ms while the app keeps running. A cleanup
# step after that would then kill the app we just started (observed: the UI
# logged "Web ready" and vanished seconds later). Start-Process -Wait blocks
# until the process actually exits, for console and GUI binaries alike.
try {
    $proc = Start-Process -FilePath $exe -PassThru -Wait
    $code = $proc.ExitCode
} finally {
    # Ctrl+C lands here while the app is still up. Exit is not enough on its
    # own: the app can also be left running intentionally (tray), so only sweep
    # processes started from THIS repo, and only if one is still alive.
    foreach ($p in @(Get-Process -Name ([System.IO.Path]::GetFileNameWithoutExtension($exeName)) -ErrorAction SilentlyContinue)) {
        $path = $null
        try { $path = $p.Path } catch { }
        if ($path -and $path.StartsWith($repo, [System.StringComparison]::OrdinalIgnoreCase)) {
            Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
        }
    }
}
Write-Host "[run] exited." -ForegroundColor DarkGray
