# CloudPrism dev script: build the backend, stream backend/frontend logs side by side.
#   [backend]  green  - Go HTTP server (stdout/stderr + .devdata/logs/cloudprism.log)
#   [frontend] yellow - Vite dev server (HMR)
# Backend changes: rerun this script. Frontend changes: Vite HMR. Ctrl+C stops all.
$ErrorActionPreference = 'Stop'

$ROOT          = Split-Path $PSScriptRoot -Parent
$BACKEND_DIR   = Join-Path $ROOT 'WindowsGo'
$FRONTEND_DIR  = Join-Path $BACKEND_DIR 'frontend'
$BACKEND_EXE   = 'CloudPrismGo-dev.exe'
$BACKEND_PORT  = 7840
$FRONTEND_PORT = 5173

# Log tags (single definition, reused everywhere).
$TAG_DEV = '[dev]'
$TAG_BE  = '[backend]'
$TAG_FE  = '[frontend]'

# Stop-DevPortListeners: free the dev ports, but ONLY kill processes whose command
# line references this repo - never touch an unrelated app that happens to share a port.
function Stop-DevPortListeners {
    foreach ($port in @($BACKEND_PORT, $FRONTEND_PORT)) {
        try {
            $procIds = Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction Stop |
                Select-Object -ExpandProperty OwningProcess -Unique
        } catch { continue }
        foreach ($procId in $procIds) {
            $cmdLine = (Get-CimInstance Win32_Process -Filter "ProcessId=$procId" -ErrorAction SilentlyContinue).CommandLine
            if ($cmdLine -and $cmdLine.Contains($ROOT)) {
                Stop-Process -Id $procId -Force -ErrorAction SilentlyContinue
            } else {
                Write-Host "$TAG_DEV warn: port $port held by unrelated process (pid $procId), left running." -ForegroundColor DarkYellow
            }
        }
    }
}

# ---- [1/4] 结束旧后端 ----
Write-Host "$TAG_DEV [1/4] stopping old backend..." -ForegroundColor Cyan
cmd /c "taskkill /F /IM $BACKEND_EXE >nul 2>&1"
# also free the dev ports: a crashed previous run may leave vite/node holding them
Stop-DevPortListeners

# ---- [2/4] 编译后端 ----
Write-Host "$TAG_DEV [2/4] building backend..." -ForegroundColor Cyan
Push-Location $BACKEND_DIR
try {
    New-Item -ItemType Directory -Force 'build\bin' | Out-Null
    $env:CGO_ENABLED = '0'
    go build -o "build\bin\$BACKEND_EXE" .
    if ($LASTEXITCODE -ne 0) { Write-Host "$TAG_DEV backend build failed." -ForegroundColor Red; exit 1 }
} finally { Pop-Location }

# ---- 日志文件准备（后端启动前建好，避免丢早期日志） ----
$env:CLOUDPRISM_DATA_DIR = Join-Path $BACKEND_DIR '.devdata'   # 开发态数据目录固定，不随 exe 路径漂移
$logDir = Join-Path $env:CLOUDPRISM_DATA_DIR 'logs'
New-Item -ItemType Directory -Force $logDir | Out-Null
$beLog = Join-Path $logDir 'cloudprism.log'
$beOut = Join-Path $logDir 'dev-be-out.log'
$beErr = Join-Path $logDir 'dev-be-err.log'
$feOut = Join-Path $logDir 'dev-fe-out.log'
$feErr = Join-Path $logDir 'dev-fe-err.log'
# Truncate dev logs; a leftover process may still hold a lock - tolerate that and
# tail such files from their current end instead of dying (ErrorActionPreference=Stop).
$cleared = @{}
foreach ($f in @($beOut, $beErr, $feOut, $feErr)) {
    try { '' | Set-Content -Encoding ascii -LiteralPath $f; $cleared[$f] = $true }
    catch { $cleared[$f] = $false; Write-Host "$TAG_DEV warn: log file locked, tailing from end: $f" -ForegroundColor DarkYellow }
}
if (-not (Test-Path $beLog)) { New-Item -ItemType File $beLog | Out-Null }
$beLogLen = (Get-Item $beLog).Length   # 只尾随新日志，不刷历史

# ---- [3/4] 启动后端 ----
# 端口可配（data/config.json）且被占用时顺延，BACKEND_PORT 只是期望起点，
# 实际端口必须探测：写死 7840 会在顺延后把 vite 代理打到错误的实例上
#（比如 run.ps1 跑着的那个）。
Write-Host "$TAG_DEV [3/4] starting backend (config port, falls forward from $BACKEND_PORT)..." -ForegroundColor Cyan
$env:CP_NO_BROWSER = '1'
$env:CP_DEV_ORIGIN = "http://127.0.0.1:$FRONTEND_PORT"
$beCmd = "`"$BACKEND_DIR\build\bin\$BACKEND_EXE`" > `"$beOut`" 2> `"$beErr`""
$bePsi = New-Object System.Diagnostics.ProcessStartInfo
$bePsi.FileName = 'cmd.exe'
$bePsi.Arguments = '/d /c "' + $beCmd + '"'
$bePsi.WorkingDirectory = $BACKEND_DIR
$bePsi.UseShellExecute = $false
$bePsi.CreateNoWindow = $true
$beProc = [System.Diagnostics.Process]::Start($bePsi)

# 探测后端实际端口：候选 = config.json 的 port..port+range-1 与默认段
# 7840..7849 的并集。用 POST /api/app/ping 辨认本程序（/api/* 只收 POST），
# 而不是裸 TCP——只占坑的不是本程序的端口不能算数。
function Find-BackendPort {
    $candidates = @($BACKEND_PORT..($BACKEND_PORT + 9))
    $cfgPath = Join-Path $env:CLOUDPRISM_DATA_DIR 'config.json'
    if (Test-Path $cfgPath) {
        try {
            $cfg = Get-Content $cfgPath -Raw | ConvertFrom-Json
            if ($cfg.port -and $cfg.port -ge 1 -and $cfg.port -le 65535) {
                $range = if ($cfg.port_range -and $cfg.port_range -ge 1) { [int]$cfg.port_range } else { 10 }
                $candidates += ([int]$cfg.port)..([int]$cfg.port + $range - 1)
            }
        } catch { }
    }
    $deadline = (Get-Date).AddSeconds(15)
    while ((Get-Date) -lt $deadline) {
        foreach ($p in ($candidates | Select-Object -Unique)) {
            try {
                $resp = Invoke-WebRequest -Uri "http://127.0.0.1:$p/api/app/ping" -Method Post `
                    -Body '{"Token":"probe"}' -UseBasicParsing -TimeoutSec 1
                if ($resp.Content -match 'pong:probe') { return $p }
            } catch { }
        }
        Start-Sleep -Milliseconds 400
    }
    return 0
}
$BACKEND_ACTUAL_PORT = Find-BackendPort
if ($BACKEND_ACTUAL_PORT -eq 0) {
    Write-Host "$TAG_DEV backend did not come up on any candidate port; see $beErr" -ForegroundColor Red
    cmd /c "taskkill /F /IM $BACKEND_EXE >nul 2>&1"
    exit 1
}
Write-Host "$TAG_DEV backend listening on 127.0.0.1:$BACKEND_ACTUAL_PORT" -ForegroundColor Cyan
# vite.config.ts 读 CP_BACKEND_ORIGIN 决定代理目标；在启动前端前注入，
# ProcessStartInfo(UseShellExecute=false) 默认继承本进程环境。
$env:CP_BACKEND_ORIGIN = "http://127.0.0.1:$BACKEND_ACTUAL_PORT"

# ---- [4/4] 前端依赖 + 启动 Vite ----
Push-Location $FRONTEND_DIR
try {
    if (-not (Test-Path 'node_modules')) {
        Write-Host "$TAG_DEV [4/4] first run: npm install..." -ForegroundColor Cyan
        npm install
        if ($LASTEXITCODE -ne 0) {
            Write-Host "$TAG_DEV npm install failed." -ForegroundColor Red
            cmd /c "taskkill /F /IM $BACKEND_EXE >nul 2>&1"   # don't orphan the running backend
            exit 1
        }
    }
} finally { Pop-Location }
Write-Host "$TAG_DEV [4/4] starting frontend (http://127.0.0.1:$FRONTEND_PORT)..." -ForegroundColor Cyan
$feCmd = "npm run dev -- --host 127.0.0.1 --port $FRONTEND_PORT --strictPort > `"$feOut`" 2> `"$feErr`""
$fePsi = New-Object System.Diagnostics.ProcessStartInfo
$fePsi.FileName = 'cmd.exe'
$fePsi.Arguments = '/d /c "' + $feCmd + '"'
$fePsi.WorkingDirectory = $FRONTEND_DIR
$fePsi.UseShellExecute = $false
$fePsi.CreateNoWindow = $true
$feProc = [System.Diagnostics.Process]::Start($fePsi)

# ---- 活性探测：以端口监听为准（进程句柄在部分环境下不可靠） ----
function Test-PortListen([int]$port) {
    # 用裸 TCP 连接探测：Get-NetTCPConnection 在部分受限环境里查不到本机监听
    try {
        $c = New-Object System.Net.Sockets.TcpClient
        $iar = $c.BeginConnect('127.0.0.1', $port, $null, $null)
        $ok = $iar.AsyncWaitHandle.WaitOne(400)
        if ($ok) { $c.EndConnect($iar) }
        $c.Close()
        return $ok
    } catch { return $false }
}

# ---- 尾随输出：逐源增量读取，加前缀 + 颜色 ----
# Start position per file: 0 when truncated just now, else current end (skip stale content).
function Get-StartPos($path) { if ($cleared[$path]) { 0L } else { (Get-Item $path).Length } }
$script:opened = $false
$sources = @(
    @{ Path = $beOut; Tag = $TAG_BE; Color = 'Green';      Pos = (Get-StartPos $beOut); Leftover = '' },
    @{ Path = $beErr; Tag = $TAG_BE; Color = 'DarkGreen';  Pos = (Get-StartPos $beErr); Leftover = '' },
    @{ Path = $beLog; Tag = $TAG_BE; Color = 'Green';      Pos = $beLogLen;             Leftover = '' },
    @{ Path = $feOut; Tag = $TAG_FE; Color = 'Yellow';     Pos = (Get-StartPos $feOut); Leftover = '' },
    @{ Path = $feErr; Tag = $TAG_FE; Color = 'DarkYellow'; Pos = (Get-StartPos $feErr); Leftover = '' }
)

function Read-NewLines([hashtable]$s) {
    if (-not (Test-Path $s.Path)) { return }
    $read = 0; $buf = $null
    try {
        $fs = [System.IO.File]::Open($s.Path, 'Open', 'Read', 'ReadWrite')
        try {
            if ($fs.Length -lt $s.Pos) { $s.Pos = 0 }   # 日志轮转后文件变小，从头读
            $len = $fs.Length - $s.Pos
            if ($len -le 0) { return }
            $buf = New-Object byte[] $len
            $fs.Position = $s.Pos
            $read = $fs.Read($buf, 0, $len)
            $s.Pos += $read
        } finally { $fs.Dispose() }
    } catch { return }
    if ($read -le 0) { return }
    $text = $s.Leftover + [System.Text.Encoding]::UTF8.GetString($buf, 0, $read)
    $nl = $text.EndsWith([char]10)
    $lines = [regex]::Split($text, '\r?\n')
    if ($nl) {
        $s.Leftover = ''
    } else {
        if ($lines.Count -eq 1) { $s.Leftover = $lines[0]; $lines = @() }
        else { $s.Leftover = $lines[-1]; $lines = $lines[0..($lines.Count - 2)] }
    }
    foreach ($l in $lines) {
        $l = ($l -replace "\x1b\[[0-9;]*[A-Za-z]", '').TrimEnd()
        if ($l -eq '') { continue }
        # npm noise: legacy .npmrc keys warning + run banner, pure clutter
        if ($l -match '^npm (warn|notice)') { continue }
        Write-Host "$($s.Tag)" -NoNewline -ForegroundColor $s.Color
        Write-Host " $l"
        if (-not $script:opened -and $l -match 'Local:') {
            $script:opened = $true
            Start-Process "http://127.0.0.1:$FRONTEND_PORT"
        }
    }
}

Write-Host ''
Write-Host "$TAG_DEV ready: green=$TAG_BE  yellow=$TAG_FE  Ctrl+C to stop all." -ForegroundColor Cyan
$beWarned = $false; $feWarned = $false
$started = Get-Date
try {
    while ($true) {
        foreach ($s in $sources) { Read-NewLines $s }
        $elapsed = ((Get-Date) - $started).TotalSeconds
        if (-not $beWarned -and $elapsed -gt 10 -and -not (Test-PortListen $BACKEND_ACTUAL_PORT)) {
            $beWarned = $true
            Write-Host "$TAG_BE port not listening, process may have exited." -ForegroundColor Red
        }
        if (-not $feWarned -and $elapsed -gt 15 -and -not (Test-PortListen $FRONTEND_PORT)) {
            $feWarned = $true
            Write-Host "$TAG_FE port not listening, process may have exited." -ForegroundColor Red
        }
        # either side exits -> stop everything (finally block kills the rest)
        if ($beWarned -or $feWarned) {
            foreach ($s in $sources) { Read-NewLines $s }
            Write-Host "$TAG_DEV a process exited, stopping the rest..." -ForegroundColor Red
            break
        }
        Start-Sleep -Milliseconds 300
    }
} finally {
    Write-Host "$TAG_DEV stopping all processes..." -ForegroundColor Cyan
    cmd /c "taskkill /F /IM $BACKEND_EXE >nul 2>&1"
    foreach ($p in @($beProc, $feProc)) {
        if ($p) { cmd /c "taskkill /T /F /PID $($p.Id) >nul 2>&1" }
    }
    # last resort: kill whatever still listens on the dev ports (catches orphaned
    # vite/node trees that taskkill /T sometimes misses)
    Stop-DevPortListeners
}
