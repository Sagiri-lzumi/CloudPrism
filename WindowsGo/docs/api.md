# CloudPrism 前后端 API 参考（WindowsGo）

> 本文是 **Web 服务模式**下前端与 Go 后端之间的完整接口契约，面向前端重构/重写时的唯一权威参考。
> 所有端点、字段、取值均逐条从源码核对（`internal/web/server.go` / `auth.go` / `internal/bind/*` /
> `internal/appstate/*` / `pkg/streaming/*` / `frontend/src/lib/api.ts` / `store.ts` / `upload.ts` /
> `types/appstate.ts`）。
> 核对基准：git `4eafcd5`（2026-10 更新，覆盖 v1.02 起的变更：POST-only 强制、请求体上限、直读本机路径上传、删除入队、云端分卷、/s/ 类型白名单、TS 模型扩充）。若代码已演进，以本文标注的「改动必须同步处」为准。
> 变更记录：v1.02 将删除改为异步任务、上传拆「拖放 / 直读路径」两条通路、新增 `/api/transfer/scanpaths|uploadpaths`、`/api/fs/dirs` 支持列文件、TaskView 增加条目口径进度、/s/ 增加内联类型白名单。

---

## 0. 文档使用说明与维护纪律

- **本文的读者**：要重构/重写前端的开发者。重构时**只依赖本文 + `frontend/src/types/appstate.ts`**，
  不需要再翻 Go 源码；若发现本文与代码不一致，以本文为准并回写本文。
- **镜像同步约束（项目既有约定，重构时必须保留）**：
  - 后端 JSON 字段 ↔ `frontend/src/types/appstate.ts`：改字段必须两侧同步；
  - 事件名（`st:*`）↔ `frontend/src/lib/events.ts`：改事件名必须两侧同步；
  - 错误码 ↔ `frontend/src/lib/api.ts` 的 `ApiCode`：改错误码必须两侧同步。
- **已知裂缝（截至核对时）**：后端注册了 `/api/transfer/download`，但 `api.ts` 没有封装该方法
  （前端走浏览器下载路径，不使用它）。重构时可顺手补封装或删除后端端点。

---

## 1. 总览与传输约定

### 1.1 架构形态

```
浏览器（Vue 3 + Vite + TS 前端，内嵌于 exe）
  |
  |- POST  /api/*            JSON API（8 个域：vault/app/files/fs/settings/transfer/preview/lan）
  |- GET   /api/events       SSE 事件流（10Hz 状态合帧 + 瞬时事件）
  |- GET   /s/* /t/* /d/*    媒体流 / 缩略图 / 下载（流式解密代理，与 API 同端口同鉴权闸门）
  `- GET   /                 静态前端（内嵌 frontend/dist，SPA 历史路由回退 index.html）
```

- 后端是**纯 HTTP server**（非 Wails/WebView2），默认监听 `127.0.0.1:7840`；端口被占时顺延
  `7840..7849`（`main.go: basePort=7840, maxPortOffset=10`）。实际端口以 `/api/lan/status` 或
  启动日志为准。
- 「局域网访问档」开启时绑 `0.0.0.0` + 访问令牌闸门；关闭（默认）时纯本机。
- 业务状态收敛在 `internal/appstate.State`（唯一有状态对象）；`internal/bind` 只做薄适配
  （错误分类映射）；`internal/web` 是前端唯一数据通道。

### 1.2 传输约定

| 项 | 约定 |
|---|---|
| 方法 | **JSON API 一律 POST，且后端强制校验**（非 POST 返回 `405` + `Allow: POST` + 错误信封，见 §8.5）；SSE 用 GET；媒体流只认 GET/HEAD（其余 `405` + `Allow: GET, HEAD`） |
| 请求体 | `application/json`；无 body 的端点可省略 Content-Type/body（前端统一发 POST + 可选 body） |
| 请求体上限 | JSON 端点上限 **1 MiB**（`wrapJSON` 用 `MaxBytesReader` 截断，超限返回错误）；multipart 上传不受此限 |
| 成功响应 | **裸 JSON 值，无统一信封**：对象/数组/字符串/数字按端点返回；返回 nil 时给 `{"ok":true}` |
| 错误响应 | HTTP **400** + `{"code":"...","message":"中文可读原因"}`（writeJSON 统一输出）；其余状态码见 §8 |
| URL 编码 | 请求字段里出现的远端路径/展示名均为 UTF-8 JSON；返回的媒体 URL 为相对路径（`/s/...` 等） |
| 幂等 | 媒体 URL 签发（mediaurl/thumburl/downloadurl）按「同 kind+同远端路径」幂等；其余端点不保证 |

### 1.3 错误信封与解包

前端统一用 `api.ts` 的 `unwrap()` 把错误还原为带 `code` 的 `ApiError`：

```ts
// HTTP 非 2xx → 尝试解析 body 为 {code, message} → ApiError(code, message)
// 解析失败 → ApiError('internal', 'HTTP ' + status)
```

UI 按 `code` 分流（见 §5 错误码目录）；`message` 为可直接展示的中文。

### 1.4 数据同步模型（重要，重构必读）

- **状态帧驱动 UI**：后端以 **10Hz** 通过 SSE `st:frame` 推送全局快照（`Snapshot`）+ 活动传输任务明细
  （`TaskView[]`）。前端 UI 状态（`ui.snap` / `ui.tasks`）**只由帧驱动**，不轮询 `/api/vault/state`。
- `/api/vault/state` 只用于**页面初始化/下拉刷新**的一次性查询（`store.ts: boot()`）。
- 长操作（向导/恢复码等）后端只发 `st:op-progress` 阶段文案，**没有终态事件**；忙碌态复位由
  调用方 `finally endOp()` + `onFrame` 兜底（快照 `connected` 由 false→true 瞬间自动清）完成。

---

## 2. 路由总表（57 条）

> 权限列：`本机` = 仅回环来源可访问（远端即使令牌正确也 403）；`默认` = 回环免令牌、非回环需令牌。

### 2.1 Vault 域（15）

| # | 路径 | 用途 | 权限 |
|---|---|---|---|
| 1 | `POST /api/vault/state` | 全局状态快照（一次性） | 默认 |
| 2 | `POST /api/vault/open` | 新建/连接密库（create 分支） | 默认 |
| 3 | `POST /api/vault/lock` | 锁定当前密库 | 默认 |
| 4 | `POST /api/vault/recents` | 最近密库记录列表 | 默认 |
| 5 | `POST /api/vault/forget` | 移除一条最近记录 | 默认 |
| 6 | `POST /api/vault/listother` | 同后端其他密库路径 | 默认 |
| 7 | `POST /api/vault/connectother` | 连接同后端另一密库 | 默认 |
| 8 | `POST /api/vault/renamevault` | 重命名当前密库展示名 | 默认 |
| 9 | `POST /api/vault/regenrecovery` | 重新生成恢复码（旧码作废） | 默认 |
| 10 | `POST /api/vault/resume` | 重建未完成任务，返回数量 | 默认 |
| 11 | `POST /api/vault/requeststats` | 触发云端占用统计（异步） | 默认 |
| 12 | `POST /api/vault/baidustatus` | 百度授权状态 | 默认 |
| 13 | `POST /api/vault/baiduauthurl` | 生成百度授权地址 | 默认 |
| 14 | `POST /api/vault/baidusaveauth` | 用 oob code 换 token 落盘 | 默认 |
| 15 | `POST /api/vault/baiduclearauth` | 清除本地百度凭证 | 默认 |

### 2.2 App 域（3）+ 事件（1）

| # | 路径 | 用途 | 权限 |
|---|---|---|---|
| 16 | `POST /api/app/ping` | 单实例探测/心跳（回显 token） | 默认 |
| 17 | `POST /api/app/version` | 运行时版本串 | 默认 |
| 18 | `POST /api/app/quit` | 优雅退出进程 | **本机** |
| 19 | `GET /api/events` | SSE 事件流 | 默认 |

### 2.3 Files 域（5）

| # | 路径 | 用途 | 权限 |
|---|---|---|---|
| 20 | `POST /api/files/list` | 列目录 | 默认 |
| 21 | `POST /api/files/newfolder` | 新建文件夹 | 默认 |
| 22 | `POST /api/files/rename` | 重命名条目 | 默认 |
| 23 | `POST /api/files/delete` | 批量删除（目录递归） | 默认 |
| 24 | `POST /api/files/export` | 解密导出到指定本地目录 | 默认 |

### 2.4 LocalFS 域（3，仅本机）

| # | 路径 | 用途 | 权限 |
|---|---|---|---|
| 25 | `POST /api/fs/drives` | 列盘符（目录选择器「此电脑」视图） | **本机** |
| 26 | `POST /api/fs/dirs` | 列指定目录的子目录 | **本机** |
| 27 | `POST /api/fs/mkdir` | 在指定目录下新建文件夹 | **本机** |

### 2.5 Settings 域（12）

| # | 路径 | 用途 | 权限 |
|---|---|---|---|
| 28 | `POST /api/settings/get` | 聚合返回全部设置 | 默认 |
| 29 | `POST /api/settings/settheme` | 主题档位 | 默认 |
| 30 | `POST /api/settings/setfontsize` | 界面字号 px | 默认 |
| 31 | `POST /api/settings/setcache` | 缓存上限 + 缓存根目录 | 默认 |
| 32 | `POST /api/settings/setchunksize` | 分块大小（=分块阈值） | 默认 |
| 33 | `POST /api/settings/cacheinfo` | 缓存运行时信息 | 默认 |
| 34 | `POST /api/settings/purgecache` | 清空缓存，返回清空后状态 | 默认 |
| 35 | `POST /api/settings/settransfer` | 分块档位 + 并发任务数 | 默认 |
| 36 | `POST /api/settings/setautolock` | 自动锁档位 | 默认 |
| 37 | `POST /api/settings/setmaxcores` | 单任务加密核心数 | 默认 |
| 38 | `POST /api/settings/setsyncdir` | 文件夹同步本地目录 | 默认 |
| 39 | `POST /api/settings/syncnow` | 触发一轮增量同步 | 默认 |

### 2.6 Transfer 域（9）

| # | 路径 | 用途 | 权限 |
|---|---|---|---|
| 40 | `POST /api/transfer/upload` | multipart 拖放上传（入队异步） | 默认 |
| 41 | `POST /api/transfer/scanpaths` | 扫描本机路径生成待上传清单 | **本机** |
| 42 | `POST /api/transfer/uploadpaths` | 直读本机路径入队上传 | **本机** |
| 43 | `POST /api/transfer/download` | 下载选中条目到本地目录（**api.ts 未封装**） | 默认 |
| 44 | `POST /api/transfer/downloadurl` | 签发浏览器下载 URL（/d/） | 默认 |
| 45 | `POST /api/transfer/tasks` | 全部任务明细 | 默认 |
| 46 | `POST /api/transfer/retry` | 重试单个失败任务 | 默认 |
| 47 | `POST /api/transfer/cancelall` | 取消全部在飞/等待任务 | 默认 |
| 48 | `POST /api/transfer/clearfinished` | 清空已完成/已取消任务 | 默认 |

### 2.7 Preview 域（3）

| # | 路径 | 用途 | 权限 |
|---|---|---|---|
| 49 | `POST /api/preview/mediaurl` | 签发媒体流 URL（/s/） | 默认 |
| 50 | `POST /api/preview/thumburl` | 签发缩略图 URL（/t/） | 默认 |
| 51 | `POST /api/preview/revoke` | 吊销一个代理令牌 | 默认 |

### 2.8 Lan 域（3）

| # | 路径 | 用途 | 权限 |
|---|---|---|---|
| 52 | `POST /api/lan/status` | 局域网访问档状态 | 默认 |
| 53 | `POST /api/lan/setenabled` | 写入开关（需重启生效） | 默认 |
| 54 | `POST /api/lan/rotatetoken` | 重新生成令牌（踢掉所有设备） | 默认 |

### 2.9 媒体流路由（3）

| # | 路径 | 用途 | 权限 |
|---|---|---|---|
| 55 | `GET/HEAD /s/{token}/{display}` | 视频/音频流（Range + 流式解密，内联类型白名单见 §7.2） | 默认 |
| 56 | `GET/HEAD /t/{token}` | 缩略图 JPEG | 默认 |
| 57 | `GET/HEAD /d/{token}/{display}` | 全文件下载 | 默认 |

---

## 3. 逐端点参考

> 请求体为 JSON 对象；字段名/类型与 Go struct 的 json 标签一一对应。
> 「响应」给出成功时的裸值形状；错误统一为 §1.3 的错误信封。

### 3.1 Vault 域

#### 3.1.1 `POST /api/vault/state`
- 请求体：无。
- 响应：`Snapshot`（§4.1）。
- 说明：一次性查询；长期订阅请用 SSE `st:frame`。

#### 3.1.2 `POST /api/vault/open`
- 请求体：`OpenRequest`（§4.4）。
- 响应：`OpenVaultResult` = `{"code": string}`。`code` 非空 = **新建成功的一次性恢复码**（只展示一次，不落盘）；连接成功时 `code` 为空串。
- 错误：`bad-password` / `bad-recovery` / `no-vault` / `vault-exists` / `backend` / `timeout` 等。
- 说明：`req.Create=true` 走新建分支（成功后返回恢复码），否则走连接分支。`kind` 为 `local`/`webdav`/`baidu`。

#### 3.1.3 `POST /api/vault/lock`
- 请求体：无。响应：`{"ok":true}`。
- 说明：幂等（未连接时空操作）。打断任务、清缓存、广播 `st:locked` 都在 core 完成。

#### 3.1.4 `POST /api/vault/recents`
- 请求体：无。
- 响应：`Record<string, unknown>[]`（最近在前）。单条记录字段：
  - `backend_type`: `local`/`webdav`/`baidu`
  - `label`: 后端显示名
  - `path`: 后端地址/路径
  - `vault_name`: 密库展示名
  - `vault_path`: 子目录密库根（"" = 根）
  - `webdav_user`: 仅 webdav 且有账号时存在（**密码绝不落盘**）
  - 另有内部键（如 `key`）供 `/api/vault/forget` 使用——前端把整条记录原样回传即可。

#### 3.1.5 `POST /api/vault/forget`
- 请求体：`{"key": string}`（最近记录的唯一键）。响应：`{"ok":true}`。
- 说明：仅删本地记录，不影响云端数据。

#### 3.1.6 `POST /api/vault/listother`
- 请求体：无。响应：`string[]`（同后端下其他密库根路径，"" 表示根目录密库）。
- 错误：`locked`（未连接）。

#### 3.1.7 `POST /api/vault/connectother`
- 请求体：`{"vaultPath": string, "masterPassword": string}`。响应：`{"ok":true}`。
- 错误：`bad-password` / `no-vault` / `backend` 等。

#### 3.1.8 `POST /api/vault/renamevault`
- 请求体：`{"newName": string}`。响应：`{"ok":true}`。
- 说明：只改展示名，不影响加密密钥。

#### 3.1.9 `POST /api/vault/regenrecovery`
- 请求体：`{"masterPassword": string}`。响应：`string`（新的恢复码；旧码立即作废）。

#### 3.1.10 `POST /api/vault/resume`
- 请求体：无。响应：`number`（恢复的任务数）。
- 错误：`no-resume`。

#### 3.1.11 `POST /api/vault/requeststats`
- 请求体：无。响应：`{"ok":true}`。
- 说明：异步触发云端占用统计；结果经后续 `st:frame` 的 `stats*` 字段回填。

#### 3.1.12 `POST /api/vault/baidustatus`
- 请求体：无。
- 响应：`{"authorized": bool, "appKey": string, "appId": string}`（AppKey/AppID 已掩码：超 4 位只留前 4 位 + `…`）。

#### 3.1.13 `POST /api/vault/baiduauthurl`
- 请求体：`{"appID": string, "appKey": string}`（appKey 必填，为空报错）。
- 响应：`string`（百度授权地址；oob 模式，页面展示一次性 code）。
- 说明：后端会尝试打开系统浏览器；失败不阻断，返回 URL 供前端展示/复制兜底。

#### 3.1.14 `POST /api/vault/baidusaveauth`
- 请求体：`{"appID": string, "appKey": string, "secretKey": string, "signKey": string, "code": string}`。
- 响应：`{"ok":true}`。
- 说明：appKey/secretKey/code 必填；code 一次性，失败需重新走 baiduauthurl。授权交换为真实网络请求（60s 超时）。

#### 3.1.15 `POST /api/vault/baiduclearauth`
- 请求体：无。响应：`{"ok":true}`。
- 说明：仅删本地凭证，不影响百度云端数据。

### 3.2 App 域

#### 3.2.1 `POST /api/app/ping`
- 请求体：`{"Token": string}`。响应：`"pong:" + Token`（JSON 字符串）。
- 说明：单实例探测（main.go 用 `{"Token":"probe"}` 探测 7840..7849，应答 `pong:probe` 即认作本程序）。

#### 3.2.2 `POST /api/app/version`
- 请求体：无。响应：`"web-mode"`（JSON 字符串；目前固定值，前端指纹见 app.go 注入的启动日志）。

#### 3.2.3 `POST /api/app/quit`
- 请求体：无。响应：`{"ok":true}`（延迟约 200ms 让本响应先刷新再退出）。
- **权限：仅本机**（远端即使令牌正确也 403）。

### 3.3 Files 域

#### 3.3.1 `POST /api/files/list`
- 请求体：`{"remote": string}`（空串 = 密库根；统一 `/` 分隔）。
- 响应：`FileEntry[]`（§4.2；目录在前、展示名升序，已过滤内部系统文件）。
- 错误：`locked` / `not-found` / `not-dir` / `backend`。

#### 3.3.2 `POST /api/files/newfolder`
- 请求体：`{"parent": string, "name": string}`（parent 空串 = 密库根）。响应：`{"ok":true}`。
- 错误：`locked` / `not-found` / `backend`。

#### 3.3.3 `POST /api/files/rename`
- 请求体：`{"remote": string, "name": string}`。响应：`{"ok":true}`。
- 说明：改名后该条目的媒体/缩略图令牌与缓存自动失效。

#### 3.3.4 `POST /api/files/delete`（v1.02 起异步）
- 请求体：`{"remotes": string[]}`。
- 响应：`{"enqueued": number}`（入队的删除任务数）。
- 说明（v1.02 变更）：删除**改走传输队列异步执行**，不再同步删完才返回。每个**选中项**一个任务（目录内部递归，条目数作进度）；等待/进行/完成/失败/取消五种状态全部在传输任务列表可见，可取消可重试。
- 前端语义：接口只保证「已受理」——提示「已开始删除 N 项」，进度与成败看传输页；当前列表按 remote 集合**乐观摘除**条目，真失败时终态刷新会把条目带回来（见 §9.3）。
- 删除任务的 `remoteDir` = 被删条目的父目录（前端据此判定「删的是不是当前浏览目录」）；删除任务**不写续传记录**（恢复删除没有意义也不安全）。

#### 3.3.5 `POST /api/files/export`
- 请求体：`{"remote": string, "dir": string}`（dir 由网页版目录选择器给出，必填）。
- 响应：`string`（落盘路径）。
- 错误：`internal`（dir 为空）等。

### 3.4 LocalFS 域（仅本机）

> 这三个端点读取/写入**运行程序的这台机器**的本地文件系统，登记为仅回环可用
> （`auth.go: localOnlyPaths`）。安全边界：远端即使令牌正确也 403。

#### 3.4.1 `POST /api/fs/drives`
- 请求体：无。
- 响应：`LocalListing`（§4.8）：`{"path":"", "parent":"", "drives":[...], "dirs":[]}`。

#### 3.4.2 `POST /api/fs/dirs`
- 请求体：`{"path": string, "files": bool}`（path 空串等价于 drives；`files` 缺省 false）。
- 响应：`LocalListing`（§4.8；`drives` 仅在 path 空串时有值；`files` 仅 `files=true` 时有值）。
- 说明：`files=true` 为「上传文件」选择器模式，一并返回本层文件（`LocalFileEntry`，含 size）；目录选择器一律不传。单层最多 2000 项（**两种模式都只按实际返回条目计**，目录模式不因同层文件多而被挤掉目录），超出置 `truncated=true`；隐藏/系统属性条目保留并标记 `hidden`。

#### 3.4.3 `POST /api/fs/mkdir`
- 请求体：`{"parent": string, "name": string}`。
- 响应：`string`（新目录绝对路径；前端拿到后自行进入，省一次往返）。
- 错误：目录名含 `\ / : * ? " < > |`、以句点/空格结尾、已存在等（中文 message）。

### 3.5 Settings 域

#### 3.5.1 `POST /api/settings/get`
- 请求体：无。
- 响应：`Record<string, unknown>`，键（与 Python 版语义对齐）：
  - `themeIndex`: int（0=跟随系统 1=深色 2=浅色，默认 0）
  - `fontSize`: int px（默认 14）
  - `cacheLimitMb`: int（默认 512）
  - `cachePath`: string（默认 "" = 程序目录旁 `data/cache`）
  - `chunkSizeMb`: int（默认 50）
  - `chunkIndex`: int（默认 2；档位 = 云端分卷尺寸：0→4MB / 1→16MB / 2→64MB / 3→256MB，v1.02 起）
  - `concurrent`: int（默认 2）
  - `syncDir`: string（默认 ""）
  - `maxCores`: int（默认 0 = 自动）
  - `autoLockIndex`: int（默认 0 = 从不）
  - `backendType`: string（默认 "local"）
  - `localDir`: string（默认 ""）
  - `webdavUrl`: string（默认 ""）
  - `webdavUser`: string（默认 ""）

#### 3.5.2 `POST /api/settings/settheme`
- 请求体：`{"index": number}`。响应：`{"ok":true}`。

#### 3.5.3 `POST /api/settings/setfontsize`
- 请求体：`{"px": number}`。响应：`{"ok":true}`。

#### 3.5.4 `POST /api/settings/setcache`
- 请求体：`{"limitMB": number, "path": string}`（path 空串 = 默认位置）。
- 响应：`{"ok":true}`。
- 说明：`path` 命中 `pkg/paths.ForbiddenCacheDir`（`%TEMP%`/`%APPDATA%`/Program Files 等系统位置）时直接拒绝并返回可读原因（缓存红线）。

#### 3.5.5 `POST /api/settings/setchunksize`
- 请求体：`{"mb": number}`。响应：`{"ok":true}`。
- 说明：该值同时是「是否分块」的阈值；clamp 到 [MinChunkMB, MaxChunkMB]。对后续新建缓存条目生效。

#### 3.5.6 `POST /api/settings/cacheinfo`
- 请求体：无。响应：`CacheInfo`（§4.7）。

#### 3.5.7 `POST /api/settings/purgecache`
- 请求体：无。响应：`CacheInfo`（§4.7，清空后状态）。

#### 3.5.8 `POST /api/settings/settransfer`
- 请求体：`{"chunkIndex": number, "concurrent": number}`。响应：`{"ok":true}`。
- 说明（v1.02 起）：档位同时决定**云端分卷尺寸**（4/16/64/256 MB），超过该尺寸的文件在远端拆成 `<名>.cpenc.part-1…N` 多个对象，由分卷装饰器折叠回单个逻辑文件（API 层无感知）。改档位只影响后续写入（已有分卷照旧可读）。立即应用到后续任务。

#### 3.5.9 `POST /api/settings/setautolock`
- 请求体：`{"index": number}`（0=从不，1/2/3 = 5/15/30 分钟）。响应：`{"ok":true}`。
- 说明：即时重启自动锁心跳。

#### 3.5.10 `POST /api/settings/setmaxcores`
- 请求体：`{"n": number}`（0=自动，按 CPU 数上限 8）。响应：`{"ok":true}`。

#### 3.5.11 `POST /api/settings/setsyncdir`
- 请求体：`{"dir": string}`（空串 = 清除设置）。响应：`{"ok":true}`。

#### 3.5.12 `POST /api/settings/syncnow`
- 请求体：无。响应：`number`（本批任务数）。
- 错误：`sync-dir-unset` / `busy` / `locked`。
- 说明：批次后台执行，进度经 `st:frame` 的 `snap.sync` 字段推送。

### 3.6 Transfer 域

#### 3.6.1 `POST /api/transfer/upload`（multipart）
- 请求体：`multipart/form-data`，字段：
  - `files`: 文件数组（字段名 `files`；`<input type=file>`/拖放的 File 对象）
  - `paths`: **JSON 数组字符串**，与 files 同序——第 i 项的相对路径（如 `["photos/2026/a.jpg"]`）；不传时退化为按文件名平铺上传
  - `remoteDir`: 目标远端目录（空串 = 密库根）
- 响应：`{"enqueued": number}`（本次实际入队文件数）。
- 说明：
  - 文件先落本地暂存目录（镜像相对路径为嵌套目录），再经 `appstate.UploadPaths` 异步入队加密上传；
  - **暂存回收由任务终态回调负责**，入队失败分支由本函数兜底清理；
  - 相对路径是**不可信输入**：含 `..`、绝对路径、盘符、NUL 的路径使整次上传被拒绝（不做静默降级）；
  - 加密发生在入队后的传输管线（上传 = 加密 → 分块上传），前端「拖入即加密」只是把文件交给队列。

#### 3.6.2 `POST /api/transfer/scanpaths`（仅本机，v1.02 新增）
- 请求体：`{"paths": string[]}`（本机绝对路径，目录或文件；空数组报错）。
- 响应：`UploadPlan`（§4.10）：`{"items": UploadPlanItem[], "totalFiles": number, "totalDirs": number, "totalBytes": number, "truncated": bool}`。
- 说明：**只扫描、不入队**——给用户一份「待上传清单」确认（误选整个盘符的代价太大）；纯本地操作，**未连接密库也能预览**。明细最多 500 条（`scanMaxDetail`），文件总数上限 200000（`scanMaxFiles`，超限置 `truncated=true`，统计不完整必须如实告知）。
- **权限：仅本机**——能读主机任意路径的文件内容，比 /api/fs/* 更硬，远端即使令牌正确也 403。

#### 3.6.3 `POST /api/transfer/uploadpaths`（仅本机，v1.02 新增）
- 请求体：`{"paths": string[], "remoteDir": string}`（paths 空数组报错）。
- 响应：`{"enqueued": number}`。
- 说明：**直读本机路径入队上传**：后端自己打开这些文件，内容完全不经过浏览器，也不在 C 盘留暂存副本。与 scanpaths 同源于 `appstate.expandLocalPaths`，清单与实际传输不会不一致。
- **权限：仅本机**（理由同 scanpaths）。

#### 3.6.4 `POST /api/transfer/download`（api.ts 未封装）
- 请求体：`{"entries": FileEntry[], "localDir": string}`。响应：`{"ok":true}`。
- 说明：把选中条目（目录时整棵下载）下载到指定本地目录。**前端当前未调用**（浏览器下载走 downloadurl）。

#### 3.6.5 `POST /api/transfer/downloadurl`
- 请求体：`{"remote": string, "display": string}`（display 用于响应头文件名）。
- 响应：`{"url": string}`（相对路径，形如 `/d/{token}/{display}`）。
- 错误：`not-found` / `not-vault-file` / `backend` / `locked`。

#### 3.6.6 `POST /api/transfer/tasks`
- 请求体：无。响应：`TaskView[]`（§4.3）。
- 说明：只服务页面初始化与下拉刷新；活动传输的实时明细由 `st:frame` 推送。

#### 3.6.7 `POST /api/transfer/retry`
- 请求体：`{"id": number}`。响应：`{"ok":true}`。
- 错误：`not-found`（任务不存在）等。

#### 3.6.8 `POST /api/transfer/cancelall`
- 请求体：无。响应：`{"ok":true}`。说明：已完成的保留供清理。

#### 3.6.9 `POST /api/transfer/clearfinished`
- 请求体：无。响应：`{"ok":true}`。说明：失败任务需先 Retry 或经此移除。

### 3.7 Preview 域

> 三个端点签发的都是**相对路径**（同源）；媒体流量与 API 共用同一端口与同一道鉴权闸门。

#### 3.7.1 `POST /api/preview/mediaurl`
- 请求体：`{"remote": string, "display": string}`。
- 响应：`string`（`/s/{token}/{display}`）。
- 说明：display 用于 MIME 推断（`<video>` 强依赖）；重复签发同路径幂等。v1.02 起 `/s/` 响应带内联类型白名单（§7.2），白名单外扩展名按附件下载而非内联。

#### 3.7.2 `POST /api/preview/thumburl`
- 请求体：`{"remote": string}`。
- 响应：`string`（`/t/{token}`）。
- 说明：服务端解码→192px JPEG→加密磁盘缓存；生成失败返回错误，前端回退占位图标。

#### 3.7.3 `POST /api/preview/revoke`
- 请求体：`{"token": string}`（URL 路径第 2 段，非整条 URL）。响应：`{"ok":true}`。
- 说明：播放结束/离开预览页时调用，释放注册表容量与派生密钥；锁库自动 RevokeAll。

### 3.8 Lan 域

#### 3.8.1 `POST /api/lan/status`
- 请求体：无。
- 响应：`LanStatus`（§4.6）：`{"enabled": bool, "active": bool, "port": number, "token": string, "localUrl": string, "addrs": LanAddr[]}`。
- 说明：`port<=0`（未监听）时地址字段为空。能调到这里说明调用方已通过闸门，返回 token 不构成新泄露面。

#### 3.8.2 `POST /api/lan/setenabled`
- 请求体：`{"on": boolean}`——**on 为必填**（后端用 `*bool` 区分「缺字段」与「显式 false」，缺字段报错）。
- 响应：`{"ok":true}`。
- 说明：只写设置、**不热重载监听**（需重启生效）；UI 必须同时显示 enabled（已保存）与 active（当前生效）。

#### 3.8.3 `POST /api/lan/rotatetoken`
- 请求体：无。响应：`{"token": string}`（新令牌）。
- 说明：旧令牌立即失效——所有已授权远端设备需用新链接重新进入（「踢掉所有设备」）。

---

## 4. 共享类型字典

> 以下结构体定义在 Go 侧（`internal/appstate/*`、`internal/bind/*`、`internal/platform/win/*`、`pkg/streaming/*`），
> JSON 字段与 `frontend/src/types/appstate.ts` / `api.ts` 的 TS 类型**必须镜像**（改动须两侧同步）。

### 4.1 Snapshot（全局状态快照）

`appstate.Snapshot` —— `POST /api/vault/state` 响应 + SSE `st:frame` 的 `snap` 字段。

| 字段 | 类型 | 含义 |
|---|---|---|
| connected | bool | 是否已连接密库 |
| vaultName | string | 密库展示名（自定义名优先，空则 vault_id 前 8 位 hex + `…`） |
| vaultPath | string | 子目录密库根（"" = 根） |
| backend | string | 后端显示名（本地文件夹 / WebDAV / 百度网盘） |
| backendId | string | 后端地址/路径 |
| filenameEnc | bool | 文件名加密是否开启 |
| hasRecovery | bool | 是否已生成恢复码 |
| connectedSec | number | 已连接秒数 |
| resumeCount | number | 可续传任务数（>0 时 UI 显示续传横幅） |
| autoLockMin | number | 自动锁档位（0=从不） |
| transferActive | bool | 是否有在飞传输或未完成任务（v1.02 起含删除任务：`unfinished > 0 \|\| (total>0 && done<total)`，删除任务字节恒为 0，不能只用字节判） |
| transferDone | number | 已完成字节（删除任务不贡献） |
| transferTotal | number | 总字节（删除任务不贡献；只有删除任务在跑时可为 0） |
| transferTasks | number | 未完成任务数（v1.02 起**恒有值**，含删除任务；此前仅在聚合 total>0 时存在） |
| sync | SyncStatus | 文件夹同步批次状态（§4.5） |
| statsDone | bool | 最新一轮云端占用统计是否已产出 |
| statsTotal | number | 统计总字节 |
| statsFiles | number | 统计文件数 |
| statsFailed | bool | 统计是否失败 |

未连接时 `connected=false`，其余连接相关字段为空/0。

### 4.2 FileEntry（目录条目）

`appstate.FileEntry` —— `POST /api/files/list` 响应元素；`/api/transfer/download` 请求体元素。

| 字段 | 类型 | 含义 |
|---|---|---|
| name | string | 后端原始名（文件名加密开启时为密文，带 `.cpenc` 后缀） |
| display | string | 解密后的展示名（解密失败回退原名） |
| isDir | bool | 是否目录 |
| size | number | 大小（字节） |
| remote | string | 可直接回传操作的完整后端相对路径（**前端不拼路径，一律用此值回传**） |

> v1.02 云端分卷：超过分卷尺寸（默认 64MB）的大文件在远端拆成 `<名>.cpenc.part-1…N` 多个对象，由后端 `storage.Parted` 装饰器折叠回「一个逻辑文件」——目录列表/大小/下载/删除/改名全部无感知，`size` 恒为逻辑大小。

### 4.3 TaskView（传输任务视图）

`appstate.TaskView` —— `POST /api/transfer/tasks` 响应元素；SSE `st:frame` 的 `tasks` 字段元素。

| 字段 | 类型 | 含义 |
|---|---|---|
| id | number | 队列内稳定标识 |
| name | string | 展示名（上传=本地文件名；下载/删除=条目展示名） |
| direction | string | `upload` / `download` / `delete`（v1.02 起含 delete） |
| state | string | `waiting` / `running` / `done` / `failed` / `cancelled` |
| progress | number | 0.0~1.0（删除任务无字节进度，恒为 0） |
| totalBytes | number | 总字节（上传=本地大小；下载=远端大小-密文头估算；**删除恒为 0**） |
| doneBytes | number | 已完成字节（删除任务恒为 0） |
| doneItems | number | 已处理条目数（v1.02 起；**仅删除任务有意义**，传输任务恒为 0） |
| totalItems | number | 总条目数（v1.02 起；删除任务执行过程中为 0——目录条目数事先不可知——成功收尾时后端补上真实值，界面从「已删 N 项」变成「N/N 项」） |
| errorMsg | string | 失败原因（失败时非空） |
| remote | string | 远端路径 |
| remoteDir | string | 上传任务的目标父目录 remote；删除任务 = 被删条目的父目录（前端据此判定「动的是不是当前目录」） |
| local | string | 本地路径 |

终态集合（前端用）：`done` / `failed` / `cancelled`。

### 4.4 OpenRequest / OpenVaultResult

`appstate.OpenRequest` —— `POST /api/vault/open` 请求体。

| 字段 | 类型 | 含义 |
|---|---|---|
| kind | string | `local` / `webdav` / `baidu` |
| localDir | string | kind=local 时的本地目录 |
| url | string | kind=webdav 时的地址 |
| user | string | kind=webdav 时的账号 |
| pass | string | kind=webdav 时的密码 |
| masterPassword | string | 主密码（必填） |
| recoveryCode | string | 忘记密码时凭恢复码开库（可空） |
| filenameEnc | bool | 新建时是否开启文件名加密 |
| vaultName | string | 新建时库名 |
| vaultPath | string | 子目录密库位置（"" = 根） |
| create | bool | true=新建分支，false=连接分支 |

`appstate.OpenVaultResult` —— 响应：`{"code": string}`（新建成功时返回一次性恢复码）。

### 4.5 SyncStatus（同步批次状态）

`appstate.SyncStatus` —— `Snapshot.sync`。

| 字段 | 类型 | 含义 |
|---|---|---|
| running | bool | 本批是否进行中 |
| total | number | 本批应同步文件数 |
| done | number | 已处理（成功+失败） |
| current | string | 正在同步的相对路径 |
| synced | number | 成功上传数 |
| failed | number | 失败条目数 |
| errors | string[] | 失败明细（限前 50 条，防帧体积膨胀） |

### 4.6 LanStatus / LanAddr

`POST /api/lan/status` 响应（`internal/bind/lan.go` 返回的 map，字段名同下表）：

| 字段 | 类型 | 含义 |
|---|---|---|
| enabled | bool | 设置里已保存的开关 |
| active | bool | 当前进程是否真的对局域网监听（切换需重启，可能不一致） |
| port | number | 实际监听端口（0=尚未监听） |
| token | string | 访问令牌（拼接分享链接用） |
| localUrl | string | 本机地址（`http://127.0.0.1:<port>`） |
| addrs | LanAddr[] | 局域网可分享地址（真实网卡在前、虚拟网卡在后） |

`bind.LanAddr`：`{ip: string, iface: string, url: string, virtual: bool}`——
ip 为 IPv4 地址，iface 为网卡名（如「以太网」「WLAN」），url 为**带访问令牌**的完整地址（复制即用），
virtual 为虚拟网卡/隧道标记（手机通常连不上，UI 需标注）。

### 4.7 CacheInfo（缓存运行时信息）

`appstate.CacheInfo` —— `POST /api/settings/cacheinfo` / `purgecache` 响应。

| 字段 | 类型 | 含义 |
|---|---|---|
| dir | string | 实际生效的缓存根目录 |
| scope | string | 当前密库的作用域目录（未连接为空） |
| chunkMb | number | 生效的分块大小（MB） |
| limitMb | number | 容量上限（MB） |
| bytes | number | 已占用字节 |
| entries | number | 条目数 |
| enabled | bool | 分块缓存是否可用 |

### 4.8 LocalListing / LocalDrive / LocalDirEntry（/api/fs/*）

`win.LocalListing`：

| 字段 | 类型 | 含义 |
|---|---|---|
| path | string | 当前目录绝对路径；空串 = 「此电脑」视图（只列盘符） |
| parent | string | 上一级目录；空串 = 已在最上层（前端据此禁用「上一级」） |
| drives | LocalDrive[] | 仅 path 为空串时有值 |
| dirs | LocalDirEntry[] | 全部子目录（含隐藏项，按名称排序） |
| files | LocalFileEntry[] | **仅 fs/dirs 带 files=true 时有值**（v1.02 起；「上传文件」选择器模式），按名称排序 |
| truncated | bool | 单层超过 2000 项已截断（只按实际返回条目计） |

`win.LocalDrive`：`{path: "C:\\", label: string, kind: string}`——kind 为 `fixed`/`removable`/`remote`/`cdrom`/`ramdisk`/`other`。

`win.LocalDirEntry`：`{name: string, path: string, hidden: bool}`——hidden 为隐藏/系统属性，选择器默认折叠。

`win.LocalFileEntry`（v1.02 起）：`{name: string, path: string, size: number, hidden: bool}`——仅在 `fs/dirs` 带 `files=true` 时返回，供「上传文件」选择器使用。

### 4.9 StateFrame（SSE 帧载荷）

`st:frame` 的 data（`frontend/src/lib/events.ts`）：

```ts
interface StateFrame {
  snap: appstate.Snapshot
  tasks?: appstate.TaskView[]   // 仅在「有在飞传输 或 队列非空」时携带（v1.02 起含删除任务）
}
```

### 4.10 UploadPlan / UploadPlanItem（v1.02 新增）

`appstate.UploadPlan` —— `POST /api/transfer/scanpaths` 响应（镜像 `frontend/src/lib/api.ts`）。

| 字段 | 类型 | 含义 |
|---|---|---|
| items | UploadPlanItem[] | 明细（最多 500 条 = `scanMaxDetail`） |
| totalFiles | number | 文件总数（truncated 时为**已扫描到的**数量） |
| totalDirs | number | 目录数 |
| totalBytes | number | 总字节 |
| truncated | bool | 已达扫描上限 200000（`scanMaxFiles`），统计不完整（前端必须如实说明） |

`appstate.UploadPlanItem`：`{rel: string（相对目标目录的逻辑路径，含目录段，POSIX 分隔符）, size: number（字节；取不到时为 0）}`。

---

## 5. 错误码目录

> 错误码定义在 `internal/bind/apierr.go`，前端镜像在 `api.ts` 的 `ApiCode`。
> 所有码都是小写字符串；`message` 为可直接展示的中文。

| code | 含义 | 前端应做的分流 |
|---|---|---|
| locked | 需先连接密库 | 回引导态/密库页 |
| busy | 上一轮独占操作未完成 | 提示稍候，可重试 |
| no-resume | 没有可续传任务 | 隐藏续传入口 |
| sync-dir-unset | 未设置本地同步目录 | 引导去设置页配置 |
| bad-password | 主密码/恢复码身份校验失败 | 提示密码错误，状态回「未连接」 |
| no-vault | 目标位置不存在密库 | 提示先新建密库 |
| vault-exists | 目标位置已有密库 | 提示换位置或改连接模式 |
| bad-recovery | 恢复码无效 | 提示重新核对（注意 O/0、I/1） |
| not-found | 远端路径不存在 | 刷新列表并提示 |
| not-dir | 目标是文件而非目录 | 按文件语义处理 |
| is-dir | 目标是目录而非文件 | 按目录语义处理 |
| range | 非法字节范围（内部错误） | 兜底重试 |
| not-vault-file | 不是合法的加密容器 | 提示文件损坏/非本程序文件 |
| backend | 后端故障（可重试） | 提示可稍后重试 |
| timeout | 网络操作超时 | 提示超时，可重试 |
| internal | 其余未分类错误 | 展示 message |

---

## 6. SSE 事件协议

### 6.1 连接与帧节奏

- 端点：`GET /api/events`（`text/event-stream`，`Cache-Control: no-cache`）。
- **连接即发首帧** `st:frame`（真实快照，不是空帧——前端对每帧 JSON.parse，空 data 会抛异常触发 boot-err）。
- 之后以 **10Hz**（每 100ms）推送 `st:frame`；事件通道缓冲 64 条，客户端慢时**丢弃**（帧丢一帧无碍）。
- 事件名即 SSE 的 `event:` 字段，`data:` 为 JSON 序列化值。

### 6.2 事件清单

| 事件名 | 载荷 | 触发与语义 |
|---|---|---|
| `st:frame` | `StateFrame`（§4.9） | 10Hz 状态合帧：全局快照 + 活动传输任务明细 |
| `st:op-progress` | string | 长操作（向导/恢复码/同步等）阶段文案；**后端不发射终态事件** |
| `st:op-done` | string | 长操作结束（当前后端不发射，前端保留防御性处理器） |
| `st:op-error` | string | 长操作失败（当前后端不发射，语义同 endOp） |
| `st:locked` | null | 自动/手动锁库广播；前端立即回引导态（`ui.page='vaults'`） |

### 6.3 前端订阅要点（重构必读）

- 用 `EventSource('/api/events')`；对每个事件 `JSON.parse(ev.data)` 前先做空值守卫（坏帧丢弃，防 boot-err 红屏）。
- `st:frame` 是 UI 状态唯一权威来源：`ui.snap`/`ui.tasks` 只由帧写入。
- 忙碌态复位约定：`st:op-progress` 置 `opBusy`；复位走调用方 `finally endOp()` + `onFrame` 兜底
  （快照 `connected` 由 false→true 瞬间自动清），**不依赖终态事件**。
- 上传完成即刷新：前端按「任务在本帧刚进入终态」判定（与上一帧同 id 状态比较），
  而非等整队列 active→idle——批量上传时先传完的文件不会被压住。

---

## 7. 媒体 / 流式路由（/s/ /t/ /d/）

### 7.1 URL 形态

| 端点 | 形态 | 用途 |
|---|---|---|
| /s/ | `/s/{token}/{display-name}` | 视频/音频流（Range + 流式解密） |
| /t/ | `/t/{token}` | 缩略图 JPEG（注册时注入） |
| /d/ | `/d/{token}/{display-name}` | 全文件下载（`<a download>`） |

- `token`：32 位十六进制（16 字节 crypto/rand），仅注册方（后端）与拿到 URL 的前端可见；
  display-name 参与路径只为可读性（复制链接/书签），**服务端按 token 找 entry，尾巴不参与判定**。
- 全部响应 `Cache-Control: no-store`（Chromium 会把 206 片段拼入磁盘缓存——解密明文绝不能落盘）。
- 仅认 `GET`/`HEAD`（其余方法 `405` + `Allow: GET, HEAD`）；令牌不存在/类型不匹配 `404`。
- v1.02：`/s/` 响应加**内联类型白名单**（§7.2）；远端内容变化（上传/重命名/删除写路径）后后端自动吊销该路径全部注册（`RevokeRemote`，§7.5）。

### 7.2 /s/（媒体流）

- `HEAD`：`200` + `Content-Length=明文总量`（播放器探时长用）。
- 空文件：`206` + `Content-Range: bytes 0-0/0`。
- 正常：`206` + `Content-Range`，按 Range 逐 256KiB 窗口边解边发；受单次响应上限截断，播放器按续请拼接。
- `Range` 越界：`416` + `Content-Range: bytes */total`（不回退整文件）。
- 首窗口失败：`502`（响应头未发）；中途窗口失败：断流（播放器感知 Content-Length 不足自动重试）。
- `Content-Type` 按**展示名扩展名**推断（`MIMEForDisplayName`）——Chromium `<video>` 强依赖 MIME。
- **v1.02 内联类型白名单**（`StreamContentType`）：只有 `mimeByExt` 表内**纯媒体/文档**类型（视频/音频/位图/PDF/纯文本/JSON，含 avif/heic/heif/tif/tiff/ico/csv 等新增项）内联渲染；白名单外的扩展名（`.html`/`.svg`/`.xml` 与所有未知类型）一律降级为 `application/octet-stream` + `Content-Disposition: attachment`——否则密库里的文件会在应用源上被当页面执行（存储型 XSS：回环来源免令牌，脚本可直接调 /api/*）。
- v1.02 起**不再回退系统注册表**（原 `mime.TypeByExtension` 兜底已移除）：未知扩展名恒定 `application/octet-stream`，类型推断不再依赖用户机器上装了什么软件。

### 7.3 /t/（缩略图）

- `200` + JPEG，`Content-Length` 固定，一次写出（5-15KB，无流式必要）。

### 7.4 /d/（下载）

- **忽略 Range、不截断**（`<a download>` 需要完整文件）；`200` + `Content-Length=明文总长`。
- `Content-Type: application/octet-stream`。
- `Content-Disposition: attachment`，双轨文件名：`filename`（ASCII 安全回退名）+ `filename*`（RFC 5987 UTF-8 原名，中文保留）。

### 7.5 令牌生命周期

- 注册：媒体/缩略图/下载 URL 签发时注册 entry（同 kind+同远端路径幂等，重复注册返回既有令牌）。
- 吊销：`POST /api/preview/revoke {token}`（前端从 URL 路径第 2 段取 token）；播放器关闭/离开预览页调用。
- 锁库：注册表整体 RevokeAll（派生密钥一并清零）。
- **v1.02 写路径自动吊销**（`RevokeRemote` → `RevokePath`）：上传/重命名/删除完成后，后端吊销该远端路径上的全部注册（三种 Kind 一并扫）——幂等注册冻结了注册时刻的文件头/密钥/大小，覆盖后旧 Entry 会解出乱码，必须让下次预览重新注册。
- 注册表容量上限 4096；触及上限返回 `too-many-tokens`（调用方忘记回收所致）。
- 幂等前提是「注册后文件内容不变」——远端文件被覆盖后需先 Revoke 旧令牌再注册。

---

## 8. 鉴权与安全边界

> 访问闸门实现在 `internal/web/auth.go`。设计前提：本程序没有用户体系——令牌的职责是「证明这台设备被授权」，不是身份认证。

### 8.1 四条规则（按请求处理顺序）

| # | 规则 | 失败响应 |
|---|---|---|
| 0a | **Host 白名单**：只认 `127.0.0.1`/`localhost`/`[::1]`（局域网档另含本机全部接口地址）且端口=实际监听端口——防 DNS rebinding | `403` |
| 0b | **Origin 校验**（仅非 GET/HEAD/OPTIONS）：存在 Origin 时须与 Host 同口径；`null`/畸形一律拒绝；缺失放行（curl/托盘等非浏览器客户端）——防 CSRF 盲打 | `403` |
| 1 | **回环来源永远免令牌**（本机浏览器/托盘/单实例探测零摩擦） | — |
| 2 | **非回环必须带令牌**；令牌为空时非回环一律拒绝（fail-closed） | `401` 提示页 |
| 3 | **本机专属端点**：`/api/app/quit`、`/api/fs/drives|dirs|mkdir`、`/api/transfer/scanpaths|uploadpaths`（v1.02 起），远端即使令牌正确也拒绝。后两者能读主机任意路径的**文件内容**，是本程序最硬的一道边界（放开 = 远程文件窃取） | `403`（API 路径回 JSON） |

### 8.2 令牌呈现

- 首次访问 `?token=xxx`：校验通过 → 种 Cookie `cp_lan_token`（`HttpOnly; SameSite=Lax; Max-Age=30d`）→ `302` 跳转到去掉令牌的干净 URL（令牌不留书签/历史）。
- 已授权设备：Cookie **或** `Authorization: Bearer <token>` 任一即可。
- 比较：两侧先 SHA-256 再 `subtle.ConstantTimeCompare`（防长度泄露）。
- 无令牌访问非回环：`401` + 可读中文提示页（含去设置取链接的指引），非裸 401。

### 8.3 失败节流

- 同 IP 连续失败 6 次 → 30 秒封禁窗口（`429` + `Retry-After: 30`）；封禁后重新计数。
- 回环永不受影响（否则用户会把自己锁在界面外）。
- 失败记录 TTL 10 分钟、表上限 2048（防伪造 IP 撑爆）。

### 8.4 已知局限（UI 必须如实告知）

- 局域网走**明文 HTTP**：令牌与数据同网段可被嗅探（家用 WPA2 可接受，公共 Wi-Fi 不应开启）。
- 拿到令牌 = 拿到全部界面能力（含解密下载），默认关闭。
- 回环免令牌 = 本机任何进程都能操作（与历史一致）。
- 首次绑 `0.0.0.0` Windows 弹防火墙授权框，拒绝则局域网连不上。

### 8.5 方法强制（v1.02）

- 所有 `/api/*` JSON 端点**只接受 POST**（`wrapErr`/`wrapJSON` 内统一校验）：非 POST 返回 `405` + `Allow: POST` + 错误信封（`{code:"internal", message:"该接口只接受 POST 请求"}`）。
- 动机：状态变更端点若接受 GET，就绕过了 `originAllowed` 的跨源校验（该校验只覆盖非 GET），而跨源 GET 是「简单请求」——不带 Origin、无需预检，本机浏览器里任意网页都能盲打 `/api/vault/lock`、`/api/app/quit`、`/api/settings/purgecache` 等。
- 影响：curl/脚本调用者必须用 POST（`main.go` 的单实例探测已同步改 POST）；媒体流 `/s/ /t/ /d/` 只认 GET/HEAD（其余 405），SSE 为 GET。

---

## 9. 前端集成地图（重构指引）

### 9.1 模块结构（frontend/src）

```
src/
|- main.ts                入口：createApp + 样式顺序 + initTheme
|- App.vue                根组件：start()/stop() 事件订阅、NavRail、全局 FolderPicker/恢复码模态
|- types/appstate.ts      与 Go 镜像的 TS 模型（字段改动须两侧同步）
|- lib/
|  |- api.ts              ★唯一 API 封装层（8 个域 + ApiCode + unwrap）
|  |- events.ts           事件名常量 + StateFrame 载荷类型
|  |- store.ts            ★唯一响应式全局状态 + 全部动作封装（901 行）
|  |- upload.ts           拖放/文件选择 → {File, rel} 列表
|  |- theme.ts / toast.ts / modalStack.ts / format.ts / media.ts / docBlob.ts / videoCover.ts / icons.ts
|- views/                 FilesView / TransfersView / VaultsView / SettingsView / PreviewPanel / GridCard
|- components/            fluent/*（仿 Fluent 控件）、layout/*（FolderPicker/MediaPlayer/ModalShell/...）、DirTree.vue
|- styles/                theme.css(token) → base.css → components.css → layout.css
```

### 9.2 api.ts 职责（重构时保持的边界）

- 视图层**只从 store.ts 取动作**，store 只从 api.ts 取 HTTP 封装；任何视图不得直接碰 fetch。
- `call()` 统一：POST + JSON body → 成功解包裸值 / 失败 `unwrap()` 成 `ApiError`。
- `ApiCode` 镜像 `apierr.go` 的 16 个错误码；`isApiCode(err, code)` 做快速分类。
- 域命名：`Vault` / `Files` / `LocalFS` / `Settings` / `Transfer` / `Preview` / `Lan` / `App`。
- 上传例外：拖放通路 `Transfer.Upload` 走 `FormData`（files + paths JSON + remoteDir），不经过 `call()`。
- v1.02 新增：`Files.Delete` 返回 `{enqueued}`（异步）；`LocalFS.ListDir(path, includeFiles?)`（`includeFiles` 只有「上传文件」选择器传 true）；`Transfer.ScanPaths(paths)` → `UploadPlan`、`Transfer.UploadPaths(paths, remoteDir)` → `{enqueued}`（直读本机路径通路）；新 TS 类型 `LocalFileEntry` / `UploadPlan` / `UploadPlanItem`。

### 9.3 store.ts 数据流与关键模式（重构必读）

- **单例响应式状态**：`export const ui = reactive<Ui>(...)`；`App.vue` `onMounted(start)` / `onBeforeUnmount(stop)`。
- **帧驱动**：`start()` 订阅 5 个 SSE 事件；`onFrame` 写 `ui.snap`/`ui.tasks`，做连接边界处理（断开清浏览态/占位、锁库强制回密库页）。
- **上传占位（乐观 UI）**：发请求**之前**挂 `UploadPlaceholder`，`onFrame` 的任务对账（按 name+remoteDir 匹配）逐步换成真实进度、终态撤下；超时 30 分钟安全阀。
- **两条上传通路（v1.02 分岔）**：
  - 拖放 → `uploadFiles()` → `Transfer.Upload`（multipart，浏览器经手）；
  - 按钮（上传文件/导入文件夹）→ `pickLocalFiles`/`pickLocalDir` 拿到**绝对路径** → `requestUploadByPaths()`（先 `Transfer.ScanPaths` 扫描）→ 弹 `UploadPlan` 确认框（App 挂载 `UploadPlan.vue`）→ `confirmUploadByPaths()`（`Transfer.UploadPaths` 入队）。扫描/确认全程在 `uploadPlan` reactive 里；入队失败会撤掉占位。
- **列目录代际防护**：`listDir` 每次 `ui.seq++`，旧目录的迟到响应按 seq 丢弃；`silent` 模式只在条目集合变化时写回（防闪烁）。
- **空闲轮询**：仅「文件页 + 已连接 + 无在飞传输 + 距手动刷新 ≥1s」时每 5s 静默刷一次。
- **忙碌态（op）**：`st:op-progress` 置文案+忙碌；复位 = 调用方 `finally endOp()` + 帧兜底；`isBusy()` 供视图禁用操作。
- **全局目录/文件选择器**（v1.02 扩展）：`pickLocalDir()` / `pickLocalFiles()`（Promise）由 App 挂载的 FolderPicker 兑现；`localPick.mode` = `'dir' | 'files'`，`settleLocalPick({dir?, files?})` 结清。调用点：向导/设置（同步目录+缓存目录）/密库页/文件页（导出、上传文件、导入文件夹）。全局单例复用 ModalShell 模态栈。
- **下载/导出**：单条导出 → `Files.Export`（先 `pickLocalDir` 选目录）；下载与多选导出 → `Transfer.DownloadURL` + `triggerDownload`（动态 `<a download>`）。目录不支持下载（提示）。
- **删除（v1.02 异步）**：`deleteSel()` 调 `Files.Delete` 拿 `enqueued` → **乐观摘除**当前列表中被删 remote → 提示「已开始删除 N 项，可在传输页查看进度」；`onFrame` 在删除任务刚进入终态时触发 `refreshAfterDelete`（判定口径与上传相反：等条目**消失**，0/0.8s/2s 有界重试；失败/取消时条目会带回来，与传输页失败任务对照看）。
- **本地持久化**：仅 `localStorage` 记住上次导出目录（`cp-export-dir`）；其余状态全部来自后端。

### 9.4 upload.ts 上传管线（重构必读的浏览器坑）

- 目的：把「拖放/文件选择」还原为「文件 + 相对路径」，因为浏览器原生 API 会丢目录信息：
  - `dataTransfer.files` 对文件夹只给 0 字节占位 File（内部文件拿不到）→ 必须用 `DataTransferItem.webkitGetAsEntry()` 递归；
  - `<input type=file multiple>` 的 `File.name` 只有文件名，目录结构在 `File.webkitRelativePath`（仅 webkitdirectory 会填）。
- 收集顺序即后端 multipart 的 files 顺序；`paths[i]` 是第 i 个文件的相对路径（POSIX 分隔符）。
- **`readEntries` 一次最多返回 100 项，必须循环调用直到空**（大目录静默截断是最易漏的一处）。
- 单次拖放上限 5000 项（防误拖整个盘符卡死浏览器）。
- **v1.02 分工**：本文件只服务**拖放通路**（浏览器只给 File 对象，没有绝对路径）。「上传文件/导入文件夹」按钮走直读本机路径（`requestUploadByPaths` → `/api/transfer/scanpaths|uploadpaths`），收集到的是一串**绝对路径**，不经本文件。
- 目录递归失败（权限/被删）时跳过单文件、不整体失败；读失败时返回已收集部分。

### 9.5 视图 ↔ store 动作映射

| 视图 | 主要动作（store） | 后端端点 |
|---|---|---|
| VaultsView（密库页） | openVault / lockVault / Vault.State / recents / listother / connectother / renamevault / regenrecovery / resume / requeststats / baidu* | vault/* |
| FilesView（文件页） | listDir / enterDir / crumbTo / goUp / uploadFiles（拖放）/ pickUploadFiles / pickUploadFolder（按钮·直读）/ requestUploadByPaths / downloadSel / exportSel / newFolder / renameSel / deleteSel / mediaUrl / thumbUrl / revoke | files/* + transfer/upload + transfer/scanpaths|uploadpaths + transfer/downloadurl + preview/* |
| GridCard / PreviewPanel | mediaUrl / revoke / thumbUrl / videoCover | preview/* + /s/ /t/ |
| TransfersView（传输页） | Transfer.Tasks / Retry / CancelAll / ClearFinished + 帧 tasks（**含删除任务**，按 doneItems/totalItems 渲染） | transfer/tasks + retry/cancelall/clearfinished |
| SettingsView（设置页） | Settings.Get / Set* / CacheInfo / PurgeCache / SyncNow / pickLocalDir / Lan.Status / SetEnabled / RotateToken | settings/* + lan/* |
| App.vue / NavRail | start / stop / quitApp / clearRecovery / pickLocalDir | app/quit + /api/events |

### 9.6 重构时的高风险点（来自历史坑）

1. **事件缺失守卫**：SSE 载荷先空值检查再 JSON.parse（坏帧丢弃），否则空帧触发全局 unhandledrejection → boot-err 红屏。
2. **锁库/断开**：`st:locked` 强制回密库页并清浏览态；断开帧清上传占位（任务没了对账依据）。
3. **上传完成即刷新**：按「任务刚进入终态」判定 + 0.8s/2s 有界重试（网盘列表索引滞后），**不要**定时轮询等待整队列 idle。
4. **多选语义**：`sel` = 最后点击的主条目，`multi` 长度 >1 时工具栏/右键呈批量态；remote 路径是唯一键。
5. **明文链 vs 密文路径**：面包屑/展示用 `display`，回传操作一律用 `remote`（加密路径），**前端不拼路径**。
6. **op 忙碌态**：不要等 `st:op-done`（后端不发射）；用 finally endOp + 帧兜底。
7. **代理 URL 吊销**：`revoke` 传 token（URL 第 2 段），不是整条 URL。
8. **`/api/transfer/download` 未封装**：重构时决定补封装或删端点，但不要在前端直接 fetch 未封装的路径。
9. **删除是异步的**（v1.02）：`Files.Delete` 只返回 `enqueued`，删除进度/成败看传输任务；提交成功后的乐观摘除是「已受理」语义——真失败由终态刷新带回，不要显示「已删除」而要说「已开始删除」。
10. **两条上传通路不可混用**：拖放只能走 multipart（无绝对路径）；按钮必须走 scanpaths/uploadpaths（仅本机端点，远端 403）。
11. **方法必须是 POST**（v1.02 起后端强制）：任何 GET 调用 `/api/*` 都会 405；单实例探测（ping）也已是 POST。
12. **`/s/` 白名单语义**：非白名单类型（html/svg/xml/未知）会被强制当附件下载，预览页不要假设任意扩展名都能内联播放。

---

## 10. 契约守卫建议（可选实施）

为防「镜像同步」退化成裂缝（已有一例：`/api/transfer/download` 未封装），建议：

1. **路由清单守卫测试**（Go）：断言「前端 `api.ts` 中引用的路径 ⊆ 后端注册路由」，加在 `internal/web`（v1.02 已有 `api_method_test.go` 守住 POST-only，路由覆盖守卫尚未实现）。
2. **类型镜像测试**（Go + 前端构建）：`vue-tsc --noEmit` 已保证前端内部一致；后端侧加测试断言 Snapshot/TaskView 的 json 标签与 appstate.ts 字段名一致（字符串比对）。
3. **文档↔代码一致性**：本文件的路由总表可由脚本从 `server.go` 的 HandleFunc 提取比对（CI 可选）。

---

*本文档由源码逐条核对生成；改动后端路由/字段时请同步更新本文与前端类型。*