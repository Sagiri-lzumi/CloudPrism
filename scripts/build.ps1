# build.ps1 - Compile CloudPrism into an executable and assemble a deployable folder.
#
# Usage (run from the repository root - it locates everything via $PSScriptRoot):
#   powershell -ExecutionPolicy Bypass -File scripts\build.ps1
#   powershell -ExecutionPolicy Bypass -File scripts\build.ps1 -Clean   (keep only the new package)
#   powershell -ExecutionPolicy Bypass -File scripts\build.ps1 -SkipFrontend
#   powershell -ExecutionPolicy Bypass -File scripts\build.ps1 -SkipNpmCi
#
# Output: releases\<yyyyMMdd_HHmmss>\
#   CloudPrismGo.exe      main binary (-H windowsgui: no console window, tray resident)
#   assets\icon.ico       application icon
#   assets\*.md           user-facing docs (self-test guide / Baidu credential guide)
#   data\config.json      startup config (listen port / host / port range) - edit + restart
#   data\tmp\             runtime data placeholder (vault/cache/logs created on demand)
#   README-portable.txt   deployment notes (shipped as README-<cn>.txt)
#   build-info.txt        build fingerprint: commit + frontend asset names
#                         (shipped as <cn>.txt; both names built from char codes)
param(
    [switch]$Clean,
    [switch]$SkipNpmCi,
    [switch]$SkipFrontend
)
$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

# --- Paths are all derived from $PSScriptRoot: no absolute paths, repo can move. ---
$repo  = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path   # repository root
$goDir = Join-Path $repo "WindowsGo"                          # Go module root
$feDir = Join-Path $goDir "frontend"

# Keep Go build caches inside the repo (.gitignore already excludes them) so the
# global user cache is untouched and the tree stays self-contained.
$env:GOCACHE     = Join-Path $repo ".gocache"
$env:GOMODCACHE  = Join-Path $repo ".gomodcache"
$env:CGO_ENABLED = "0"   # pure Go build, no cgo dependencies

function Fail([string]$msg) {
    Write-Host "[build] FAILED: $msg" -ForegroundColor Red
    # Drop this run's (incomplete) output dir so releases/ only ever lists
    # packages that actually finished. $outDir may not exist if we failed before
    # creating it, and may be the only thing here -- never touch other entries.
    if ($outDir -and (Test-Path -LiteralPath $outDir)) {
        try { Remove-Tree $outDir } catch { }
    }
    exit 1
}
function Step([string]$msg) { Write-Host "[build] $msg" -ForegroundColor Cyan }

# Remove-Tree: .NET Directory.Delete goes straight to Win32 and is 1-2 orders of
# magnitude faster than Remove-Item -Recurse on deep trees with many small files.
# On failure it retries instead of falling back to Remove-Item: some environments
# route Remove-Item through the Recycle Bin, which pops a modal dialog on a locked
# file and blocks forever.
function Remove-Tree([string]$path) {
    if (-not (Test-Path -LiteralPath $path)) { return }
    $item = Get-Item -LiteralPath $path -Force
    if ($item.PSIsContainer) {
        Get-ChildItem -LiteralPath $item.FullName -Recurse -Force -ErrorAction SilentlyContinue |
            ForEach-Object { try { $_.Attributes = [System.IO.FileAttributes]::Normal } catch { } }
    } else {
        try { [System.IO.File]::SetAttributes($item.FullName, [System.IO.FileAttributes]::Normal) } catch { }
    }
    $lastErr = $null
    for ($i = 0; $i -lt 3; $i++) {
        try {
            if ($item.PSIsContainer) { [System.IO.Directory]::Delete($item.FullName, $true) }
            else { [System.IO.File]::Delete($item.FullName) }
        } catch { $lastErr = $_ }
        if (-not (Test-Path -LiteralPath $path)) { return }
        Start-Sleep -Milliseconds 400
    }
    Fail "cannot delete old artifact (likely locked by another process): $path - $($lastErr.Exception.Message)"
}

# --- Output dir: releases\<yyyyMMdd_HHmmss> (standalone folder at the repo root) ---
$stamp   = Get-Date -Format "yyyyMMdd_HHmmss"
$relRoot = Join-Path $repo "releases"
$outDir  = Join-Path $relRoot $stamp
New-Item -ItemType Directory -Force -Path $outDir | Out-Null

# Every text file we ship is written with a BOM so Notepad (and PS 5.1 reading
# it back) renders the Chinese names/content correctly.
$utf8Bom = New-Object System.Text.UTF8Encoding($true)

# The two Chinese output file names are built from char codes rather than written
# literally. This file must stay pure ASCII: Windows PowerShell 5.1 reads .ps1
# sources using the ANSI code page unless the file has a UTF-8 BOM, and any tool
# that rewrites the file without preserving the BOM would turn a literal Chinese
# name into mojibake and break the build with "Illegal characters in path".
#   build-info.txt  /  README-portable.txt
$namePortable  = "README-" + ([char]0x4FBF) + ([char]0x643A) + ([char]0x7248) + ".txt"
$nameBuildInfo = ([char]0x7248) + ([char]0x672C) + ([char]0x4FE1) + ([char]0x606F) + ".txt"

# --- 0. Toolchain (a fresh shell does not have Go on PATH) ---
$env:Path = "C:\Program Files\Go\bin;$env:USERPROFILE\go\bin;" + $env:Path
go version | Out-Null
if ($LASTEXITCODE -ne 0) { Fail "go is not available (install Go 1.27+)" }

# -Clean is applied AFTER a successful build (see the end of this script), not
# before. Deleting old artifacts first would mean any build failure destroys the
# last known-good package while producing no replacement -- the user is left with
# nothing. Deleting last gives the identical end state on success and keeps the
# previous package intact on failure.
if ($Clean) {
    Step "[0/4] -Clean requested: old artifacts will be removed after this build succeeds"
}

# --- 1. Frontend build (Vue source -> frontend\dist, embedded into the exe) ---
Step "[1/4] frontend build (frontend\ -> dist\)"
if ($SkipFrontend) {
    $distHtml = Join-Path $feDir "dist\index.html"
    if (-not (Test-Path $distHtml)) {
        Fail "-SkipFrontend was given but frontend\dist\index.html does not exist. frontend\dist is not in git, so a fresh clone has no prebuilt UI; run this script once WITHOUT -SkipFrontend to build it"
    }
    Write-Host "[build] skipping all npm steps (-SkipFrontend, reusing frontend\dist)" -ForegroundColor Yellow
} else {
    if (-not (Get-Command npm -ErrorAction SilentlyContinue)) {
        Fail "npm unavailable and -SkipFrontend not set: the frontend must be built first (cd WindowsGo\frontend; npm ci; npm run build)"
    }
    Push-Location $feDir
    try {
        if ($SkipNpmCi) {
            if (-not (Test-Path (Join-Path $feDir "node_modules"))) {
                Fail "-SkipNpmCi was given but frontend\node_modules is missing; run once without that switch"
            }
            Write-Host "[build] skipping npm ci (-SkipNpmCi, reusing node_modules)" -ForegroundColor Yellow
        } elseif (-not (Test-Path (Join-Path $feDir "node_modules"))) {
            Write-Host "[build] first run: npm ci ..." -ForegroundColor Cyan
            npm ci --no-audit --no-fund
            if ($LASTEXITCODE -ne 0) { Fail "npm ci failed (exit $LASTEXITCODE)" }
        }
        npm run build
        if ($LASTEXITCODE -ne 0) { Fail "npm run build failed (exit $LASTEXITCODE)" }
    } finally { Pop-Location }
}

# --- 2. Go build (-H windowsgui: no console window; -s -w: strip symbols) ---
$exeName = "CloudPrismGo.exe"
Step "[2/4] go build -> $exeName"

# main.go embeds frontend/dist via //go:embed all:frontend/dist. That directory
# is gitignored, so a fresh clone reaches this point with no UI built and the Go
# compiler fails with a cryptic "pattern all:frontend/dist: no matching files".
# Check up front and say what to do instead.
$distIndex = Join-Path $feDir "dist\index.html"
if (-not (Test-Path $distIndex)) {
    Fail "frontend\dist is missing (it is gitignored and not in the repo), but main.go embeds it. Re-run without -SkipFrontend so the frontend gets built first"
}
Push-Location $goDir
try {
    go build -ldflags "-s -w -H windowsgui" -o (Join-Path $outDir $exeName) .
    if ($LASTEXITCODE -ne 0) { Fail "go build failed (exit $LASTEXITCODE)" }
} finally { Pop-Location }

$exe = Join-Path $outDir $exeName
if (-not (Test-Path $exe)) { Fail "build output not found: $exe" }

# --- Self-check: the exe must embed this run index.html css/js asset names. ---
# Guards against the "dist rebuilt but package shipped an old exe" mismatch.
$html = Get-Content (Join-Path $feDir "dist\index.html") -Raw
$assetHashes = [regex]::Matches($html, "assets/(index-[\w-]+\.(?:css|js))") | ForEach-Object { $_.Groups[1].Value }
if ($assetHashes) {
    $exeText = [Text.Encoding]::UTF8.GetString([IO.File]::ReadAllBytes($exe))
    foreach ($h in $assetHashes) {
        if (-not $exeText.Contains($h)) { Fail "frontend asset $h is not embedded in the exe (dist and exe differ); rebuild" }
    }
    Write-Host "[build] self-check OK: exe embeds frontend assets $($assetHashes -join ' + ')" -ForegroundColor Green
} else {
    Write-Host "[build] note: no asset names parsed from dist/index.html, skipping embed self-check" -ForegroundColor Yellow
}

# --- 3. Assemble the deployable folder (pristine: no user data included) ---
Step "[3/4] assembling package resources"
$assetsDir = Join-Path $outDir "assets"
New-Item -ItemType Directory -Force -Path $assetsDir | Out-Null
$icon = Join-Path $goDir "build\windows\icon.ico"
if (Test-Path $icon) { Copy-Item $icon (Join-Path $assetsDir "icon.ico") }

# Only user-facing docs ship; internal engineering docs stay in the repo.
$docs = @("self_test_guide.md", "baidu_guide.md")
foreach ($doc in $docs) {
    $docSrc = Join-Path $goDir ("docs\" + $doc)
    if (Test-Path $docSrc) { Copy-Item $docSrc (Join-Path $assetsDir $doc) }
    else { Write-Host "[build] note: docs\$doc not found, skipping" -ForegroundColor Yellow }
}

# Runtime data placeholder (vault/cache/logs are created on demand by the app).
$dataDir = Join-Path $outDir "data"
New-Item -ItemType Directory -Force -Path (Join-Path $dataDir "tmp") | Out-Null

# Startup config: port / host / port_range. This is the one file an operator is
# expected to edit before deploying. Values here MUST stay in sync with
# pkg/config Default() - the app falls back to those defaults on any bad value.
$cfgJson = @'
{
  "port": 7840,
  "host": "127.0.0.1",
  "port_range": 10
}
'@
[System.IO.File]::WriteAllText((Join-Path $dataDir "config.json"), $cfgJson, $utf8Bom)
Write-Host "[build] wrote data\config.json (port 7840, range 10)" -ForegroundColor DarkGray

# --- 4. Build info + documentation ---
Step "[4/4] writing build info and docs"

# git revision: fall back to a placeholder, never abort the build over it.
$rev = "(unknown)"; $dirty = "(unknown)"
try {
    $revRaw = git -C $repo rev-parse --short HEAD 2>$null
    if ($LASTEXITCODE -eq 0 -and $revRaw) {
        $rev = "$revRaw".Trim()
        $dirty = if (git -C $repo status --porcelain 2>$null) { "dirty" } else { "clean" }
    }
} catch { Write-Host "[build] note: could not read git revision, recording (unknown)" -ForegroundColor Yellow }

$assetLine = if ($assetHashes) { $assetHashes -join ' + ' } else { '(not parsed)' }
$verInfo = @(
    "CloudPrismGo build info",
    "=====================",
    "built at : $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')",
    "commit   : $rev ($dirty)",
    "frontend : $assetLine",
    "dist dir : WindowsGo/frontend/dist/assets/",
    "",
    "Troubleshooting:",
    "1. Open WindowsGo/frontend/dist/assets/ and compare the file names above.",
    "2. If the UI will not load, check data/logs/cloudprism.log next to the exe."
)
[System.IO.File]::WriteAllText((Join-Path $outDir $nameBuildInfo), ($verInfo -join "`r`n"), $utf8Bom)

$readme = @"
CloudPrism portable (Go build) - deployment notes
=================================================

What this is
    An end-to-end encrypted cloud drive client: files are encrypted locally
    before being uploaded to a local directory / WebDAV / Baidu Netdisk.
    The key never leaves this machine.

Requirements
    Windows 10 / 11 (64-bit). The UI opens in your browser (no WebView2).
    The default browser is opened on launch; if not, visit
    http://127.0.0.1:7840 (the tray icon reopens the UI at any time).

Layout
    CloudPrismGo.exe          main binary (single file, no install, tray resident)
    data\config.json          startup config: listen port / host / port range
    assets\icon.ico           application icon
    assets\baidu_guide.md     how to obtain Baidu Netdisk API credentials
    assets\self_test_guide.md encryption smoke test (new vault -> upload -> verify -> resume)
    data\                     runtime state: logs, caches, temp files.
                              Safe to delete the whole folder; cloud vault data is unaffected.
    data\logs\                logs (cloudprism.log, for troubleshooting)

Configuration (data\config.json)
    Edit the file and restart the app. A missing file or an invalid value
    falls back to the built-in default; every fallback is reported in
    data\logs\cloudprism.log.

        port        7840         first port to try
        host        "127.0.0.1"  bind address (LAN access is a UI setting)
        port_range  10           try port .. port+port_range-1

    If every port in the range is busy the app will not start: widen
    port_range or free a port. config.json only picks the bind address;
    whether non-loopback access needs a token is still decided by the
    LAN switch in the UI (fail-closed), so this file cannot bypass auth.

Data and privacy
    Vault blobs live in the cloud (or in the folder you picked for a local backend).
    The local data\ folder holds only settings, thumbnail cache and logs. Baidu
    credentials are DPAPI-encrypted and readable only on this machine.
    Uninstall = delete this folder; keep the cloud files if you want the data.

Updating
    Just replace CloudPrismGo.exe (data\ and cloud data are preserved).
"@
[System.IO.File]::WriteAllText((Join-Path $outDir $namePortable), $readme, $utf8Bom)

# --- Deferred -Clean: the build succeeded, so old packages can go now. ---
# Reaching this point means the package is fully assembled (exe self-check
# passed, assets copied, docs written). Only now is it safe to drop history.
if ($Clean -and (Test-Path $relRoot)) {
    Step "[5/5] cleaning old artifacts (-Clean)"
    # Only directories are packages, and only those are removed. A stray file
    # (someone dropping notes.txt or a zip into releases/) is left alone:
    # -Clean is about build artifacts, and silently deleting a file the build
    # never created would be over-reach.
    $removed = 0
    Get-ChildItem $relRoot -Directory -Force | Where-Object { $_.Name -ne $stamp } | ForEach-Object {
        Write-Host "[build] removing $($_.Name)" -ForegroundColor DarkGray
        Remove-Tree $_.FullName
        $removed++
    }
    Write-Host "[build] removed $removed old package(s), kept $stamp" -ForegroundColor Green
}

# --- Summary ---
$size  = (Get-ChildItem $outDir -Recurse -File | Measure-Object Length -Sum).Sum
$exeMB = [math]::Round((Get-Item $exe).Length / 1MB, 1)
Write-Host ""
Write-Host "[build] Done: $outDir" -ForegroundColor Green
Write-Host "  exe: $exeName ($exeMB MB)" -ForegroundColor DarkGray
Write-Host "  total: $([math]::Round($size / 1MB, 1)) MB" -ForegroundColor DarkGray
Write-Host "  contents: $exeName / assets\ / data\{config.json,tmp} / $namePortable / $nameBuildInfo" -ForegroundColor DarkGray
Write-Host "BUILD OK" -ForegroundColor Green
