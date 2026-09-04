# CloudPrism WindowsGo（Go 版客户端）

与 `WindowsPy/`（Python 版）**全功能对等**的 Go 重写实现：同一套密库格式
**字节级兼容**（互读互写、产物逐字节相等），UI 视觉高度还原，但改用
Go + Wails v2 + WebView2 技术栈，产物为绿色免安装单 exe。

- 技术栈：Go（无 cgo，`CGO_ENABLED=0`）+ [Wails v2](https://wails.io) + WebView2
- 前端：Vue 3 + Vite + TypeScript（零 UI 组件库，Fluent 观感全手写 CSS token）
- 协议：与 Python 版共享同一套密文/元数据格式，见 [interop 夹具](interop/README.md)

## 构建要求

| 工具 | 版本 | 说明 |
|---|---|---|
| Go | ≥ 1.25 | `CGO_ENABLED=0`（本机无 gcc，**不依赖 cgo**） |
| Wails CLI | v2.15+ | `go install github.com/wailsapp/wails/v2/cmd/wails@latest` |
| Node / npm | ≥ 20 | 前端构建（产物 `frontend/dist` 入库，无 Node 也能 `go build`） |
| WebView2 Runtime | Evergreen | Win11 自带；缺失时 exe 启动自动引导安装 |

## 构建与验证

```powershell
cd WindowsGo
$env:CGO_ENABLED = "0"

go vet ./...                    # 必须无告警
go test ./...                   # 全量单测（纯逻辑，不打真实网络）
wails build -platform windows/amd64   # 产物 build/bin/CloudPrismGo.exe
```

发布打包（双形态，见 build/release.ps1）：

```powershell
powershell -ExecutionPolicy Bypass -File build\release.ps1 -Tag v1
# 产物 Release/<日期>-v1-Go-dir/  （exe + assets + 说明，便携目录版）
#       Release/<日期>-v1-Go-exe/  （仅 exe，单文件版）
```

## 运行

- 双击 exe 即用（GUI 子系统，无控制台窗口）；全部本地数据随程序目录
  落在同级 `data/`（UDF / 设置 / 缓存 / 日志），不写注册表，拷走即迁移。
- 首次启动进初始化向导：选择存储（本地文件夹 / WebDAV / 百度网盘）→
  配置凭据或授权 → 设主密码 →（可选）开启文件名加密。
- 百度网盘后端需开放平台应用凭证并完成 oob 授权（向导与设置页均可操作，
  教程见随包 `assets/baidu_guide.md` 或仓库 `Plan/百度网盘开放平台接入指南.md`）。

## 目录结构

```
WindowsGo/
├── main.go / app.go          装配（探测 WebView2 → 状态 → 绑定 → wails.Run）
├── build/                    图标/版本资源 + release.ps1 发布脚本
├── frontend/                 Vue 3 前端（src/ 源码；dist、wailsjs 入库）
├── pkg/                      GUI 无关核心（零 Wails import）
│   ├── protocol/             格式常量唯一真源（逐条标注 Python 对照行号）
│   ├── cryptox/              KDF / AES-CTR / GCM(12,16) / 文件头 / 文件名 / Vault
│   ├── session/ vault/       主密码派生缓存 / 密库生命周期与同步索引
│   ├── pipeline/ transfer/   分片加解密流水线 / 传输队列（并发/续传/重试）
│   ├── thumb/ perf/ settings/ paths/ update/
│   ├── storage/              三后端：local / webdav / baidu（含 DPAPI 凭证）
│   └── streaming/            令牌化流式解密代理（206/416/MIME 推断）
├── internal/
│   ├── appstate/             唯一有状态对象（连接/锁库/同步/统计/自动锁）
│   ├── bind/                 5 域 Wails 绑定（Vault/Files/Transfer/Settings/Preview）
│   ├── platform/win/         DPAPI/ShellExecute/WebView2 探测 —— 唯一 syscall 出口
│   └── loggingx/             slog + 脱敏 + 2MB×3 轮转
├── interop/                  跨语言黄金向量（Go 解密 Python 产物 == 明文）
└── docs/                     架构活文档 + 手工冒烟清单
```

## 与 Python 端的文件对照（协议相关）

| Go | Python（`WindowsPy/src/cloudprism/`） | 职责 |
|---|---|---|
| `pkg/protocol/constants.go` | `crypto/{header,filename,vault,kdf}.py` 常量 | 格式常量唯一真源（Go 侧聚合） |
| `pkg/cryptox/kdf.go` | `core/kdf.py` | PBKDF2-SHA256 200000 派生 |
| `pkg/cryptox/ctr.go` | `crypto/stream_cipher.py` | AES-256-CTR 随机访问解密 |
| `pkg/cryptox/gcm.go` | `core/encryptor.py`（GCM 段） | GCM 16/12 认证加密 |
| `pkg/cryptox/header.go` | `crypto/header.py` | 文件头加解密 |
| `pkg/cryptox/filename.go` | `crypto/filename.py` | 文件名 Base32 加解密 |
| `pkg/cryptox/vault.go` | `crypto/vault.py` | Vault Marker v3（13 字节内偏移） |
| `pkg/pipeline/*` | `core/{encryptor,decryptor}.py` | 1MiB 对齐分片 + 并行流式 |
| `pkg/vault/{manager,sync}.go` | `core/{vault_manager,transfer_task}.py` 等 | 建/连库、恢复码、同步计划 |
| `pkg/storage/local.go` | `storage/local_backend.py` | 本地文件夹后端 |
| `pkg/storage/webdav.go` | `storage/webdav_backend.py` | WebDAV 后端（自实现分块 PUT） |
| `pkg/storage/baidu/*` | `storage/baidu_backend.py` + `gui/baidu_auth.py` | 百度网盘后端 + 授权 |
| `pkg/transfer/queue.go` | `core/transfer_queue.py` | 并发/续传/进度聚合 |
| `pkg/streaming/*` | `streaming/{proxy_server,range_mapper}.py` | 令牌化解密流（MIME 推断） |
| `pkg/thumb/*` | `core/thumbnailer.py` + `gui/preview_panel.py` | 服务端 192px JPEG 重编码 |
| `internal/appstate/*` | `app.py`（AppController） | 连接状态编排 |
| `frontend/src/views/*` | `gui/{side_panel,main_window,vault_info_page,init_wizard}.py` | 三栏 IDE 布局 + 各页 |

> 行为层面的有意差异逐条记录在 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) 第 5 节；
> 协议常量一律以 `pkg/protocol` 的对照行号注释为准。
