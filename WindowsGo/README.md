# CloudPrism WindowsGo

CloudPrism 的 Windows 客户端，也是**仓库内唯一实现**：纯 Go 单 exe，内嵌 HTTP
服务与系统托盘，界面在系统默认浏览器中打开。

- **v33 起彻底移除 Wails**：不再需要 wails CLI、不再需要 WebView2 运行时、
  也不再生成 bindings（`frontend/wailsjs/` 已删除）。构建就是一条 `go build`。
- **v38 起移除 Python 参考实现**（原 `WindowsPy/`）：本端成为唯一实现，
  密文格式契约改由 interop 的**冻结黄金向量**锁定，详见文末。

## 技术栈

| 项 | 取值 |
|---|---|
| 语言 | Go（`CGO_ENABLED=0`，无 cgo；依赖仅 `golang.org/x/sys` + `golang.org/x/image`） |
| 界面 | Vue 3 + Vite + TypeScript（零 UI 组件库，Fluent 观感全手写 CSS token） |
| 前端数据层 | `fetch /api/*` + SSE `/api/events`（`frontend/src/lib/api.ts`） |
| 托盘 | `internal/tray`，纯 syscall 实现 |
| 系统对话框 | `internal/platform/win`，裸 COM `IFileOpenDialog`，纯 syscall |
| 产物 | 绿色免安装单 exe，无任何运行时依赖 |

## 构建要求

| 工具 | 版本 | 说明 |
|---|---|---|
| Go | ≥ 1.25（`go.mod` 声明 1.25.0） | `CGO_ENABLED=0`，本机无 gcc，**不依赖 cgo** |
| Node / npm | ≥ 20 | 仅前端构建需要 |

无需 Wails CLI，也无需 WebView2 运行时。

## 构建与验证

```powershell
cd WindowsGo
$env:CGO_ENABLED = "0"
$env:Path = "C:\Program Files\Go\bin;$env:Path"

gofmt -l pkg internal interop          # 必须无输出
go vet ./...                           # 必须 0 告警
go test ./...                          # 全量单测（纯逻辑，不打真实网络）
go build -ldflags "-s -w -H windowsgui" -o build\bin\CloudPrismGo.exe .
```

改了**前端源码**时必须先重建 `frontend/dist`（`//go:embed` 吃它），否则 exe 里跑的还是旧界面：

```powershell
npm --prefix frontend ci               # 首次或依赖变更
npm --prefix frontend run build
```

> 已知取舍：纯 `go build` 不生成 `.syso`，因此 exe **没有图标与版本元数据**
> （`-H windowsgui` 只保证不弹控制台）。要补需另做资源编译，当前未排期。

发布打包（双形态，见 `build/release.ps1`）：

```powershell
powershell -ExecutionPolicy Bypass -File build\release.ps1 -Tag vN
# 产物 Release/<日期>-vN-Go-dir/  （exe + assets + 说明，便携目录版）
#       Release/<日期>-vN-Go-exe/  （仅 exe，单文件版）
```

## 运行

- 双击 exe：托盘常驻，并自动用系统默认浏览器打开界面（默认从 `127.0.0.1:7840`
  起，端口被占用时自动顺延）。
- 全部本地数据随程序目录落在同级 `data/`（设置 / UDF / 缓存 / 日志 / 局域网令牌），
  不写注册表、不写用户目录，拷走即迁移。
- 首次启动进初始化向导：选择存储（本地文件夹 / WebDAV / 百度网盘）→
  配置凭据或授权 → 设主密码 →（可选）开启文件名加密。
- 百度网盘后端需开放平台应用凭证并完成 oob 授权（向导与设置页均可操作，
  教程见随包 `assets/baidu_guide.md` 或仓库 `Plan/百度网盘开放平台接入指南.md`）。
- **局域网访问档**（可选）：设置页开启后，同一网络内的手机/平板可带令牌访问。
  闸门在 `internal/web/auth.go`（回环免令牌、非回环必须带令牌、fail-closed）。
  开关键**不热重载监听**，改完需重启程序。
- **拖放上传**：文件或文件夹拖到窗口任意位置即加密入队；文件夹会递归展开并
  在密库中保留目录结构。

## 目录结构

```
WindowsGo/
├── main.go / app.go          装配（单实例检测 → 状态 → 绑定 → HTTP server → 托盘）
├── build/                    release.ps1 发布脚本
├── frontend/                 Vue 3 前端（src/ 源码；dist 入库，//go:embed 依赖）
│   └── src/lib/api.ts        唯一数据通道封装（fetch + SSE）
├── pkg/                      GUI 无关核心（零平台 import）
│   ├── protocol/             格式常量唯一真源（逐条标注参考实现对照行号）
│   ├── cryptox/              KDF / AES-CTR / GCM(12,16) / 文件头 / 文件名 / Vault
│   ├── session/ vault/       主密码派生缓存 / 密库生命周期与同步索引
│   ├── pipeline/ transfer/   分片加解密流水线 / 传输队列（并发/续传/重试）
│   ├── cache/ secret/        LRU 缓存 / 秘密落盘（DPAPI）
│   ├── thumb/ perf/ settings/ paths/ update/
│   ├── storage/              三后端：local / webdav / baidu（含 DPAPI 凭证）
│   └── streaming/            令牌化流式解密（206/416/MIME 推断）
├── internal/
│   ├── appstate/             唯一有状态对象（连接/锁库/同步/统计/自动锁）
│   ├── bind/                 5 域 API 绑定（Vault/Files/Transfer/Settings/Preview）
│   ├── web/                  前端唯一数据通道（/api/* JSON + /api/events SSE）
│   ├── tray/                 托盘图标与菜单（纯 syscall）
│   ├── platform/win/         唯一 syscall/COM 出口
│   │                         （DPAPI / ShellExecute / 文件对话框 / 进程统计 / 致命框）
│   └── loggingx/             slog + 脱敏 + 2MB×3 轮转
├── interop/                  冻结的跨语言黄金向量（夹具即契约）
└── docs/                     架构活文档 + 手工冒烟清单
```

## 分层约束

改代码前先定位落在哪一层，详见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) 第 2 节：

| 层 | 职责 | 禁止 |
|---|---|---|
| `pkg/*` | GUI 无关核心 | 依赖 `internal/*`、触碰平台 API |
| `internal/platform/win` | 唯一 syscall / COM 出口 | 被 `pkg/*` 依赖 |
| `internal/appstate` | 唯一有状态对象 | 依赖 `internal/bind` |
| `internal/bind` | 只做 error → Code 映射 | 写业务逻辑 |
| `internal/web` | 前端唯一数据通道 | — |

## 密文格式与「夹具即契约」

本端曾是与 Python 参考实现字节级兼容的双实现之一。v38 参考实现移除后，契约形态变了：

- `interop/testdata/` 下的黄金向量由参考实现在移除前用 `interop/gen_vectors.py`
  一次性生成并入库，此后**只读、不可再生** —— **夹具即契约**。
- 若确实需要变更密文格式，必须同步升级 `pkg/protocol` 的版本号并补迁移说明，
  不能再指望「重跑生成器」来对齐。
- `interop/gen_vectors.py` 仍保留在仓库中作为生成逻辑的存档，但它依赖的
  `cloudprism` Python 包已不存在，**无法直接运行**。
- 行为层面的历史差异逐条记录在 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) 第 5 节；
  协议常量一律以 `pkg/protocol` 的对照行号注释为准。

## 已知遗留

- `pkg/perf`（传输速度 / 缓存占用采样）在 v33 Web 化改造后未再接回生产代码，
  仅测试引用；速度指标目前不上屏。需要么重新接回状态帧、么整体移除。
