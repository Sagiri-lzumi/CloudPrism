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
│   ├── scripts/gen-icons.mjs   图标注册表生成器（按源码引用裁剪，prebuild 自动跑，详见 §8.2）
│   └── src/lib/icons.gen.ts    ★ 生成物，勿手工编辑；图标显式 import 表
├── pkg/                        ★ GUI 无关核心，零平台 import
│   ├── protocol/               格式常量的单一真源（逐条标注 Python 对照行号）
│   ├── cryptox/                KDF / AES-CTR / GCM(12,16) / 文件头 / 文件名 / Vault
│   ├── session/                主密码与「盐 → 派生密钥」缓存
│   ├── vault/                  密库生命周期 + 同步索引
│   ├── pipeline/               Range 映射、分片规划、加解密流水线
│   ├── transfer/               传输队列（并发、重试、断点续传、进度聚合）
│   ├── thumb/                  缩略图解码与两级缓存
│   ├── cache/                  大文件分块读缓存（密文、按阈值切块、LRU 淘汰）
│   ├── secret/                 单个秘密值的加密落盘（DPAPI:/PLAIN: 前缀，局域网令牌用）
│   ├── perf/  settings/  paths/  update/  syncengine/
│   ├── storage/                三后端：local / webdav / baidu
│   └── streaming/              令牌化流式解密代理（/s/ 播放 /t/ 缩略图 /d/ 下载）
├── internal/
│   ├── appstate/               唯一有状态对象，取代 WindowsPy 的 AppController
│   ├── bind/                   6 个域 struct（Vault/Files/Transfer/Settings/Preview/Lan）+ ContextHolder
│   ├── web/                    HTTP server：/api/* JSON + /api/events SSE + 静态前端 + 10Hz 合帧循环
│   │                           + 同源媒体路由 /s/ /t/ /d/ + 访问闸门 auth.go（局域网档）
│   │                           + 静态资源 gzip 中间件 compress.go（§8.3）
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
| 21 | 跨设备访问 | 无此概念：Qt 窗口只在运行它的那台机器上 | 可选「局域网访问档」：绑 `0.0.0.0` + 访问令牌闸门（回环免令牌，非回环必须带令牌，退出/选目录端点仅本机）；令牌 DPAPI 落盘 `data/lan_token`，**默认关闭** | Web 模式天然可被同网段访问，必须显式开关 + 访问控制，否则等于把密库界面开放给整层楼 |
| 22 | 媒体/下载 URL 形态 | Qt 播放器直连本机代理端口，无 URL 概念 | 相对路径（`/s/ /t/ /d/`），与其余 API 共用同一监听端口与同一道鉴权闸门；**不再另起 127.0.0.1 动态代理端口** | 绝对地址 `http://127.0.0.1:<port>` 在远端浏览器上会指向**远端自己**，局域网档下预览/缩略图/下载会全部失效（详见 §7.3） |
| 23 | 前端产物体积与传输 | 无此概念：Qt 资源编译进二进制，不存在独立前端产物与传输环节 | 图标注册表改**生成式显式 import**（起 176 + 59 个 SVG 共 437KB 全量内联 → 只打包源码引用到的 62 个）；静态资源加 **gzip 中间件**。入口 JS 637KB → 229KB（gzip 76KB），首屏 690KB → 283KB（gzip 87KB） | 局域网档把浏览器变成真正的客户端，首屏要过网线/无线；690KB 未压缩明文在手机弱网下打开明显偏慢（详见 §8） |

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

---

## 7. 局域网访问档（v35）

### 7.1 定位

Web 模式意味着「凡是能连上这个端口的人，都能操作这个密库界面」。默认只绑
`127.0.0.1` 时这只等价于「本机进程可操作」，与历史版本一致；但一旦要让手机 /
另一台电脑访问，就必须补上访问控制，否则等于把端到端加密的密库对整层楼开放。

因此本档的设计原则是：**默认行为零变化，开启是一个显式动作，且开启后必有闸门。**

| 规则 | 说明 |
|---|---|
| 默认不变 | `listen/lan` 默认关，仍绑 `127.0.0.1`，无令牌摩擦 |
| 回环免令牌 | 来源 `127.0.0.1`/`::1` 直接放行 → 本机浏览器、托盘「打开界面」、单实例探测全零改动 |
| 非回环必须带令牌 | 覆盖静态资源、`/api/*`、SSE、媒体 `/s/ /t/ /d/` 全路径 |
| fail-closed | 令牌为空时非回环一律拒绝。即使外部把监听地址误配成 `0.0.0.0`，也不会出现无鉴权对外服务 |
| 本机专属端点 | `/api/app/quit` 与三个原生目录选择端点仅回环可用，远端即使令牌正确也 403 |
| 令牌呈现 | 首次 `?token=xxx` → 种 `HttpOnly; SameSite=Lax` Cookie → 302 跳到去令牌的干净 URL；同时接受 `Authorization: Bearer` |
| 常量时间比较 | 两侧先取 SHA-256 再 `subtle.ConstantTimeCompare`，避免长度差泄露令牌长度 |
| 失败节流 | 同 IP 连续失败 6 次进入 30 秒封禁窗口；回环永不受影响（否则用户会把自己锁在界面外） |

**为什么是「URL 带令牌 + Cookie」而不是账号密码**：本程序没有用户体系，密库
主密钥始终留在主机，远端浏览器只是界面。令牌的职责是「证明这台设备被授权」，
不是身份认证 —— 一次性种 Cookie 后可长期使用，发链接即可分享，重新生成能一键
踢掉所有设备。

### 7.2 令牌存储

| 键 / 文件 | 含义 | 默认 |
|---|---|---|
| `listen/lan`（非秘密） | 是否允许局域网访问 | `"0"`（关） |
| `data/lan_token`（秘密） | 访问令牌密文，`DPAPI:<b64>` / `PLAIN:<b64>` | 首次需要时生成 |

`pkg/secret` 专管「一个文件装一个秘密值」，落盘格式与
`pkg/storage.BaiduCredStore` 同一约定（`DPAPI:` / `PLAIN:` 前缀 + base64），
加解密实现由装配层注入（`pkg/*` 不允许 import `internal/platform/win`）。
**令牌绝不进设置存储** —— `pkg/settings` 顶部明确写着「密码/token 等秘密一律
不入本存储」。令牌为 16 字节 `crypto/rand` → 32 位 hex。

### 7.3 媒体链路：本档最容易翻车的一环

历史坑（务必记住）：`appstate` 曾把流式解密代理**单独起在
`127.0.0.1:<动态端口>`**，并把 `proxy.BaseURL()` 这个**绝对地址**拼在
`entry.URLPath()` 前面返回给前端。本机访问看不出问题，但局域网档下远端浏览器
会去连**它自己**的 `127.0.0.1` → 预览、缩略图、下载全部失效。

现在的做法：

1. `appstate` 的 `ThumbURL/MediaURL/DownloadURL` 一律返回**相对路径**
   （`/s/ /t/ /d/`，`Entry.URLPath()` 本来就给的是相对路径）；
2. `appstate.startProxy` **不再 `Start()` 监听**，只装配 `streaming.Server`
   并把令牌注册表保留在原处（`Revoke` 语义不变）；
3. `internal/web` 的 `registerStream` 在 `/s/ /t/ /d/` 上**每请求**取
   `State.StreamProxy()` 并直接调 `proxy.Handler()`（每请求取一次：连接建立 /
   锁库 / 换连时 `appstate` 会整体替换代理实例，缓存指针会拿到过期对象）；
4. 媒体流量因此与其余 API 共用同一端口、同一道闸门，`pkg/streaming` 顶部
   「代理只监听 127.0.0.1，令牌与解密能力不能暴露给局域网」的约束**天然成立**。

同时删掉了 `web.Server.stream` 字段与死代码 `SetStreaming`（全仓 grep 确认
从未有调用方，`/s/ /t/ /d/` 因此一直 404，流量实际全走代理端口），以及
`appstate.Snapshot.ProxyBase` 与前端同名类型字段。

### 7.4 开关不热重载

`Listen` 在启动时确定绑定地址，切换开关**不会**重新监听。语义上：

- `listen/lan` = 已保存的意愿；`web.Server.LanActive()` = 当前进程真实状态；
- 设置页同时显示两者，不一致时显式提示「需重启程序才会生效」。

这不是「隐藏的无效开关」，而是刻意避免「看似可配但要重启」的展示缺口 ——
把生效时机如实写在卡片上。

**明确非目标：不做端口可配。** 端口可配同样需要重新监听；而 `7840` 起自动
顺延的机制已够用，UI 直接显示**实际监听端口**即可。

### 7.5 已知局限（必须在 UI 如实告知）

- 局域网走**明文 HTTP**，令牌与数据在同一网段内可被嗅探 → 家用 WPA2 可接受，
  公共 Wi-Fi 下不应开启。
- 拿到令牌即等于拿到本程序全部界面能力（含解密下载），因此默认关闭。
- 回环免令牌意味着**本机任何进程**都能操作（与历史版本一致，未扩大也未收窄）。
- 首次绑 `0.0.0.0` 时 Windows 会弹防火墙授权框，拒绝则局域网连不上 ——
  设置页写明了排查步骤。

## 8. 前端资源体积与传输（v36）

### 8.1 两条独立的杠杆

局域网档让浏览器成为真正的客户端，首屏必须经网络传输，于是「产物多大」与
「传多少字节」变成两件必须分别治理的事：

| 杠杆 | 手段 | 效果 |
|---|---|---|
| 产物体积 | 图标注册表按源码引用裁剪（§8.2） | 入口 JS 637KB → 229KB（CSS 不变） |
| 传输字节 | 静态资源 gzip（§8.3） | 首屏明文 690KB → 283KB，再 gzip → 87KB |

合计把局域网首屏从 **690KB 明文**压到 **约 87KB**（约 8 倍）。顺带修掉
`index.html` 的强缓存缺陷（§8.4）。

### 8.2 图标注册表：从全量 glob 到生成式显式 import

`src/lib/icons.ts` 原先用 `import.meta.glob(..., {eager:true})` 把
`assets/fluent-icons`（176 个 / 401KB）与 `assets/lucide`（59 个 / 26KB）
**全部**以 `?raw` 内联进入口 chunk —— 合计 437KB，占 637KB 入口 JS 的 69%，
而源码真正引用的只有 62 个。

现在由 `frontend/scripts/gen-icons.mjs` 生成 `src/lib/icons.gen.ts`：扫描
`src/**/*.{vue,ts}` 的字符串字面量 + `ICONS.xxx` 直接访问，与图标文件名求交，
只对命中的文件 emit 显式 `import`。`npm run build` 的 `prebuild` 会自动先跑一次，
正常无需手动执行（手动：`npm --prefix WindowsGo/frontend run gen:icons`）。

**改这个脚本前必须知道的三个坑：**

1. **三种引号必须各用一条正则独立扫描**，不能写成 `A|B|C` 互斥分支。Vue 模板里
   普遍存在 `:name="reveal ? 'hide' : 'eye'"` 这种「双引号属性内套单引号」的
   写法，互斥分支会先命中双引号并把整段属性一起吃掉，内层 `'eye'` 永远匹配不到。
   实测曾因此把 `eye` / `lock_open` / `mute` / `pause` / `play` 五个**在用**图标
   误判为未引用而裁掉。
2. **`question` 必须强制保留**（脚本里的 `ALWAYS_KEEP`）。它是 `Icon.vue` 的兜底
   图标，以裸标识符 `ICONS.question` 访问，字面量扫描天然扫不到。
3. **扫描必须排除产物 `icons.gen.ts` 自身**，否则上一轮生成的 `import` 会被当成
   「引用」，形成自锁，失效图标永远删不掉。

另设命中数下限断言（`MIN_EXPECTED`）：低于下限直接报错退出且**不写任何产物** ——
宁可不生成，也不能静默丢图标。

**降级行为**：裁剪后若出现未注册的名字，`Icon.vue` 回退成 `question`（布局不塌）
并在 dev 控制台告警，prod 静默（不给终端用户添噪音）。

### 8.3 静态资源 gzip（`internal/web/compress.go`）

Go 侧原先直接 `http.FileServer`，没有任何压缩中间件。现在 `registerStatic`
统一套一层 `withGzip`。

- **只挂静态路由**：SSE / API / 媒体流不经过这里；`text/event-stream` 在
  `compressible` 里另有显式排除（双保险）。
- **压缩决策推迟到 `WriteHeader`**：`Content-Type` 由 `http.FileServer` 自己写，
  只有到那一刻才知道类型。
- **必须删 `Content-Length` 与 `Accept-Ranges`**：前者记录的是压缩前长度，留着会
  让客户端截断或挂起；后者是对字节范围的承诺，压缩后偏移不再对应原始文件，
  必须撤掉这个广告，否则客户端可能按压缩前的偏移续传而拿到错位数据。
- **每个请求 `Close()` 冲刷 gzip 尾部**，否则缺 CRC + 长度，响应会被判定为截断。
- `gzip.Writer` 走 `sync.Pool`，避免每请求分配约 32KB 压缩窗口。

跳过条件：客户端未声明接受 gzip（含 `q=0` 显式拒绝）、带 `Range` 头（断点续传
依赖原始字节语义）、已有 `Content-Encoding`、非 2xx、`Content-Type` 不可压缩
（png / woff2 / mp4 等已自带压缩的格式再压一遍几乎不减小，纯属浪费 CPU）。

### 8.4 `.html` 不再强缓存（回归防护）

原实现对**任何已存在的文件**都贴 `Cache-Control: public, max-age=31536000,
immutable`，`.html` 一并中招。准确的失效路径是：

- `/index.html` 会被 `http.FileServer` **301 重定向到 `./`**（Go 的标准行为），
  这条 **301 响应**于是也带上了 `immutable` —— 浏览器会把它当**永久重定向**缓存。
  用户最终仍能从 `/` 拿到最新前端（`/` 走 no-store 分支），所以不是「拿不到新
  前端」，但 301 被永久缓存本身就是错误的缓存语义。
- 更严重的隐患在**非 index 的 `.html`**：`http.FileServer` 会直接服务、不重定向，
  那类文件就真被钉死一年。

现在 `.html` 一律 `no-store`，只有带内容 hash 的构建产物才 immutable，
`TestRegisterStaticHtmlIsNeverImmutable` 守住这条。


