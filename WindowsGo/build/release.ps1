# CloudPrism WindowsGo Web 模式发布打包脚本。
#
# 用法：
#   powershell -ExecutionPolicy Bypass -File build\release.ps1 [-Tag <tag>] [-Clean] [-SkipNpmCi] [-SkipFrontend]
# 默认 Tag=v1.0；产物落在仓库根 Release\<yyyy-MM-dd>-<Tag>-Go-{dir,exe}/：
#
# 版本号规则（2026-09-22 用户定，**未发布期与发布后是两套**）：
#   · **未发布期**：`v1.0` 是基线（第一个完整可用的包），其后**每改完打包一次**
#     递增两位小数 —— `v1.01`、`v1.02`…（两位小数读作「第 N 次迭代包」，
#     它明说了这是未发布的迭代，不会跟正式序列混淆）。
#   · **正式发布序列从 `v1.1` 起**（用户说了「v1.1 之后才是之后的」），
#     其后按 `v1.1` → `v1.2` → … 递增。
#   ⇒ 不要把未发布期的迭代号写成 `v1.10` 这类：它与正式序列的 `v1.1`→`v1.10`
#     在字面上撞车（这正是 2026-09-22 全部重打一遍的原因）。
#   · 标签形态一律 `v<大>.<小>` 或 `v<大>.<两位迭代>`，**不沿用历史连续递增序号**
#     （那串已到 v38，用户嫌太长），也**不打 git tag**（见下方说明）。
#   -dir：CloudPrismGo.exe + assets\{icon.ico,self_test_guide.md,baidu_guide.md}（随包资源，
#         便于日后替换/增补）+ data\tmp\（运行期临时数据目录占位）+
#         README-便携版.txt
#   -exe：仅 CloudPrismGo.exe（Web 模式，浏览器打开界面，无需 WebView2）
#
# -Clean：打包前先清空旧产物 —— 清掉仓库根 Release\ 下的全部内容，以及
#   WindowsGo\ 下历史遗留的 Release* 临时发布目录，只留本次新包。
#   不指定该开关时行为与历史版本完全一致（不删任何东西）。
#
# -SkipNpmCi：跳过 npm ci，直接用现有 frontend\node_modules 构建。
#   npm ci 会先删光再重装整个 node_modules（本机实测数千次小文件写 +
#   杀软实时扫描，单次可达十几分钟甚至卡死），仅在前端依赖确实变动、
#   或需要严格对齐锁文件时才需要跑；日常改前端代码用 npm run build 即可。
#
# -SkipFrontend：前端完全不动，直接复用现有 frontend\dist。
#   适用于「本轮只改后端/只打包」的场景。dist 必须已是最新（go:embed 直接
#   吃它），S1 自检仍会校验 exe 内嵌资源与 dist 一致，装错产物会立即失败。
#
# 全部路径用 $PSScriptRoot 相对定位（不写任何绝对路径，便于仓库整体搬迁）；
# 任一步失败立即退出并给出非 0 码。
param(
    [string]$Tag = "v1.0",
    [switch]$Clean,
    [switch]$SkipNpmCi,
    [switch]$SkipFrontend
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

# Remove-Tree 高效递归删除。
#
# 不用 Remove-Item -Recurse：PS 5.1 的实现在深层目录上会反复重枚举，
# 实测删 WebView2/UDF 这类「目录深 + 小文件极多」的树只有约 1 文件/秒
# （一次 -Clean 要等数小时）。.NET 的 Directory.Delete(path,true) 直接走
# Win32，快 1~2 个数量级。
#
# 删不掉时不回退 Remove-Item，而是重试后直接失败退出，原因是实测踩过的坑：
# 某些环境（含自动化沙箱）把 Remove-Item 接到了「发送到回收站」，遇到被
# 占用的文件会弹 OnlyErrorDialogs 模态框并**无限等待**——打包脚本就此挂死
# （实测卡在组装步骤 8 分钟以上，进程 CPU 仅 3 秒 = 纯阻塞，且模态框不会
# 因非交互主机而自动消失）。失败要响、不要等：把异常原样抛出来定位占用者。
function Remove-Tree([string]$path) {
    if (-not (Test-Path -LiteralPath $path)) { return }
    $item = Get-Item -LiteralPath $path -Force

    # 先清只读属性：.NET 的 Delete 遇到只读文件会直接抛 IOException，
    # 旧产物里可能残留只读文件，不清属性就会白白走进重试分支。
    if ($item.PSIsContainer) {
        Get-ChildItem -LiteralPath $item.FullName -Recurse -Force -ErrorAction SilentlyContinue |
            ForEach-Object { try { $_.Attributes = [System.IO.FileAttributes]::Normal } catch { } }
    } else {
        try { [System.IO.File]::SetAttributes($item.FullName, [System.IO.FileAttributes]::Normal) } catch { }
    }

    # 占用往往是瞬态的（杀软实时扫描、上一进程刚退出尚未释放句柄），重试有意义
    $lastErr = $null
    for ($i = 0; $i -lt 3; $i++) {
        try {
            if ($item.PSIsContainer) {
                [System.IO.Directory]::Delete($item.FullName, $true)
            } else {
                [System.IO.File]::Delete($item.FullName)
            }
        } catch { $lastErr = $_ }
        if (-not (Test-Path -LiteralPath $path)) { return }
        Start-Sleep -Milliseconds 400
    }

    Fail "旧产物无法删除（多半被其他进程占用）：$path`n  $($lastErr.Exception.Message)"
}

# ---------- 0. 可选清理（-Clean） ----------
# 放在工具链检查之前：清理由用户显式要求，不应因 Go/npm 缺失而跳过。
if ($Clean) {
    Step "[0/4] 清理旧产物（-Clean）"
    if (Test-Path $relRoot) {
        Get-ChildItem $relRoot -Force | ForEach-Object {
            Write-Host "[release] 删除 $($_.Name)" -ForegroundColor DarkGray
            Remove-Tree $_.FullName
        }
    } else {
        New-Item -ItemType Directory -Force -Path $relRoot | Out-Null
    }
    # WindowsGo\ 下历史遗留的临时发布目录（Release / Release-v33test 之类），
    # 易与正式产物混淆，一并清掉；按 Release* 通配以免写死具体版本号。
    Get-ChildItem $root -Directory -Filter "Release*" -Force | ForEach-Object {
        Write-Host "[release] 清理源码树临时目录 $($_.Name)" -ForegroundColor DarkGray
        Remove-Tree $_.FullName
    }
    Write-Host "[release] 旧产物已清空" -ForegroundColor Green
}

# ---------- 1. 工具链（新开的 shell 不带 Go 的 PATH） ----------
$env:Path = "C:\Program Files\Go\bin;$env:USERPROFILE\go\bin;" + $env:Path
$env:CGO_ENABLED = "0"
go version | Out-Null
if ($LASTEXITCODE -ne 0) { Fail "go 不可用（请先安装 Go 1.24+）" }

# GOPROXY 探测失败时切国内镜像（go env 本身失败说明环境异常）
go env GOPROXY | Out-Null
if ($LASTEXITCODE -ne 0) { $env:GOPROXY = "https://goproxy.cn,direct" }

# ---------- 构建来源：git 修订号 ----------
# 为什么单列一项：用户反复被「跑的到底是哪个包」困住 —— 单实例探测会把新 exe
# 静默顶掉、界面仍旧版，而 `-MD5.txt` 只给指纹不给来源。Go 默认 `-buildvcs=auto`
# 确实会把修订号编进 exe，但那要专用工具才读得出；这里直接落成文本，与 MD5 一起
# 构成「产物 ↔ 提交」的对应。**取不到就写「未知」，绝不因此中断打包**
# （用户可能在没有 git 的机器上跑这个脚本）。
$rev = "未知"
$dirty = "未知"
try {
    $revRaw = git rev-parse --short HEAD 2>$null
    if ($LASTEXITCODE -eq 0 -and $revRaw) {
        $rev = "$revRaw".Trim()
        $dirty = if (git status --porcelain 2>$null) { "有未提交改动" } else { "干净" }
    }
} catch {
    Write-Host "[release] 提示：读取 git 修订号失败，构建信息里记「未知」" -ForegroundColor Yellow
}
Write-Host "[release] 构建来源：$rev（工作区 $dirty）" -ForegroundColor DarkGray

# ---------- 2. 前端产物 ----------
# 默认跑 npm ci + npm run build；两个开关用于跳过（见文件头说明）：
#   -SkipNpmCi    跳过 ci，复用现有 node_modules 再 build
#   -SkipFrontend 前端 dist 已是最新时整体跳过，go:embed 直接吃现成 dist
Step "[1/4] 前端构建"
$feDir = Join-Path $root "frontend"
if ($SkipFrontend) {
    $distHtml = Join-Path $feDir "dist\index.html"
    if (-not (Test-Path $distHtml)) {
        Fail "指定了 -SkipFrontend，但 frontend\dist\index.html 不存在，请先完整跑一次前端构建"
    }
    Write-Host "[release] 跳过全部 npm 步骤（-SkipFrontend，复用现有 frontend\dist）" -ForegroundColor Yellow
    Write-Host "[release] 提示：仅当前端源码未变动时可用；S1 自检仍会校验 exe 与 dist 一致" -ForegroundColor DarkGray
} else {
    Push-Location $feDir
    if ($SkipNpmCi) {
        if (-not (Test-Path (Join-Path $feDir "node_modules"))) {
            Pop-Location; Fail "指定了 -SkipNpmCi，但 frontend\node_modules 不存在，请先在不带该开关的情况下跑一次"
        }
        Write-Host "[release] 跳过 npm ci（-SkipNpmCi，复用现有 node_modules）" -ForegroundColor Yellow
    } else {
        npm ci
        if ($LASTEXITCODE -ne 0) { Pop-Location; Fail "npm ci 失败" }
    }
    npm run build
    if ($LASTEXITCODE -ne 0) { Pop-Location; Fail "npm run build 失败" }
    Pop-Location
}

# ---------- 3. Go 编译 ----------
# -H windowsgui：托盘守护进程无控制台窗口（Web 模式形态）；
# -ldflags "-s -w"：去符号表/调试信息减体积。
Step "[2/4] go build"
Push-Location $root
go build -ldflags "-s -w -H windowsgui" -o "build\bin\CloudPrismGo.exe" .
if ($LASTEXITCODE -ne 0) { Pop-Location; Fail "go build 失败" }
Pop-Location

$exe = Join-Path $root "build\bin\CloudPrismGo.exe"
if (-not (Test-Path $exe)) { Fail "未找到构建产物 $exe" }

# ---------- 产物自检 ----------
# S1 前端嵌入断言：exe 内必须能找到 dist/index.html 引用的 css/js 文件名。
# 曾发生「前端已改、dist 已重建，但发布目录里放的是旧 exe」的错配事故
# （v15-dir 曾错放 v14 exe），此处直接扫 exe 字节验证内嵌资源与 dist 一致，
# 不一致立即失败，杜绝「打包了却看不到改动」类问题。
$html = Get-Content (Join-Path $root "frontend\dist\index.html") -Raw
$assetHashes = [regex]::Matches($html, 'assets/(index-[\w-]+\.(?:css|js))') | ForEach-Object { $_.Groups[1].Value }
if (-not $assetHashes) { Fail "dist/index.html 未解析到产物文件名，S1 自检无法执行" }
$exeBytes = [IO.File]::ReadAllBytes($exe)
$exeText = [Text.Encoding]::UTF8.GetString($exeBytes)
foreach ($h in $assetHashes) {
    if (-not $exeText.Contains($h)) { Fail "前端产物 $h 未嵌入 exe（dist 与 exe 不一致），请重新 go build" }
}
Write-Host "[release] S1 通过：exe 已嵌入前端产物 $($assetHashes -join ' + ')" -ForegroundColor Green

# ---------- 4. 组装双形态产物 ----------
Step "[3/4] 组装 $ver-dir"
if (Test-Path $dirOut) { Remove-Tree $dirOut }
New-Item -ItemType Directory -Force -Path $dirOut | Out-Null
Copy-Item $exe $dirOut

# 随包资源：图标
$assetsDir = Join-Path $dirOut "assets"
New-Item -ItemType Directory -Force -Path $assetsDir | Out-Null
Copy-Item (Join-Path $root "build\windows\icon.ico") (Join-Path $assetsDir "icon.ico")

# 随包交付文档（源 = WindowsGo\docs；用户自测指南 + 百度凭证教程）
# 只放**面向用户**的文档。内部工程文档（如 ui_polish_v1.md 这类对照/审计表）
# 不进包 —— 它们讲的是实现沿革与内部取舍，对使用者没有意义。
foreach ($doc in @("self_test_guide.md", "baidu_guide.md")) {
    $docSrc = Join-Path $root "docs\$doc"
    if (Test-Path $docSrc) {
        Copy-Item $docSrc (Join-Path $assetsDir $doc)
    } else {
        Write-Host "[release] 提示：未找到 docs\$doc，跳过" -ForegroundColor Yellow
    }
}

# 运行期数据目录占位（UDF/缓存/tmp 均由程序按需 MkdirAll，此处仅留档）
New-Item -ItemType Directory -Force -Path (Join-Path $dirOut "data\tmp") | Out-Null

# 版本信息.txt：写前端产物文件名 + 排障指引。用户用记事本打开即可核对
# 跑的 exe 是否带最新前端（对照仓库 frontend/dist/assets/ 实际文件名），
# 避免「删了 UDF 重启仍看到旧 UI」时无从判断根因（v15/v17 反复出现）。
$verInfo = @(
    "CloudPrismGo 构建信息",
    "====================",
    "构建时间：$stamp",
    "标签：$Tag",
    "提交：$rev（工作区 $dirty）",
    "前端产物：$($assetHashes -join ' + ')",
    "对应 dist 目录：WindowsGo/frontend/dist/assets/",
    "",
    "排障步骤：",
    "1. 用资源管理器打开 仓库 WindowsGo/frontend/dist/assets/，对照上面的文件名。",
    "2. 若打不开界面（页面报错/无法访问），查看 exe 旁边 data/logs/cloudprism.log。"
)
$verInfoPath = Join-Path $dirOut "版本信息.txt"
$verInfo -join "`r`n" | Out-File -FilePath $verInfoPath -Encoding UTF8
Write-Host "[release] 已写 $verInfoPath" -ForegroundColor Green

# 便携版说明（无 BOM 读取时中文可能乱码，故源文件内直接用单行段落）
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
    assets\icon.ico         应用图标（随包资源）
    assets\baidu_guide.md   百度网盘开放平台凭证获取教程
    assets\self_test_guide.md 加密链路自测指南（新建库→上传→验证解密→续传）
    data\                   运行期数据（日志/缓存/临时文件，可整目录删除，
                            不影响云端密库数据）
    data\logs\              日志（cloudprism.log，排障用）

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
if (Test-Path $exeOut) { Remove-Tree $exeOut }
New-Item -ItemType Directory -Force -Path $exeOut | Out-Null
Copy-Item $exe $exeOut

# S2 双形态一致性断言：-dir 与 -exe 两处 exe 必须逐字节一致（防装错产物）
$hashDir = (Get-FileHash (Join-Path $dirOut "CloudPrismGo.exe") -Algorithm MD5).Hash
$hashExe = (Get-FileHash (Join-Path $exeOut "CloudPrismGo.exe") -Algorithm MD5).Hash
if ($hashDir -ne $hashExe) {
    Fail "S2 失败：-dir 与 -exe 的 exe 不一致（dir=$hashDir exe=$hashExe），疑似装错产物"
}
Write-Host "[release] S2 通过：双形态 exe 一致（MD5=$hashDir）" -ForegroundColor Green

# S3 指纹清单：构建时间 / 双 exe MD5 / 前端产物名落盘，供用户核对所跑版本
$stampLines = @(
    "CloudPrismGo $ver 构建指纹",
    "时间: $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')",
    "提交: $rev（工作区 $dirty）",
    "-dir exe MD5: $hashDir",
    "-exe exe MD5: $hashExe",
    "前端产物: $($assetHashes -join ' + ')",
    "自检: S1 嵌入断言通过 / S2 双形态一致通过"
)
$md5File = Join-Path $relRoot "$ver-MD5.txt"
[System.IO.File]::WriteAllLines($md5File, $stampLines, (New-Object System.Text.UTF8Encoding($true)))
Write-Host ("[release] 指纹清单: {0}" -f $md5File)

# ---------- 收尾 ----------
$dirSize = (Get-ChildItem $dirOut -Recurse -File | Measure-Object Length -Sum).Sum
$exeSize = (Get-ChildItem $exeOut -Recurse -File | Measure-Object Length -Sum).Sum
# 注意：先算好字符串再 Write-Host，避免 -f 被当成 -ForegroundColor 缩写
Write-Host ("[release] -dir: {0}（{1:N1} MB）" -f $dirOut, ($dirSize / 1MB))
Write-Host ("[release] -exe: {0}（{1:N1} MB）" -f $exeOut, ($exeSize / 1MB))
Write-Host "BUILD ALL OK"
