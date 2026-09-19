# CloudPrism

[![Platform](https://img.shields.io/badge/Platform-Windows-lightgrey.svg)](https://github.com/Sagiri-lzumi/CloudPrism)
[![License](https://img.shields.io/badge/License-AGPL--3.0-green.svg)](LICENSE)

**CloudPrism** 是一个端到端加密的云盘解决方案：以云端存储为介质，本地设备负责全部加解密——云端只存密文，隐私与安全由本地掌握。

This solution uses cloud storage as the medium, with local devices handling encryption and decryption of cloud data to ensure file privacy and security.

---

## 🧩 客户端

仓库只有一个实现：**[WindowsGo](WindowsGo/README.md)** —— 纯 Go 单 exe，
内嵌 HTTP 服务与系统托盘，界面在系统默认浏览器中打开。

| 项 | 取值 |
|---|---|
| 技术栈 | Go（`CGO_ENABLED=0`，无 cgo）+ Vue 3 / Vite / TypeScript |
| 界面形态 | 本地 HTTP 服务 + 默认浏览器（无 Wails、无 WebView2 运行时依赖） |
| 构建 | 一条 `go build`（无需 wails CLI，前端 `dist/` 已入库） |
| 产物 | 绿色免安装单 exe，约 14 MB，无任何运行时依赖 |
| Release 命名 | `<日期>-<tag>-Go-exe`（单文件）/ `-Go-dir`（便携目录版） |

> **历史沿革**：曾存在两套字节级兼容的实现（Go 版 + Python/PySide6 参考实现）。
> **v33 起移除 Wails/WebView2**（改为内嵌 Web 服务 + 浏览器界面），
> **v38 起移除 Python 参考实现**，仓库收敛为单一 Go 实现。密文格式的契约
> 形态也随之改变，改密文相关代码前请务必读
> [WindowsGo/README.md](WindowsGo/README.md#密文格式与夹具即契约) 的
> 「夹具即契约」一节。

## ✨ 核心特性

- **端到端加密**：文件在本地加密后才上传，下载后在本地解密，云端（网盘/服务器）永远只存密文
- **流式播放**：视频、音频无需整文件下载即可边解密边播放；图片、文本即点即预览
- **多存储位置**：支持本地文件夹、WebDAV 服务器与百度网盘（需开放平台应用凭证）
- **可选文件名加密**：初始化密库时可开启，连远端目录结构也完全不可见
- **拖入即加密上传**：文件或文件夹拖到界面任意位置即入队加密；文件夹递归展开，**在密库中保留完整目录结构**
- **局域网访问档**（可选）：设置页开启后，同一网络内的手机/平板可带访问令牌访问界面
- **快速重连**：最近使用的密库会自动记录，双击即可一键重连，只需输入一次主密码
- **绿色便携**：全部设置随程序目录走（`data/` 子目录），不写注册表、不碰用户目录，单文件/免安装版拷到 U 盘即用
- **密码零落盘**：主密码仅存在内存中，退出即清零，任何地方都不会保存你的密码

## 🔐 安全设计

- 主密码经高强度密钥派生算法（PBKDF2 + 随机盐）处理，密钥用完即弃
- 文件采用 AES 流式加密（AES-CTR），支持任意位置的随机访问解密
- 密库元信息经 AES-GCM 认证加密，任何篡改都会立即被发现
- 上传暂存的明文在任务结束后即回收，不留驻磁盘
- 局域网访问采用令牌闸门：回环来源免令牌、非回环必须带令牌，且**取不到令牌时回退为仅本机监听**（fail-closed）
- 日志全程脱敏，不会记录密码、密钥等敏感信息

## 🖥️ 界面一览

仿 IDE 风格的三栏布局 Web 界面（导航轨 + 内容区 + 状态条）：

- **文件**：目录树 / 文件列表 / 预览三栏，支持列表与网格两种视图，可上传（含文件夹）、下载、重命名、删除与拖放
- **传输**：实时查看上传/下载队列与进度，支持续传、重试与清空
- **密库**：查看密库信息、最近连接记录，一键快速重连
- **设置**：外观、缓存、传输、安全（自动锁定、恢复码）、百度网盘凭证、局域网访问等选项

## 🚀 快速开始

1. 从 [Release](https://github.com/Sagiri-lzumi/CloudPrism/tree/main/Release) 下载：
   推荐 `…-Go-exe\CloudPrismGo.exe`（单文件绿色版，拷到 U 盘即用）或 `…-Go-dir`
   便携目录版（附图标与百度网盘接入说明）
2. 双击启动：程序常驻系统托盘，并自动用系统默认浏览器打开界面
   （默认从 `127.0.0.1:7840` 起，端口被占用时自动顺延）
3. 首次启动会进入初始化向导：选择存储位置（本地文件夹 / WebDAV / 百度网盘）→ 测试连接 → 设置主密码 → 选择是否加密文件名
4. 完成后即可浏览、上传、下载密库中的文件，选中即可预览或播放

> ⚠️ 请牢记你的主密码。它不会保存在任何地方；忘记后可凭恢复码找回访问（需在设置页提前生成），否则将**无法找回**密库中的数据。
>
> ⚠️ 文件名加密选项在密库创建后**无法中途修改**，请在初始化时谨慎选择。

## 💾 便携化存储

CloudPrism 的全部本地数据（界面设置、最近密库记录、百度网盘凭证、缓存与日志等）均保存在**程序目录旁的 `data/` 子目录**，不写注册表、不写用户目录：

- 单文件 exe / 绿色免安装版：把 exe 拷到任何位置（如 U 盘），设置都随程序整体迁移；
- 建议将程序解压到可写目录（如桌面、D 盘），若所在目录不可写，应用仍可运行但设置不会持久化；
- 旧版本写入注册表的设置会在首次启动时自动迁移到 `data/`，无需手动操作。

## 🔄 检查更新

目前尚未内置「检查更新」按钮（`pkg/update` 引擎已实现，缺 UI 入口），
请留意 [Release](https://github.com/Sagiri-lzumi/CloudPrism/tree/main/Release) 页发布的新版本。

## 🛠️ 从源码构建

```powershell
cd WindowsGo
$env:CGO_ENABLED = "0"
go build -ldflags "-s -w -H windowsgui" -o build\bin\CloudPrismGo.exe .
```

需要 Go ≥ 1.25（前端 `dist/` 已入库，纯 Go 构建无需 Node）。改了前端源码则需
`npm --prefix frontend run build` 先重建产物。完整说明、分层约束与发布脚本见
[WindowsGo/README.md](WindowsGo/README.md)。

## 🗺️ 路线图

- [x] Windows 客户端：加密浏览、上传下载、流式播放、文件预览、文件夹上传保留目录结构
- [x] WebDAV 后端支持
- [x] 密库快速重连
- [x] 百度网盘后端接入（开放平台应用凭证 + 授权登录）
- [x] 恢复码 / 自动锁定 / 便携化存储
- [x] 移除 Wails/WebView2，改为内嵌 Web 服务 + 浏览器界面（纯 `go build`）
- [x] 局域网访问档（令牌闸门）
- [x] 仓库收敛为单一 Go 实现，移除 Python 参考实现
- [ ] 内置「检查更新」UI 入口（引擎已就绪）
- [ ] 传输速度/缓存指标重新上屏（`pkg/perf` 待接回）
- [ ] Android 客户端（与 Windows 端通用同一套加密格式）

## 📄 许可证

本项目基于 [AGPL-3.0](LICENSE) 开源。
