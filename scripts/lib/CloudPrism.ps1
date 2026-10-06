# lib/CloudPrism.ps1 - scripts/ 下各入口脚本共享的辅助函数。
#
# 用法（在每个入口脚本顶部，$PSScriptRoot 已就位后）：
#   . (Join-Path $PSScriptRoot "lib\CloudPrism.ps1")
#
# 为什么集中在这里：清理逻辑（删产物、杀进程）曾经在 build/dev/run 三个
# 脚本里各写一套，行为有细微差别且极易漂移。这里有且只有一份实现，
# 改语义只改这里。
#
# 共同原则：
#   - 只动「本仓库产生的东西」：删只删脚本产物的目录，杀进程只杀路径
#     落在本仓库内的进程，绝不碰碰巧占用了同一端口/同一 exe 名的外部进程。
#   - 失败要响：删除失败重试后仍失败就抛错，而不是静默跳过或无限等待。

# Remove-Tree 高效递归删除。
#
# 不用 Remove-Item -Recurse：PS 5.1 的实现在深层目录上会反复重枚举，实测
# 删 node_modules 这类「目录深 + 小文件极多」的树只有约 1 文件/秒。.NET 的
# Directory.Delete(path,true) 直接走 Win32，快 1~2 个数量级。
#
# 删不掉时不回退 Remove-Item，而是重试后直接抛错，原因是实测踩过的坑：
# 某些环境把 Remove-Item 接到「发送到回收站」，遇到被占用的文件会弹
# OnlyErrorDialogs 模态框并无限等待 —— 脚本就此挂死。
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
    throw "无法删除（多半被其他进程占用）：$path - $($lastErr.Exception.Message)"
}

# Stop-RepoProcess 结束名为 $processName 且可执行路径在 $repoRoot 之下的
# 进程。返回实际结束的进程数。
#
# 必须按路径前缀过滤，不能只按 exe 名：同名程序可能来自另一份安装
#（例如用户同时装了发布版和源码仓库版），误杀后果自负不起。
function Stop-RepoProcess([string]$processName, [string]$repoRoot) {
    $killed = 0
    foreach ($p in @(Get-Process -Name $processName -ErrorAction SilentlyContinue)) {
        $path = $null
        try { $path = $p.Path } catch { }
        if ($path -and $path.StartsWith($repoRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
            Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
            Write-Host "  stopped $processName (pid $($p.Id))" -ForegroundColor DarkGray
            $killed++
        } else {
            Write-Host "  leaving $processName (pid $($p.Id)) alone - not from this repo" -ForegroundColor DarkYellow
        }
    }
    return $killed
}

# Stop-RepoPortListeners 释放一组端口：结束监听这些端口、且命令行引用了
# $repoRoot 的进程。返回实际结束的进程数。
#
# 端口过滤用命令行而不是可执行路径：node.exe / vite 这类进程的可执行路径
# 是 Node 安装目录，不含仓库路径，只有命令行里带脚本路径。
function Stop-RepoPortListeners([int[]]$ports, [string]$repoRoot) {
    $killed = 0
    foreach ($port in $ports) {
        try {
            $procIds = Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction Stop |
                Select-Object -ExpandProperty OwningProcess -Unique
        } catch { continue }
        foreach ($procId in $procIds) {
            $cmdLine = (Get-CimInstance Win32_Process -Filter "ProcessId=$procId" -ErrorAction SilentlyContinue).CommandLine
            if ($cmdLine -and $cmdLine.Contains($repoRoot)) {
                Stop-Process -Id $procId -Force -ErrorAction SilentlyContinue
                Write-Host "  freed port $port (pid $procId)" -ForegroundColor DarkGray
                $killed++
            } else {
                Write-Host "  warn: port $port held by unrelated process (pid $procId), left running." -ForegroundColor DarkYellow
            }
        }
    }
    return $killed
}
