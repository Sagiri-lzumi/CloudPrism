# CloudPrism Go 端手工冒烟清单

自动化测试覆盖不到的部分（GUI 渲染、真实网盘、系统交互）在这里逐项手工验证。
**每完成一个阶段就把对应小节的勾选项跑一遍**，发现回归立刻停下修，不要攒到最后。

运行前置：

```powershell
cd C:\Codes\CloudPrism\WindowsGo
$env:CGO_ENABLED = "0"
wails build -platform windows/amd64
# 产物：build\bin\CloudPrismGo.exe
```

> ⚠️ 以下所有需要窗口的项，**必须在 IDE 之外的普通 PowerShell 里启动 exe**。
> IDE 的沙箱会禁止 Chromium 建立 Mojo IPC 通道，窗口起不来（详见
> `docs/ARCHITECTURE.md` 第 4 节）。

---

## 阶段 3：工程骨架

- [ ] `wails doctor` 全绿（WebView2 ✅，不提示需要 gcc）
- [ ] `go vet ./...` 无输出
- [ ] `wails build` 成功，产出 `build/bin/CloudPrismGo.exe`
- [ ] 双击 exe：窗口标题为 `CloudPrism`，图标是项目图标（非 Wails 默认 logo）
- [ ] 任务栏 / 资源管理器右键属性 → 详细信息：产品版本 `1.0.0`、
      文件说明 `CloudPrism 端到端加密云盘`、版权 `AGPL-3.0`
- [ ] **启动瞬间不闪黑窗**（`-H windowsgui` 生效）
- [ ] 页面「绑定往返」一栏显示绿色 `pong:skeleton`（= 阶段 2 的 S1b 验证通过）
- [ ] 页面「运行时」一栏显示 `Go go1.27.x · WebView2 <版本> · CGO_ENABLED=0 · windows/amd64`
- [ ] exe 同级生成 `data\webview2\`，且 `%AppData%\CloudPrismGo.exe` **不存在**
      （证明 UDF 显式生效，便携约定未被破坏）
- [ ] 点「退出」按钮，进程正常结束，任务管理器无残留 `CloudPrismGo.exe`
- [ ] DPI 缩放 125% / 150% 下窗口文字不模糊、不错位（`wails.exe.manifest` 的
      permonitorv2 生效）

## 阶段 4–5：核心与绑定层

纯逻辑已由 `go test ./...` 全绿覆盖，这里只列**运行态可观察**项：

- [ ] 首启后 exe 同级 `data\logs\` 出现日志：中文文案，无任何明文密钥/密码/token
- [ ] 日志轮转：连续写满 6MB+ 后该目录恒为 3 个文件（2MB×3），不无限膨胀
- [ ] 浏览含大图的文件夹后 `data\thumb-cache\` 出现 5–15KB 的 192px JPEG
      （服务端重编码，非原图字节）；重进目录无新增文件 = 命中缓存
- [ ] 把 Python 版旧 `data\cloudprism.ini` 拷入 Go exe 同级 `data\` 后首启：
      生成 `config.json` 且旧键已迁移、INI 原文未动（一次性只读导入）
- [ ] 自动锁定：设置页把自动锁设为 5 分钟，空闲等待后界面回锁定态，需重输主密码
- [ ] 播放/预览时把代理地址 `http://127.0.0.1:PORT/s/{token}/…` 复制到浏览器
      新标签访问 → 404（令牌仅本进程内有效，密文路径不外泄）

## 阶段 6：界面

> 骨架期的「绑定往返 pong」页已被正式页面替换，相关验证并入下文各页。

- [ ] 首次启动（清空 `data\`）进初始化向导：选本地文件夹 → 设主密码 →
      （可选）文件名加密 → 完成；向导关闭即已连接，直接进入文件页
- [ ] 文件页：目录树导航 + 网格/列表切换 + 上传（含文件夹）/下载/重命名/删除，
      全程有进度反馈；大目录滚动流畅（虚拟滚动）
- [ ] 右键菜单「导出到…」能把远端文件另存到本地指定目录
- [ ] 从资源管理器拖文件/文件夹进窗口即开始上传（OnFileDrop）
- [ ] 快捷键：F5 刷新、Ctrl+U 上传、Ctrl+D 下载、Ctrl+L 锁库
- [ ] 预览四态分流：图片即点即显；txt/md 文本可读；mp4 可播（播放/暂停/
      进度条/时长/错误行上屏）；`.avi/.mkv` 走「用系统播放器打开」兜底成功
- [ ] 视频拖动进度条能立即 seek 任意位置（206 Range 令牌流），不等待整文件
- [ ] 密库页：连接信息（库名/后端/路径/加密状态）正确；最近记录卡片双击即
      快速重连（只输主密码）；「其它密库」可切换到已记录的库
- [ ] 密库页生成/重生成恢复码（需当前主密码）→ 一次性弹窗展示、可复制，
      旧码立即作废；恢复码不明文落盘（仅加密存库内）
- [ ] 传输页：任务列表实时进度/速度；结束后可一键清空；失败项可重试
- [ ] 断点续传：下载大文件中途强杀 exe → 重启 → 该任务带续传横幅，
      从接近断点处继续而非从头（比对已下载字节）
- [ ] 设置页七组全览：主题三档（跟随系统/深/浅）切换即时生效且重启保持；
      字号 12/14/16/18 即时生效；缓存上限/分块大小/并发数/核心数修改后重启生效
- [ ] 百度凭证：填 Appid/AppKey/SecretKey/SignKey →「检查」通过 →「登录」
      拉起浏览器完成 oob 授权 → 状态变已授权（需真实凭证；无凭证则标注待验）
- [ ] 设置页「关于」组显示版本与运行时信息（App.Version 诊断串）；
      Go 端暂无「检查更新」按钮（未接线，见 ARCHITECTURE 差异 #18）
- [ ] 状态栏：未连接灰（--muted）、已连接绿（--ok）且字重 600
- [ ] 深色主题下启动无白闪；125% / 150% DPI 下各页文字不错位（复查）

## 阶段 7：发布产物

```powershell
powershell -ExecutionPolicy Bypass -File build\release.ps1 -Tag v1
# 预期末行 BUILD ALL OK；产物 Release\<日期>-v1-Go-{dir,exe}\ 两目录
```

- [ ] `-Go-exe\` 内**仅**一个 `CloudPrismGo.exe`；`-Go-dir\` 含 exe +
      `assets\{icon.ico, baidu_guide.md}` + `data\tmp\` + `README-便携版.txt`
      （README 文件名与内容均无乱码）
- [ ] 目录与命名遵循 `YYYY-MM-DD-<tag>-Go-dir` / `-Go-exe` 惯例，含 `-Go` 标识
- [ ] 两个 exe 右键属性 → 详细信息：产品版本 `1.0.0.0`、产品名 `CloudPrism`、
      文件说明正确、图标为项目图标（非 Wails 默认）
- [ ] `-exe` 版拷到 U 盘/任意目录双击：`data\`（含 webview2 缓存）落在 exe 同级；
      `%AppData%` 无 `CloudPrismGo.exe` 残留目录（便携约定）
- [ ] `-dir` 版同测一遍；删除 `data\webview2\` 后重启仍正常（缓存可重建）
- [ ] 双击到窗口可交互的冷启动时间 < 1s；退出后任务管理器无残留进程
- [ ] 在**无 WebView2 运行时的干净机器**上双击：弹出说明框且系统默认浏览器
      自动打开微软官方下载页（fwlink）→ 安装后重启即正常（条件允许时验证）
