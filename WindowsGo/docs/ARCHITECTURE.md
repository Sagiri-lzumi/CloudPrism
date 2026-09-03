# CloudPrism Go 端架构

本文件是 `WindowsGo/` 的活文档：目录职责、分层依赖规则、构建验证命令，
以及**与 `WindowsPy/` 的有意差异清单**。每完成一个阶段就回来更新对应小节，
不要等到最后一次性补写。

协议常量、字节布局等**兼容契约**不在本文件重复，唯一真源是
`WindowsPy/src/cloudprism/`（Go 侧代码注释逐条标注对照行号）。

---

## 1. 目标与边界

- **字节级兼容**：同一份密库、同一个主密码，Go 端与 Python 端必须能互读互写，
  产物逐字节相等。这是所有设计决策的最高约束。
- **绿色便携**：用户数据（配置、凭证、缩略图缓存、WebView2 缓存）全部落在
  exe 同级的 `data/`，不写注册表、不写 `%AppData%`。目录不可写时静默降级，
  绝不让应用启动失败。
- **Windows 专属**：Go 端只服务 Windows。平台相关代码全部收口在
  `internal/platform/win/`，`pkg/` 保持平台无关，未来 Android 端可整体抽出复用。
- **无 cgo**：本机无 gcc，`CGO_ENABLED=0` 是硬前提；所有 Windows API
  经 `golang.org/x/sys/windows` 纯 syscall 调用。

---

## 2. 目录结构与职责

```
WindowsGo/
├── main.go                     装配：环境探测 → 构造状态 → 注册绑定 → wails.Run
├── app.go                      骨架期绑定宿主占位（阶段 5 拆为 5 个域 struct）
├── build/                      appicon.png + windows/{icon.ico, info.json, manifest}
├── frontend/                   Vue 3 + Vite + TS；dist/ 与 wailsjs/ 均入库
├── pkg/                        ★ GUI 无关核心，零 Wails import
│   ├── protocol/               格式常量的单一真源（逐条标注 Python 对照行号）
│   ├── cryptox/                KDF / AES-CTR / GCM(12,16) / 文件头 / 文件名 / Vault
│   ├── session/                主密码与「盐 → 派生密钥」缓存
│   ├── vault/                  密库生命周期 + 同步索引
│   ├── pipeline/               Range 映射、分片规划、加解密流水线
│   ├── transfer/               传输队列（并发、重试、断点续传、进度聚合）
│   ├── thumb/                  缩略图解码与两级缓存
│   ├── perf/  settings/  paths/  update/  syncengine/
│   ├── storage/                三后端：local / webdav / baidu
│   └── streaming/              令牌化流式解密代理
├── internal/
│   ├── appstate/               唯一有状态对象，取代 WindowsPy 的 AppController
│   ├── bind/                   5 个域的 Wails 绑定 struct（Vault/Files/Transfer/Settings/Preview）
│   ├── platform/win/           dpapi / procstats / shell / webview2detect —— 唯一 syscall 出口
│   └── loggingx/               slog + 脱敏 handler + 2MB×3 轮转
├── interop/                    跨语言黄金向量夹具（testdata/ 入库，向量由 Python 侧生成）
└── docs/                       本文件 + manual_smoke.md
```

### 分层依赖规则（违反即视为设计缺陷）

| 层 | 允许依赖 | 禁止依赖 |
|---|---|---|
| `pkg/protocol`、`pkg/cryptox` | 仅标准库 | 项目内任何其它包 |
| `pkg/*`（其余） | 标准库、`pkg/protocol`、`pkg/cryptox`、第三方库 | `internal/*`、Wails |
| `internal/platform/win` | 标准库、`x/sys/windows` | `pkg/*`、`internal/*` |
| `internal/appstate` | `pkg/*`、`internal/*` | Wails 绑定层 |
| `internal/bind` | `internal/appstate`、Wails runtime | **`pkg/protocol`、`pkg/cryptox`** |
| `main.go` / `app.go` | 以上全部 | — |

`internal/bind` 的唯一职责是把 core 返回的 error 映射为带 `Code` 的错误对象，
**不写任何业务逻辑**。新增能力必须先归入某个域，再决定是否需要新域。

### 制度性约束

- 每个绑定域 struct **≤ 12 个方法**，每个方法**≤ 25 行**；
- `frontend/wailsjs/go/bind/*.js` 的文件数恒为 **5**，多出一个即说明分层被破坏；
- 所有流式路径禁止 `append` 增长式拼接，缓冲一律走 `cryptox` 的 `sync.Pool`。

---

## 3. 构建与验证

```powershell
cd C:\Codes\CloudPrism\WindowsGo
$env:CGO_ENABLED = "0"
$env:GOPROXY     = "https://goproxy.cn,direct"

go vet ./...                                   # 必须无告警
go test ./...                                  # 全部单测（纯逻辑，不打真实网络）
wails build -platform windows/amd64            # 产物 build/bin/CloudPrismGo.exe
```

- `wails build` 五步：生成绑定 → 安装前端依赖 → 编译前端 → 生成资源（`.syso`）→ 编译 Go。
  增量约 6s，全量约 45s（含首次 `npm install`）。
- ⚠️ **生成绑定这一步会真实执行 `main()`**：`wails build` 先
  `go build -tags bindings -o %TEMP%\wailsbindings.exe .`，再在项目目录里运行它；
  该 tag 下 `wails.Run` 被换成「生成前端绑定后立即返回」的实现（见 wails v2
  `internal/app/app_bindings.go`）。所以**启动前的副作用必须用 `generatingBindings`
  常量挡住**（`bindings_mode.go` / `run_mode.go`），否则缺 WebView2 的构建机会让
  构建直接失败甚至弹系统模态框，且 `os.Executable()` 指向 `%TEMP%` 会在系统
  临时目录落下 `data\webview2` 垃圾目录（已实测复现）。
- 同一步骤会把 `frontend/wailsjs/go/` **整目录删除后重生**，`runtime/` 亦然；
  故这两个目录的内容必须是可由 `wails build` 完全重建的，不要手工编辑。
- `.syso` 由 winres 每次重生（先写到项目根的 `CloudPrismGo-res.syso`，链接后删除），
  **不入库**；发布脚本恒走 `wails build`，故构建时必然存在。
- ⚠️ **`build/windows/info.json` 的语言键必须是 `0804` 而不是脚手架默认的 `0000`**：
  `0000`（language-neutral）会生成 `StringFileInfo\000004b0` 块，Win32 版本 API
  无法解析它 —— 资源字节确实写进了 exe，但资源管理器与
  `FileVersionInfo` 读到的全是空串。改为 `0804`（zh-CN，与 PyInstaller
  生成的 `080404b0` 一致）后八个字段全部正常可读。
- `frontend/dist/` 与 `frontend/wailsjs/` **必须入库**，否则 fresh clone 下
  `//go:embed all:frontend/dist` 直接编译失败。仓库根 `.gitignore` 已为二者开
  白名单例外；**不要在本目录再放 `.gitignore`**（Wails 脚手架自带的那份含
  `frontend/dist`，嵌套优先级更高，会把白名单顶掉）。

---

## 4. 阶段 2 spike 的确证结论

以下四条已由一次性 spike 实测验证，后续实现直接采信：

| 项 | 结论 |
|---|---|
| S1a 构建 | Wails v2.15.0 + Go 1.27.1 + `CGO_ENABLED=0` 可构建，无需 gcc；产物约 12MB |
| S4 KDF | 标准库 `crypto/pbkdf2`（Go 1.24+）命中黄金向量 `188492f1…b2615257`，**无需引入 `x/crypto`** |
| S3 DPAPI | `x/sys/windows` 的 `CryptProtectData/CryptUnprotectData/LocalFree` 纯 syscall 往返成功；与 Python `baidu_backend._dpapi_*` **双向互读**，明文逐字节一致，两端密文长度同为 326 字节 |
| JSON 兼容 | Go 的紧凑 JSON（无分隔符空格）可被 Python `json.loads` 正常读取 → `baidu.json` 无需复刻 `json.dumps` 的空格风格 |

**S1b（绑定往返）与 S2b（跨源媒体）受 IDE 沙箱限制未在沙箱内完成**：沙箱禁止
Chromium 建立 Mojo IPC 通道（实测 `platform_channel.cc:183 Check failed: 拒绝访问 (0x5)`、
`network_sandbox.cc:410 Failed to grant sandbox access (0x5)`），而 WebView2 的
浏览器进程就是 Chromium。这是环境限制而非技术路线问题，验证脚本见
`Release/_spike/VERIFY_GUI.ps1`（需在沙箱外的普通 PowerShell 中运行）。
骨架页启动即调用 `Ping`，因此在沙箱外运行本程序就等于顺手完成 S1b。

### 对计划的两处必要偏离

1. **`go.mod` 为 `go 1.25.0` 而非计划原定的 1.24**：wails v2.15.0 自身要求
   go >= 1.25，降级会在依赖解析阶段直接失败。
2. **KDF 用标准库 `crypto/pbkdf2` 而非 `golang.org/x/crypto/pbkdf2`**：少一个
   第三方依赖，且签名不同（`h func() Hash` 是第一个参数、password 为 `string`），
   移植时注意别照抄 x/crypto 的调用形式。

---

## 5. 与 WindowsPy 的有意差异清单

「字节级兼容」只约束**密文与元数据格式**；实现层面的行为差异是有意为之，
逐条记录如下（后续阶段继续追加）。

| # | 差异 | Python 端行为 | Go 端行为 | 理由 |
|---|---|---|---|---|
| 1 | WebView2 用户数据目录 | —（Qt 无此概念） | 显式指定 `<exe>/data/webview2`，不可写时降级 `%LOCALAPPDATA%` | go-webview2 默认落 `%AppData%\<exe 名>`，破坏便携约定 |
| 2 | 缺失 WebView2 运行时 | — | 启动前探测注册表，弹系统模态框给出安装指引后退出 | GUI 子系统程序无控制台，否则表现为「双击无反应」 |
| 3 | 并行加密分段上限 | `num_segments = min(..., 4)`（子进程启动数秒，被迫限流） | 仅受 worker 数与 CPU 核数约束 | goroutine 启动是 µs 级，无需人为限流 |
| 4 | 缩略图缓存位置 | `%TEMP%/cloudprism_thumbs` | `data/thumb-cache/` | 修掉便携性不一致 |
| 5 | 缩略图缓存内容 | 最多 512KB 的原始图片头部字节，缩放在 UI 侧每次重做 | 服务端解码 + 缩放到 192px + 重编码 JPEG（约 5–15KB） | 解码开销、IPC 传输量、内存占用同时降一个数量级 |
| 6 | 代理响应 MIME | 恒发 `application/octet-stream` | 按解密后**展示名**的扩展名推断 | Qt Multimedia 会嗅探内容，Chromium 的 `<video>` 强依赖 MIME，照抄会黑屏 |
| 7 | 进度事件频率 | 每个分块回调都 emit，且聚合时全量遍历任务 | core 层原子累计字节，10Hz 定时合帧发快照 | 避免 Wails IPC 风暴（1MiB 分块 × 4 并发 = 每秒数十次 O(n) 扫描 + JSON 序列化） |
| 8 | 百度凭证刷新 | 多线程可同时触发刷新（竞态） | `sync.Mutex` 串行化，`errno=111` 只重试一次 | 修掉既有竞态 |
| 9 | 设置存储 | QSettings IniFormat（`data/cloudprism.ini`） | `data/config.json`（原子写：tmp + rename）；首启只读导入 INI 一次 | 摆脱 Qt 依赖；老用户数据无缝迁移 |
| 10 | 传输任务生命周期 | `_release_worker` / `thread.wait(5000)` / `_graveyard` | `context` + `WaitGroup` | 整类生命周期问题天然消失 |
| 11 | 速度统计 | `PerfMonitor.report_bytes()` 无生产调用方，状态栏恒为 `--` | 取队列 `done_bytes` 差分 | 修好死接线 |
| 12 | 拖出到资源管理器 | `filesDraggedOut` 信号 | 改为「导出到…」按钮 + 右键菜单 | WebView2 无法发起 OS 级 drag-out |

### 明确不移植的 Python 历史包袱

① Qt 兼容适配层 `_ActivityBarAdapter/_SidePanelAdapter/_MenuBarShim`；
② `ProcessPoolExecutor` 及其 `PermissionError` 降级分支；
③ `_migrate_registry_settings`（改为只读 INI 一次性导入）；
④ `freeze_support()` + faulthandler crash.log（改 `defer recover()` + slog）；
⑤ `qconfig.load` 重定向（qfluentwidgets 专属）；
⑥ 系统托盘（已确认 Python 端亦无 `QSystemTrayIcon`，Wails v2 也无托盘 API）。
