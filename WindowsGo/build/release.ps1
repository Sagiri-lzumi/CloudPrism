# CloudPrism WindowsGo 双形态发布打包脚本。
#
# 用法：
#   powershell -ExecutionPolicy Bypass -File build\release.ps1 [-Tag <tag>]
# 默认 Tag=v1；产物落在仓库根 Release\<yyyy-MM-dd>-<Tag>-Go-{dir,exe}/：
#   -dir：CloudPrismGo.exe + assets\{icon.ico,baidu_guide.md}（随包资源，
#         便于日后替换/增补）+ data\tmp\（运行期临时数据目录占位）+
#         WebView2 引导安装器（本机有才附带，可选）+ README-便携版.txt
#   -exe：仅 CloudPrismGo.exe（WebView2 用 Evergreen 运行时，exe 启动时
#         自行探测缺失并打开官方下载页引导安装）
#
# 全部路径用 $PSScriptRoot 相对定位（不写任何绝对路径，风格对齐
# WindowsPy/build/_package.ps1）；任一步失败立即退出并给出非 0 码。
param(
    [string]$Tag = "v1"
)
$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$root    = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path   # WindowsGo/
$repo    = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path # 仓库根
$stamp   = Get-Date -Format "yyyy-MM-dd"
$ver     = "$stamp-$Tag-Go"
$relRoot = Join-Path $repo "Release"
$dirOut  = Join-Path $relRoot "$ver-dir"
$exeOut  = Join-Path $relRoot "$ver-exe"

function Fail([string]$msg) {
    Write-Host "[release] 失败：$msg" -ForegroundColor Red
    exit 1
}
function Step([string]$msg) { Write-Host "[release] $msg" -ForegroundColor Cyan }

# ---------- 1. 工具链（新开的 shell 不带 Go/wails 的 PATH） ----------
$env:Path = "C:\Program Files\Go\bin;$env:USERPROFILE\go\bin;" + $env:Path
$env:CGO_ENABLED = "0"
go version | Out-Null
if ($LASTEXITCODE -ne 0) { Fail "go 不可用（请先安装 Go 1.24+）" }
wails version | Out-Null
if ($LASTEXITCODE -ne 0) { Fail "wails CLI 不可用（go install github.com/wailsapp/wails/v2/cmd/wails@latest）" }

# GOPROXY 探测失败时切国内镜像（go env 本身失败说明环境异常）
go env GOPROXY | Out-Null
if ($LASTEXITCODE -ne 0) { $env:GOPROXY = "https://goproxy.cn,direct" }

# ---------- 2. 前端产物（ci 保证依赖与锁文件一致） ----------
Step "[1/4] 前端构建"
Push-Location (Join-Path $root "frontend")
npm ci | Out-Host
if ($LASTEXITCODE -ne 0) { Pop-Location; Fail "npm ci 失败" }
npm run build | Out-Host
if ($LASTEXITCODE -ne 0) { Pop-Location; Fail "npm run build 失败" }
Pop-Location

# ---------- 3. Go 编译 ----------
# -webview2 browser：不把 ~150MB 的 Evergreen 安装器 embed 进 exe；
# -ldflags "-s -w"：去符号表/调试信息减体积（不用 UPX：压缩后
# WebView2Loader 加载失败且杀软误报率高）
Step "[2/4] wails build"
Push-Location $root
wails build -platform windows/amd64 -clean -webview2 browser -ldflags "-s -w" | Out-Host
if ($LASTEXITCODE -ne 0) { Pop-Location; Fail "wails build 失败" }
Pop-Location

$exe = Join-Path $root "build\bin\CloudPrismGo.exe"
if (-not (Test-Path $exe)) { Fail "未找到构建产物 $exe" }

# ---------- 4. 组装双形态产物 ----------
Step "[3/4] 组装 $ver-dir"
if (Test-Path $dirOut) { Remove-Item $dirOut -Recurse -Force }
New-Item -ItemType Directory -Force -Path $dirOut | Out-Null
Copy-Item $exe $dirOut

# 随包资源：图标与百度凭证教程（源 = 构建资源 + Python 资产目录）
$assetsDir = Join-Path $dirOut "assets"
New-Item -ItemType Directory -Force -Path $assetsDir | Out-Null
Copy-Item (Join-Path $root "build\windows\icon.ico") (Join-Path $assetsDir "icon.ico")
$guideSrc = Join-Path $repo "WindowsPy\assets\baidu_guide.md"
if (Test-Path $guideSrc) {
    Copy-Item $guideSrc (Join-Path $assetsDir "baidu_guide.md")
} else {
    Write-Host "[release] 提示：未找到 WindowsPy\assets\baidu_guide.md，跳过" -ForegroundColor Yellow
}

# 随包交付文档（源 = WindowsGo\docs；用户自测指南 + 视觉对照表）
foreach ($doc in @("self_test_guide.md", "ui_polish_v1.md")) {
    $docSrc = Join-Path $root "docs\$doc"
    if (Test-Path $docSrc) {
        Copy-Item $docSrc (Join-Path $assetsDir $doc)
    } else {
        Write-Host "[release] 提示：未找到 docs\$doc，跳过" -ForegroundColor Yellow
    }
}

# 运行期数据目录占位（UDF/缓存/tmp 均由程序按需 MkdirAll，此处仅留档）
New-Item -ItemType Directory -Force -Path (Join-Path $dirOut "data\tmp") | Out-Null

# WebView2 引导安装器（可选附带：本机常见位置有才复制，找不到不失败）
$bootstraps = @(
    "$env:WEBVIEW2_BOOTSTRAP",
    (Join-Path $env:TEMP "MicrosoftEdgeWebview2Setup.exe"),
    (Join-Path $env:USERPROFILE "Downloads\MicrosoftEdgeWebview2Setup.exe")
) | Where-Object { $_ -and (Test-Path $_) } | Select-Object -First 1
if ($bootstraps) {
    $wvDir = Join-Path $dirOut "WebView2"
    New-Item -ItemType Directory -Force -Path $wvDir | Out-Null
    Copy-Item $bootstraps (Join-Path $wvDir "MicrosoftEdgeWebview2Setup.exe")
    Write-Host "[release] 已附带 WebView2 引导安装器" -ForegroundColor Yellow
} else {
    Write-Host "[release] 提示：本机未找到 WebView2 引导安装器（可选），未附带" -ForegroundColor Yellow
}

# 便携版说明（无 BOM 读取时中文可能乱码，故源文件内直接用单行段落）
$readme = @"
CloudPrism 便携版（Go 版）说明
=============================

这是什么
    端到端加密云盘客户端：文件先加密再上传到本地目录 / WebDAV /
    百度网盘，全程密钥不出本机。

运行前提
    1. Windows 10 / 11（64 位）。
    2. 系统需装有 Microsoft Edge WebView2 运行时（Win11 自带）。
       缺失时程序会自动打开微软官方下载页引导安装。

目录结构
    CloudPrismGo.exe        主程序（单文件，无安装）
    assets\icon.ico         应用图标（随包资源）
    assets\baidu_guide.md   百度网盘开放平台凭证获取教程
    assets\self_test_guide.md 加密链路自测指南（新建库→上传→验证解密→续传）
    assets\ui_polish_v1.md  UI 视觉对照表（对照 Python 版，含待确认项）
    data\                   运行期数据（UDF/日志/缓存/临时文件，可整目录删除，
                            不影响云端密库数据）
    WebView2\               可选：WebView2 引导安装器（未装运行时的机器用）

数据与隐私
    密库本体在云端（本地后端则为所选目录）；本机 data\ 只存设置、
    缩略图缓存与日志，百度凭证经 DPAPI 加密后仅本机可读。
    卸载 = 删除本目录；如要保留云端密库数据请勿删除云端文件。

更新
    覆盖替换 CloudPrismGo.exe 即可（data\ 与云端数据均保留）。
"@
$utf8 = New-Object System.Text.UTF8Encoding($true)  # BOM，记事本直接可读
[System.IO.File]::WriteAllText((Join-Path $dirOut "README-便携版.txt"), $readme, $utf8)

Step "[4/4] 组装 $ver-exe"
if (Test-Path $exeOut) { Remove-Item $exeOut -Recurse -Force }
New-Item -ItemType Directory -Force -Path $exeOut | Out-Null
Copy-Item $exe $exeOut

# ---------- 收尾 ----------
$dirSize = (Get-ChildItem $dirOut -Recurse -File | Measure-Object Length -Sum).Sum
$exeSize = (Get-ChildItem $exeOut -Recurse -File | Measure-Object Length -Sum).Sum
# 注意：先算好字符串再 Write-Host，避免 -f 被当成 -ForegroundColor 缩写
Write-Host ("[release] -dir: {0}（{1:N1} MB）" -f $dirOut, ($dirSize / 1MB))
Write-Host ("[release] -exe: {0}（{1:N1} MB）" -f $exeOut, ($exeSize / 1MB))
Write-Host "BUILD ALL OK"
