# CloudPrism 开发脚本：编译后端，并在同一终端内并排输出前后端日志。
#   [后端] 绿色 —— Go HTTP 服务（stdout/stderr + data/logs/cloudprism.log 落盘日志）
#   [前端] 黄色 —— Vite 开发服务（热更新）
# 后端代码改动：重跑本脚本（先结束旧后端再编译启动）。前端改动：Vite 自动热更新。
# Ctrl+C 一次停止全部进程。
$ErrorActionPreference = 'Stop'

$ROOT          = Split-Path $PSScriptRoot -Parent
$BACKEND_DIR   = Join-Path $ROOT 'WindowsGo'
$FRONTEND_DIR  = Join-Path $BACKEND_DIR 'frontend'
$BACKEND_EXE   = 'CloudPrismGo-dev.exe'
$BACKEND_PORT  = 7840
$FRONTEND_PORT = 5173

# ---- [1/4] 结束旧后端 ----
Write-Host '[1/4] 结束后端开发进程（如果存在）...' -ForegroundColor Cyan
cmd /c "taskkill /F /IM $BACKEND_EXE >nul 2>&1"

# ---- [2/4] 编译后端 ----
Write-Host '[2/4] 编译后端...' -ForegroundColor Cyan
Push-Location $BACKEND_DIR
try {
    New-Item -ItemType Directory -Force 'build\bin' | Out-Null
    $env:CGO_ENABLED = '0'
    go build -o "build\bin\$BACKEND_EXE" .
    if ($LASTEXITCODE -ne 0) { Write-Host '后端编译失败。' -ForegroundColor Red; exit 1 }
} finally { Pop-Location }

# ---- 日志文件准备（后端启动前建好，避免丢早期日志） ----
$logDir = Join-Path $BACKEND_DIR 'build\bin\data\logs'
New-Item -ItemType Directory -Force $logDir | Out-Null
$beLog = Join-Path $logDir 'cloudprism.log'
$beOut = Join-Path $logDir 'dev-be-out.log'
$beErr = Join-Path $logDir 'dev-be-err.log'
$feOut = Join-Path $logDir 'dev-fe-out.log'
$feErr = Join-Path $logDir 'dev-fe-err.log'
foreach ($f in @($beOut, $beErr, $feOut, $feErr)) { '' | Set-Content -Encoding ascii -LiteralPath $f }
if (-not (Test-Path $beLog)) { New-Item -ItemType File $beLog | Out-Null }
$beLogLen = (Get-Item $beLog).Length   # 只尾随新日志，不刷历史

# ---- [3/4] 启动后端 ----
Write-Host "[3/4] 启动后端（http://127.0.0.1:$BACKEND_PORT）..." -ForegroundColor Cyan
$env:CP_NO_BROWSER = '1'
$env:CLOUDPRISM_DATA_DIR = Join-Path $BACKEND_DIR '.devdata'   # 开发态数据目录固定，不随 exe 路径漂移
$env:CP_DEV_ORIGIN = "http://127.0.0.1:$FRONTEND_PORT"
$beCmd = "`"$BACKEND_DIR\build\bin\$BACKEND_EXE`" > `"$beOut`" 2> `"$beErr`""
$bePsi = New-Object System.Diagnostics.ProcessStartInfo
$bePsi.FileName = 'cmd.exe'
$bePsi.Arguments = '/d /c "' + $beCmd + '"'
$bePsi.WorkingDirectory = $BACKEND_DIR
$bePsi.UseShellExecute = $false
$bePsi.CreateNoWindow = $true
$beProc = [System.Diagnostics.Process]::Start($bePsi)

# ---- [4/4] 前端依赖 + 启动 Vite ----
Push-Location $FRONTEND_DIR
try {
    if (-not (Test-Path 'node_modules')) {
        Write-Host '[4/4] 首次运行，安装前端依赖...' -ForegroundColor Cyan
        npm install
        if ($LASTEXITCODE -ne 0) { Write-Host '前端依赖安装失败。' -ForegroundColor Red; exit 1 }
    }
} finally { Pop-Location }
Write-Host "[4/4] 启动前端（http://127.0.0.1:$FRONTEND_PORT）..." -ForegroundColor Cyan
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
$script:opened = $false
$sources = @(
    @{ Path = $beOut; Tag = '后端'; Color = 'Green';      Pos = 0L;        Leftover = '' },
    @{ Path = $beErr; Tag = '后端'; Color = 'DarkGreen';  Pos = 0L;        Leftover = '' },
    @{ Path = $beLog; Tag = '后端'; Color = 'Green';      Pos = $beLogLen; Leftover = '' },
    @{ Path = $feOut; Tag = '前端'; Color = 'Yellow';     Pos = 0L;        Leftover = '' },
    @{ Path = $feErr; Tag = '前端'; Color = 'DarkYellow'; Pos = 0L;        Leftover = '' }
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
        Write-Host "[$($s.Tag)] $l" -ForegroundColor $s.Color
        if (-not $script:opened -and $l -match 'Local:') {
            $script:opened = $true
            Start-Process "http://127.0.0.1:$FRONTEND_PORT"
        }
    }
}

Write-Host ''
Write-Host '开发环境就绪：绿色=[后端]  黄色=[前端]  Ctrl+C 停止全部。' -ForegroundColor Cyan
$beWarned = $false; $feWarned = $false
$started = Get-Date
try {
    while ($true) {
        foreach ($s in $sources) { Read-NewLines $s }
        $elapsed = ((Get-Date) - $started).TotalSeconds
        if (-not $beWarned -and $elapsed -gt 3 -and -not (Test-PortListen $BACKEND_PORT)) {
            $beWarned = $true
            Write-Host '[后端] 端口未监听，进程可能已退出。' -ForegroundColor Red
        }
        if (-not $feWarned -and $elapsed -gt 15 -and -not (Test-PortListen $FRONTEND_PORT)) {
            $feWarned = $true
            Write-Host '[前端] 端口未监听，进程可能已退出。' -ForegroundColor Red
        }
        if ($beWarned -and $feWarned) {
            foreach ($s in $sources) { Read-NewLines $s }
            Write-Host '前后端均已退出。' -ForegroundColor Red
            break
        }
        Start-Sleep -Milliseconds 300
    }
} finally {
    Write-Host '正在停止前后端进程...' -ForegroundColor Cyan
    cmd /c "taskkill /F /IM $BACKEND_EXE >nul 2>&1"
    foreach ($p in @($beProc, $feProc)) {
        if ($p) { cmd /c "taskkill /T /F /PID $($p.Id) >nul 2>&1" }
    }
    try {
        $c = Get-NetTCPConnection -LocalPort $FRONTEND_PORT -State Listen -ErrorAction Stop | Select-Object -First 1
        if ($c) { Stop-Process -Id $c.OwningProcess -Force -ErrorAction SilentlyContinue }
    } catch {}
}
