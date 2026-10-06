# build.ps1 - CloudPrism 一键发布打包脚本（唯一打包入口）。
#
# 用法（在仓库根运行，路径全部自 $PSScriptRoot 推导）：
#   powershell -ExecutionPolicy Bypass -File scripts\build.ps1 [-Tag <tag>] [-Clean] [-SkipNpmCi] [-SkipFrontend]
#
# 产物（小写 releases\，与既有大写 Release\ 的历史目录区分）：
#   releases\<yyyy-MM-dd>-<Tag>-Go-dir\   便携目录版：exe + assets\ + data\config.json
#                                          + data\tmp\ + README-便携版.txt + 版本信息.txt
#   releases\<yyyy-MM-dd>-<Tag>-Go-exe\   单文件版：仅 CloudPrismGo.exe
#   releases\<yyyy-MM-dd>-<Tag>-Go-MD5.txt  指纹清单（双形态 exe MD5 + 前端产物名 + 提交号）
#
# 版本号规则（2026-09-22 定，沿用不改）：
#   · 未发布期：v1.0 是基线，其后每打一次包递增两位小数（v1.01、v1.02…）。
#   · 正式发布序列从 v1.1 起（v1.1 → v1.2 → …）。
#   · 未发布期迭代号不写 v1.10：与正式序列字面撞车。一律 v<大>.<小> 或
#     v<大>.<两位迭代>，不打 git tag。
#
# -Clean：构建**成功后**才清空旧产物（只留本次新包）。刻意放在最后：
#   先删后建会让任何构建失败把上一个可用包一起毁掉。
# -SkipNpmCi：跳过 npm ci，复用现有 frontend\node_modules。仅依赖变动时才需要 ci。
# -SkipFrontend：前端完全不动，复用现有 frontend\dist。dist 不入库，
#   fresh clone 没有它，此时本开关会失败 —— 先不带开关跑一次。
#
# 任一一步失败立即退出并给出非 0 码，且清掉本次的半成品目录。
param(
    [string]$Tag = "v1.0",
    [switch]$Clean,
    [switch]$SkipNpmCi,
    [switch]$SkipFrontend
)
$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

# 本文件必须是 UTF-8 with BOM：Windows PowerShell 5.1 按 ANSI 读取无 BOM 的
# .ps1，含中文的脚本会被读成乱码并直接解析失败。用编辑器保存时确认编码。

# --- 路径 ---
$repo   = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path   # 仓库根
$root   = Join-Path $repo "WindowsGo"                          # Go 模块根
$feDir  = Join-Path $root "frontend"

# Go 构建缓存固定在仓库内（已在 .gitignore），不污染用户全局缓存。
$env:GOCACHE     = Join-Path $repo ".gocache"
$env:GOMODCACHE  = Join-Path $repo ".gomodcache"
$env:CGO_ENABLED = "0"   # 纯 Go 构建，无 cgo 依赖

# --- 输出定位 ---
$stamp   = Get-Date -Format "yyyy-MM-dd"
$ver     = "$stamp-$Tag-Go"
$relRoot = Join-Path $repo "releases"
$dirOut  = Join-Path $relRoot "$ver-dir"
$exeOut  = Join-Path $relRoot "$ver-exe"
$md5File = Join-Path $relRoot "$ver-MD5.txt"

function Step([string]$msg) { Write-Host "[build] $msg" -ForegroundColor Cyan }

# 共享辅助函数（Remove-Tree 等）：单一实现，见 scripts\lib\CloudPrism.ps1。
. (Join-Path $PSScriptRoot "lib\CloudPrism.ps1")

# Fail：报错 + 清掉本次的半成品输出目录（不碰历史产物）+ 非 0 退出。
function Fail([string]$msg) {
    Write-Host "[build] 失败：$msg" -ForegroundColor Red
    foreach ($p in @($dirOut, $exeOut)) {
        try { Remove-Tree $p } catch { }
    }
    exit 1
}

# ---------- 1. 工具链（新开的 shell 不带 Go 的 PATH） ----------
$env:Path = "C:\Program Files\Go\bin;$env:USERPROFILE\go\bin;" + $env:Path
go version | Out-Null
if ($LASTEXITCODE -ne 0) { Fail "go 不可用（请先安装 Go 1.27+）" }

# GOPROXY 探测失败时切国内镜像（go env 本身失败说明环境异常）
go env GOPROXY | Out-Null
if ($LASTEXITCODE -ne 0) { $env:GOPROXY = "https://goproxy.cn,direct" }

# ---------- 构建来源：git 修订号 ----------
# 与 MD5 一起构成「产物 ↔ 提交」的对应。取不到就写「未知」，绝不因此中断打包
#（用户可能在没有 git 的机器上跑这个脚本）。
$rev = "未知"
$dirty = "未知"
try {
    $revRaw = git -C $repo rev-parse --short HEAD 2>$null
    if ($LASTEXITCODE -eq 0 -and $revRaw) {
        $rev = "$revRaw".Trim()
        $dirty = if (git -C $repo status --porcelain 2>$null) { "有未提交改动" } else { "干净" }
    }
} catch {
    Write-Host "[build] 提示：读取 git 修订号失败，构建信息里记「未知」" -ForegroundColor Yellow
}
Write-Host "[build] 构建来源：$rev（工作区 $dirty）" -ForegroundColor DarkGray

# ---------- 2. 前端产物 ----------
# main.go 用 //go:embed all:frontend/dist 吃这份产物；dist 不入库，缺失时
# 必须先构建，否则 go build 报 "pattern all:frontend/dist: no matching files"。
Step "[1/4] 前端构建（frontend\ -> dist\）"
if ($SkipFrontend) {
    $distHtml = Join-Path $feDir "dist\index.html"
    if (-not (Test-Path $distHtml)) {
        Fail "指定了 -SkipFrontend，但 frontend\dist 不存在（dist 不入库，fresh clone 没有它）；请先不带 -SkipFrontend 跑一次"
    }
    Write-Host "[build] 跳过全部 npm 步骤（-SkipFrontend，复用现有 frontend\dist）" -ForegroundColor Yellow
} else {
    if (-not (Get-Command npm -ErrorAction SilentlyContinue)) {
        Fail "npm 不可用且未指定 -SkipFrontend：前端必须先构建（需要 Node >= 20）"
    }
    Push-Location $feDir
    try {
        if ($SkipNpmCi) {
            if (-not (Test-Path (Join-Path $feDir "node_modules"))) {
                Fail "指定了 -SkipNpmCi，但 frontend\node_modules 不存在，请先在不带该开关的情况下跑一次"
            }
            Write-Host "[build] 跳过 npm ci（-SkipNpmCi，复用现有 node_modules）" -ForegroundColor Yellow
        } elseif (-not (Test-Path (Join-Path $feDir "node_modules"))) {
            Write-Host "[build] 首次构建：npm ci ..." -ForegroundColor Cyan
            npm ci --no-audit --no-fund
            if ($LASTEXITCODE -ne 0) { Fail "npm ci 失败（exit $LASTEXITCODE）" }
        }
        npm run build
        if ($LASTEXITCODE -ne 0) { Fail "npm run build 失败（exit $LASTEXITCODE）" }
    } finally { Pop-Location }
}

# ---------- 3. Go 编译 ----------
# -H windowsgui：托盘守护进程无控制台窗口（双击不闪黑框）；-s -w 去符号减体积。
Step "[2/4] go build"
$exe = Join-Path $root "build\bin\CloudPrismGo.exe"
Push-Location $root
try {
    go build -ldflags "-s -w -H windowsgui" -o $exe .
    if ($LASTEXITCODE -ne 0) { Fail "go build 失败（exit $LASTEXITCODE）" }
} finally { Pop-Location }
if (-not (Test-Path $exe)) { Fail "未找到构建产物 $exe" }

# ---------- S1 自检：exe 内嵌的前端产物必须与 dist 一致 ----------
# 防「前端已重建、包里却是旧 exe」的错配（历史事故）：扫 exe 字节验证 dist
# index.html 引用的 css/js 文件名都在 exe 里，不一致立即失败。
$html = Get-Content (Join-Path $feDir "dist\index.html") -Raw
$assetHashes = [regex]::Matches($html, "assets/(index-[\w-]+\.(?:css|js))") | ForEach-Object { $_.Groups[1].Value }
if (-not $assetHashes) { Fail "dist/index.html 未解析到产物文件名，S1 自检无法执行" }
$exeText = [Text.Encoding]::UTF8.GetString([IO.File]::ReadAllBytes($exe))
foreach ($h in $assetHashes) {
    if (-not $exeText.Contains($h)) { Fail "前端产物 $h 未嵌入 exe（dist 与 exe 不一致），请重新 go build" }
}
Write-Host "[build] S1 通过：exe 已嵌入前端产物 $($assetHashes -join ' + ')" -ForegroundColor Green

# ---------- 4. 组装双形态产物 ----------
Step "[3/4] 组装 $ver-dir"
if (Test-Path $dirOut) { Remove-Tree $dirOut }
New-Item -ItemType Directory -Force -Path $dirOut | Out-Null
Copy-Item $exe $dirOut

# 随包资源：图标 + 面向用户的文档（内部工程文档不进包）
$assetsDir = Join-Path $dirOut "assets"
New-Item -ItemType Directory -Force -Path $assetsDir | Out-Null
Copy-Item (Join-Path $root "build\windows\icon.ico") (Join-Path $assetsDir "icon.ico")
foreach ($doc in @("self_test_guide.md", "baidu_guide.md")) {
    $docSrc = Join-Path $root "docs\$doc"
    if (Test-Path $docSrc) { Copy-Item $docSrc (Join-Path $assetsDir $doc) }
    else { Write-Host "[build] 提示：未找到 docs\$doc，跳过" -ForegroundColor Yellow }
}

# 启动配置随包交付（唯一需要用户在部署前手工编辑的文件）。值必须与
# pkg/config 的 Default() 保持一致：改默认要两边一起改。
$dataDir = Join-Path $dirOut "data"
New-Item -ItemType Directory -Force -Path (Join-Path $dataDir "tmp") | Out-Null
$cfgJson = @"
{
  "port": 7840,
  "port_range": 10
}
"@
$utf8Bom = New-Object System.Text.UTF8Encoding($true)
[System.IO.File]::WriteAllText((Join-Path $dataDir "config.json"), $cfgJson, $utf8Bom)

# 版本信息.txt：用户用记事本即可核对所跑 exe 是否带最新前端（对照仓库
# frontend/dist/assets/ 实际文件名），避免「删了缓存重启仍看到旧 UI」无从排查。
$verInfo = @(
    "CloudPrismGo 构建信息",
    "====================",
    "构建时间：$(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')",
    "标签：$Tag",
    "提交：$rev（工作区 $dirty）",
    "前端产物：$($assetHashes -join ' + ')",
    "对应 dist 目录：WindowsGo/frontend/dist/assets/",
    "",
    "排障步骤：",
    "1. 打开仓库 WindowsGo/frontend/dist/assets/，对照上面的文件名。",
    "2. 若打不开界面（页面报错/无法访问），查看 exe 旁边 data/logs/cloudprism.log。",
    "3. 改了 data/config.json 但没生效：查日志里的「启动配置回退」WARN。"
)
[System.IO.File]::WriteAllText((Join-Path $dirOut "版本信息.txt"), ($verInfo -join "`r`n"), $utf8Bom)

$readme = @"
CloudPrism 便携版（Go 版）说明
=============================

这是什么
    端到端加密云盘客户端：文件先加密再上传到本地目录 / WebDAV /
    百度网盘，全程密钥不出本机。

运行前提
    Windows 10 / 11（64 位）。界面在浏览器中打开（无需 WebView2）。
    启动后自动打开默认浏览器；若未自动打开，请手动访问
    http://127.0.0.1:7840 （程序托盘图标可随时重新打开界面）。

目录结构
    CloudPrismGo.exe        主程序（单文件，无安装，常驻系统托盘）
    data\config.json        启动配置：端口 / 顺延范围（改完重启生效）
    assets\icon.ico         应用图标（随包资源）
    assets\baidu_guide.md   百度网盘开放平台凭证获取教程
    assets\self_test_guide.md 加密链路自测指南（新建库→上传→验证解密→续传）
    data\                   运行期数据（日志/缓存/临时文件，可整目录删除，
                            不影响云端密库数据）
    data\logs\              日志（cloudprism.log，排障用）

启动配置（data\config.json）
    改完重启生效。文件缺失、损坏或字段非法时一律回退默认值，并在
    data\logs\cloudprism.log 记「启动配置回退」WARN，不会阻塞启动。

        port        7840         起始监听端口，被占用时顺延
        port_range  10           顺延范围：试 port .. port+port_range-1

    范围内所有端口都被占用时程序无法启动：把 port_range 调大或腾出一个端口。

数据与隐私
    密库本体在云端（本地后端则为所选目录）；本机 data\ 只存设置、
    缩略图缓存与日志，百度凭证经 DPAPI 加密后仅本机可读。
    卸载 = 删除本目录；如要保留云端密库数据请勿删除云端文件。

更新
    覆盖替换 CloudPrismGo.exe 即可（data\ 与云端数据均保留）。
"@
[System.IO.File]::WriteAllText((Join-Path $dirOut "README-便携版.txt"), $readme, $utf8Bom)

Step "[4/4] 组装 $ver-exe"
if (Test-Path $exeOut) { Remove-Tree $exeOut }
New-Item -ItemType Directory -Force -Path $exeOut | Out-Null
Copy-Item $exe $exeOut

# ---------- S2 双形态一致性：-dir 与 -exe 的 exe 必须逐字节一致 ----------
$hashDir = (Get-FileHash (Join-Path $dirOut "CloudPrismGo.exe") -Algorithm MD5).Hash
$hashExe = (Get-FileHash (Join-Path $exeOut "CloudPrismGo.exe") -Algorithm MD5).Hash
if ($hashDir -ne $hashExe) {
    Fail "S2 失败：-dir 与 -exe 的 exe 不一致（dir=$hashDir exe=$hashExe），疑似装错产物"
}
Write-Host "[build] S2 通过：双形态 exe 一致（MD5=$hashDir）" -ForegroundColor Green

# ---------- S3 指纹清单 ----------
$stampLines = @(
    "CloudPrismGo $ver 构建指纹",
    "时间: $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')",
    "提交: $rev（工作区 $dirty）",
    "-dir exe MD5: $hashDir",
    "-exe exe MD5: $hashExe",
    "前端产物: $($assetHashes -join ' + ')",
    "自检: S1 嵌入断言通过 / S2 双形态一致通过"
)
[System.IO.File]::WriteAllLines($md5File, $stampLines, $utf8Bom)
Write-Host "[build] 指纹清单: $md5File"

# ---------- 5. -Clean：成功之后才清旧产物 ----------
# 到达这里说明双形态已组装且自检全过。此时删除历史产物才是安全的。
# 只删目录与 *-MD5.txt（本脚管的产物形态）；散落的其它文件不动，避免误删
# 用户放进 releases\ 的东西。
if ($Clean -and (Test-Path $relRoot)) {
    Step "[5/5] 清理旧产物（-Clean）"
    $removed = 0
    Get-ChildItem $relRoot -Force | ForEach-Object {
        $isOurs = $_.PSIsContainer -or ($_.Name -like "*-Go-MD5.txt")
        $isCurrent = $_.FullName -in @($dirOut, $exeOut, $md5File)
        if ($isOurs -and -not $isCurrent) {
            Write-Host "[build] 删除 $($_.Name)" -ForegroundColor DarkGray
            Remove-Tree $_.FullName
            $removed++
        }
    }
    Write-Host "[build] 已删除 $removed 个旧产物，保留 $ver" -ForegroundColor Green
}

# ---------- 收尾 ----------
$dirSize = (Get-ChildItem $dirOut -Recurse -File | Measure-Object Length -Sum).Sum
$exeSize = (Get-ChildItem $exeOut -Recurse -File | Measure-Object Length -Sum).Sum
Write-Host ""
Write-Host ("[build] -dir: {0}（{1:N1} MB）" -f $dirOut, ($dirSize / 1MB)) -ForegroundColor Green
Write-Host ("[build] -exe: {0}（{1:N1} MB）" -f $exeOut, ($exeSize / 1MB)) -ForegroundColor Green
Write-Host "BUILD ALL OK" -ForegroundColor Green
