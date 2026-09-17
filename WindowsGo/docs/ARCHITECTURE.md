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
- **绿色便携**：用户数据（配置、凭证、缓存、日志）全部落在
  exe 同级的 `data/`，不写注册表、不写 `%AppData%`、**不写 `%TEMP%`**。
  目录不可写时静默降级，绝不让应用启动失败。
- **明文不落盘**：播放/预览的明文只在内存中停留一个窗口；磁盘上的缓存
  （缩略图 `.cthumb`、媒体分块读缓存）一律是密文或派生自密文，见第 6 节。
- **Windows 专属**：Go 端只服务 Windows。平台相关代码全部收口在
  `internal/platform/win/`，`pkg/` 保持平台无关，未来 Android 端可整体抽出复用。
- **无 cgo**：本机无 gcc，`CGO_ENABLED=0` 是硬前提；所有 Windows API
  经 `golang.org/x/sys/windows` 纯 syscall 调用。

---

## 2. 目录结构与职责

```
WindowsGo/
├── main.go                     Web 模式入口：依赖图 → Listen → 开浏览器 → Serve(goroutine) → 托盘
├── app.go                      NewApp() 依赖图装配 + frontendFingerprint + Version 诊断
├── build/                      release.ps1（go build -H windowsgui）+ windows/icon.ico
├── frontend/                   Vue 3 + Vite + TS；dist/ 入库（//go:embed 依赖），无 wailsjs
├── pkg/                        ★ GUI 无关核心，零平台 import
│   ├── protocol/               格式常量的单一真源（逐条标注 Python 对照行号）
│   ├── cryptox/                KDF / AES-CTR / GCM(12,16) / 文件头 / 文件名 / Vault
│   ├── session/                主密码与「盐 → 派生密钥」缓存
│   ├── vault/                  密库生命周期 + 同步索引
│   ├── pipeline/               Range 映射、分片规划、加解密流水线
│   ├── transfer/               传输队列（并发、重试、断点续传、进度聚合）
│   ├── thumb/                  缩略图解码与两级缓存
│   ├── cache/                  大文件分块读缓存（密文、按阈值切块、LRU 淘汰）
│   ├── perf/  settings/  paths/  update/  syncengine/
│   ├── storage/                三后端：local / webdav / baidu
│   └── streaming/              令牌化流式解密代理（/s/ 播放 /t/ 缩略图 /d/ 下载）
├── internal/
│   ├── appstate/               唯一有状态对象，取代 WindowsPy 的 AppController
│   ├── bind/                   5 个域 struct（Vault/Files/Transfer/Settings/Preview）+ ContextHolder
│   ├── web/                    HTTP server：/api/* JSON + /api/events SSE + 静态前端 + 10Hz 合帧循环
│   ├── tray/                   系统托盘（getlantern/systray，纯 syscall）
│   ├── platform/win/           dpapi / shell / dialogs(IFileOpenDialog) / FatalMessage —— 唯一 syscall 出口
│   └── loggingx/               slog + 脱敏 handler + 2MB×3 轮转
├── interop/                    跨语言黄金向量夹具（testdata/ 入库，向量由 Python 侧生成）
└── docs/                       本文件 + manual_smoke.md
```

### 分层依赖规则（违反即视为设计缺陷）

| 层 | 允许依赖 | 禁止依赖 |
|---|---|---|
| `pkg/protocol`、`pkg/cryptox` | 仅标准库 | 项目内任何其它包 |
| `pkg/*`（其余） | 标准库、`pkg/protocol`、`pkg/cryptox`、第三方库 | `internal/*` |
| `internal/platform/win` | 标准库、`x/sys/windows` | `pkg/*`、`internal/*` |
| `internal/appstate` | `pkg/*`、`internal/*` | `internal/bind` |
| `internal/bind` | `internal/appstate`、`internal/platform/win` | **`pkg/protocol`、`pkg/cryptox`** |
| `internal/web` | `internal/appstate`、`internal/bind`、`pkg/*` | — |
| `main.go` / `app.go` | 以上全部 | — |

`internal/bind` 的唯一职责是把 core 返回的 error 映射为带 `Code` 的错误对象，
**不写任何业务逻辑**。新增能力必须先归入某个域，再决定是否需要新域。

### 制度性约束

- 每个绑定域 struct **≤ 12 个方法**，每个方法**≤ 25 行**；
- `internal/web` 是前端唯一数据通道（fetch /api/* + SSE /api/events），
  `internal/bind` 不再直接暴露给前端；
- 所有流式路径禁止 `append` 增长式拼接，缓冲一律走 `cryptox` 的 `sync.Pool`。

---

## 3. 构建与验证

```powershell
cd C:\Codes\CloudPrism\WindowsGo
$env:CGO_ENABLED = "0"
$env:GOPROXY     = "https://goproxy.cn,direct"

go vet ./...                                   # 必须无告警（COM unsafe 警告为已知误报）
go test ./...                                  # 全部单测（纯逻辑，不打真实网络）
cd frontend && npm run build && cd ..          # 前端产物（vue-tsc + vite build）
go build -ldflags "-s -w -H windowsgui" -o build/bin/CloudPrismGo.exe .
```

- v33 起彻底移除 Wails，构建为纯 `go build`：`-H windowsgui` 让托盘守护进程
  无控制台窗口（双击不闪黑框）；`-s -w` 去符号减体积。无绑定生成、无 `.syso`、
  无 `wails build` 的 main() 二次执行副作用。
- 前端产物由 `npm run build` 生成到 `frontend/dist/`，经 `//go:embed all:frontend/dist`
  内嵌进 exe；`frontend/dist/` **必须入库**（fresh clone 下 embed 才能编译），
  仓库根 `.gitignore` 已为它开白名单例外。
- 前端 TS 类型自 `src/types/appstate.ts`（v33 起自有，替代已删除的 `wailsjs/go/models.ts`）。
- 发布走 `build/release.ps1`（go build + S1 嵌入断言 + S2 双形态一致性 + 指纹清单）。

---

## 4. 阶段 2 spike 的确证结论

以下四条已由一次性 spike 实测验证，后续实现直接采信：

| 项 | 结论 |
|---|---|
| S1a 构建 | 纯 `go build` + `CGO_ENABLED=0` 可构建，无需 gcc；产物约 14MB（含内嵌前端） |
| S4 KDF | 标准库 `crypto/pbkdf2`（Go 1.24+）命中黄金向量 `188492f1…b2615257`，**无需引入 `x/crypto`** |
| S3 DPAPI | `x/sys/windows` 的 `CryptProtectData/CryptUnprotectData/LocalFree` 纯 syscall 往返成功；与 Python `baidu_backend._dpapi_*` **双向互读**，明文逐字节一致，两端密文长度同为 326 字节 |
| JSON 兼容 | Go 的紧凑 JSON（无分隔符空格）可被 Python `json.loads` 正常读取 → `baidu.json` 无需复刻 `json.dumps` 的空格风格 |
| S5 IFileOpenDialog | `dialogs.go` 用 ole32 `CoCreateInstance` + 手动 vtable 调用（纯 syscall），SDK 头核对过 CLSID/IID 与 vtable 索引；COM 失败仅返回 error 给前端提示，不崩进程 |

v33 前 S1b/S2b 的 Wails/WebView2 限制已随架构移除而不复存在：前端经
HTTP/SSE 与后端交互，不再依赖 Chromium Mojo IPC。

### 对计划的必要偏离

**KDF 用标准库 `crypto/pbkdf2` 而非 `golang.org/x/crypto/pbkdf2`**：少一个
第三方依赖，且签名不同（`h func() Hash` 是第一个参数、password 为 `string`），
移植时注意别照抄 x/crypto 的调用形式。

---

## 5. 与 WindowsPy 的有意差异清单

「字节级兼容」只约束**密文与元数据格式**；实现层面的行为差异是有意为之，
逐条记录如下（后续阶段继续追加）。

| # | 差异 | Python 端行为 | Go 端行为 | 理由 |
|---|---|---|---|---|
| 1 | UI 形态 | Qt 桌面窗口（PySide6） | HTTP server + 系统托盘 + 浏览器前端（`127.0.0.1:7840`） | v33 彻底转向 Web 模式；前端可任意浏览器打开，支持多标签/移动端访问 |
| 2 | 浏览器缺失 | —（Qt 自带引擎） | 启动时自动打开默认浏览器；失败仅记 Warn（托盘可随时重开） | Web 模式无内置浏览器；用户机器无默认浏览器时手动访问 URL |
| 3 | 并行加密分段上限 | `num_segments = min(..., 4)`（子进程启动数秒，被迫限流） | 仅受 worker 数与 CPU 核数约束 | goroutine 启动是 µs 级，无需人为限流 |
| 4 | 缩略图缓存位置 | `%TEMP%/cloudprism_thumbs` | `<缓存根>/thumbs/`（默认 `data/cache/thumbs/`） | 修掉便携性不一致；与媒体分块缓存共用同一个可配置根目录 |
| 5 | 缩略图缓存内容 | 最多 512KB 的原始图片头部字节，缩放在 UI 侧每次重做 | 服务端解码 + 缩放到 192px + 重编码 JPEG（约 5–15KB） | 解码开销、IPC 传输量、内存占用同时降一个数量级 |
| 6 | 代理响应 MIME | 恒发 `application/octet-stream` | 按解密后**展示名**的扩展名推断 | Qt Multimedia 会嗅探内容，Chromium 的 `<video>` 强依赖 MIME，照抄会黑屏 |
| 7 | 进度事件频率 | 每个分块回调都 emit，且聚合时全量遍历任务 | core 层原子累计字节，`internal/web` 10Hz 定时合帧经 SSE 推快照 | 避免高频 IPC/SSE 风暴（1MiB 分块 × 4 并发 = 每秒数十次 O(n) 扫描 + JSON 序列化） |
| 8 | 百度凭证刷新 | 多线程可同时触发刷新（竞态） | `sync.Mutex` 串行化，`errno=111` 只重试一次 | 修掉既有竞态 |
| 9 | 设置存储 | QSettings IniFormat（`data/cloudprism.ini`） | `data/config.json`（原子写：tmp + rename）；首启只读导入 INI 一次 | 摆脱 Qt 依赖；老用户数据无缝迁移 |
| 10 | 传输任务生命周期 | `_release_worker` / `thread.wait(5000)` / `_graveyard` | `context` + `WaitGroup` | 整类生命周期问题天然消失 |
| 11 | 速度统计 | `PerfMonitor.report_bytes()` 无生产调用方，状态栏恒为 `--` | 取队列 `done_bytes` 差分 | 修好死接线 |
| 12 | 拖出到资源管理器 | `filesDraggedOut` 信号 | 浏览器原生下载（`<a download>` 指向 /d/ 流式端点）| 浏览器可直接落盘解密文件；目录暂不支持下载（zip 打包后续） |
| 13 | 毛玻璃/亚克力材质 | Qt 实底绘制，无亚克力 | 主题预留 `--acrylic-bg`（`color-mix` 82% 表面色半透明）；**放弃** Wails 窗口级 translucent | 窗口级 translucent 在 Win10/11 行为不一致、影响文字锐度、拖慢合成，CSS 近似零平台风险 |
| 14 | 代理响应缓存头 | 无 `Cache-Control`（Qt 播放器不缓存响应） | 流式响应恒发 `Cache-Control: no-store` | Chromium 会拼 206 片段入磁盘缓存——解密后的明文内容会落盘，必须显式禁止 |
| 15 | 页面功能分布 | 连接信息卡、恢复码卡在**设置页** | 恢复码/同步/连接信息并入**密库页**（VaultsView）；设置页只留纯偏好 + 百度凭证 | 与「最近记录/快速重连」同屏同上下文，操作和信息一处找齐 |
| 16 | 百度授权教程承载 | `gui/baidu_guide.py` 运行时读取 `assets/baidu_guide.md` 文件并弹独立窗口 | 教程 6 步文案内嵌前端（`GUIDE_LINES` 常量），展开卡片展示 | 省掉「运行时资产加载器」整体复杂度；release 随包仍带 md 供人工阅读 |
| 17 | 并行分段粒度 | 修复后按 worker 数均分、向下 16 对齐（大文件单段可达 GB 级） | 固定 `ShardAlign = 1MiB` 分片（≥4MiB 才启用并行） | goroutine 无进程启动成本；1MiB 粒度进度更平滑、取消更及时、峰值内存 = workers×1MiB；偏移恒 16 对齐 → 与 Python 产物逐字节等价（interop 已验证） |
| 18 | 检查更新 UI | 设置页「关于」组提供「检查更新」按钮（对比 GitHub Release） | **未接线 UI**：`pkg/update` checker 有单测但无绑定消费；「关于」组只展示 App.Version() 运行时诊断串 | Go 版暂无产品版本号载体；功能等价缺口，已记录待后续接线 |
| 19 | 大文件读取 | 无本地读缓存，每次 Range 请求都回源（单次响应受 `MAX_RESPONSE_BYTES` 截断） | `pkg/cache` 密文分块读缓存：读穿命中零下载、未命中按原区间回源并落盘分块 | 重看/回拖不再重复下载；分块大小可配且同时是「是否分块」的阈值 |
| 20 | 缓存目录默认位置 | 未配置时落 `%TEMP%/cloudprism_cache`，会持续吃满系统盘 | 程序目录旁 `data/cache`，**代码层面不提供 `%TEMP%`/`%AppData%` 兜底**；用户配置进系统目录直接拒绝 | 缓存红线：绝不写系统盘位置；`pkg/paths.ForbiddenCacheDir` 是唯一闸门 |

### 明确不移植的 Python 历史包袱

① Qt 兼容适配层 `_ActivityBarAdapter/_SidePanelAdapter/_MenuBarShim`；
② `ProcessPoolExecutor` 及其 `PermissionError` 降级分支；
③ `_migrate_registry_settings`（改为只读 INI 一次性导入）；
④ `freeze_support()` + faulthandler crash.log（改 `defer recover()` + slog）；
⑤ `qconfig.load` 重定向（qfluentwidgets 专属）；
⑥ ~~系统托盘~~（v33 已实现：`internal/tray`，getlantern/systray 纯 syscall；Python 端仍无 `QSystemTrayIcon`）；
⑦ 运行时资产文件加载器（`assets/baidu_guide.md` 类文件读取，教程文案内嵌前端，见差异 #16）。

---

## 6. 大文件分块读缓存（`pkg/cache`）

### 6.1 定位

`pkg/streaming` 的代理**本来就是**流式解密：按 HTTP Range 请求回源、解 256KiB
窗口边解边发，明文不落盘 —— 所以「视频必须整文件缓存完才能看」从来不是问题。
`pkg/cache` 补的是另一半：**把已经下载过的密文按块留在本地**，让重看、回拖、
反复预览不再重复下载。

代理不感知缓存的任何概念，只在 `readCipher` 里问一句「本地有吗」：

```
命中   → 直接返回本地字节（零网络往返）
未命中 → 按**原区间**回源（首字节延迟与无缓存时完全一致，绝不放大单次下载）
        → 顺手把这一段喂给缓存
```

### 6.2 磁盘布局

```
<缓存根>/                        默认 <程序目录>/data/cache
    thumbs/                      缩略图加密缓存
    media/<scope>/               媒体分块读缓存（scope = 后端+密库位置+密库 ID 的哈希）
        .meta/<hash>.json        条目元信息（原名 / 是否分块 / 块大小 / 覆盖区间）
        movie.mp4                小于阈值：单独文件
        movie.mp4/               大于等于阈值：以原名命名的子文件夹
            movie.mp4-1
            movie.mp4-2
```

- 阈值与分块大小是**同一个值**（`cache/chunk_mb`，默认 50MB）：小于它整存为
  单独文件，大于等于它按块切分。切块规则由 `buildChunks` 单点决定。
- 未补齐的块/文件带 `.part` 后缀，覆盖区间补齐后去后缀转正 —— 资源管理器里
  能直接看出哪些还在下载中。
- 同名不同路径（`/A/pic.jpg` 与 `/B/pic.jpg`）以 `原名 (2)` 消歧，杜绝串扰。
- **缓存的是密文**：延续「明文不落盘」，AES-CTR 解密开销可忽略，缓存密文
  零性能损失却避免明文视频裸奔在磁盘上。

### 6.3 两个必须知道的实现细节

1. **渐进填充**：块文件用 `WriteAt` 稀疏写入 + 元信息记录覆盖区间，因此
   「读一个 256KiB 窗口」不会退化成「下载整个 50MB 块」。任何时刻只有缺失的
   区间才回源。
2. **密文头部要补写**：播放器的 Range 请求只覆盖明文主体对应的密文段，
   密文头部（salt/iv，`CipherOffset` 之前）永远不会被请求。若不处理，第一个
   块的覆盖区间将永远缺一小段、无法转正。故 `RegisterStream/RegisterDownload`
   会把注册阶段已经下载的文件头一并 `Put` 进去（复用既有下载，零额外往返）。

### 6.4 红线与安全

- `pkg/paths.ForbiddenCacheDir` 是缓存位置的唯一闸门：拒绝 `%TEMP%`、`%TMP%`、
  `%APPDATA%`、`%LOCALAPPDATA%`、`%USERPROFILE%`、`SystemRoot`、`Program Files*`、
  `ProgramData`。默认值（程序目录旁）天然跟随程序所在盘符。
- 作用域目录带占用标记 `.meta/scope.json`：目录若已有非本程序数据，
  `cache.Open` 直接报错而不是开始增删文件 —— 用户误指缓存目录也不会删到个人数据。
- 缓存内容是密文且作用域含密库 ID，因此「换密库 → 不会命中另一个密库的密文」。
  改主密码后旧缓存无法解密，应清空缓存（设置页「清空缓存」）。
- 分块大小变更后旧布局不兼容：元信息记录 `chunkSize`，不一致即视为未命中并重建。

### 6.5 设置项

| 键 | 含义 | 默认 |
|---|---|---|
| `cache/chunk_mb` | 分块大小 = 分块阈值（MB） | 50 |
| `cache/path` | 缓存根目录；空 = 程序目录旁 `data/cache` | 空 |
| `cache/limit_mb` | 媒体分块缓存上限（MB），超出按最近访问淘汰整条 | 512 |

