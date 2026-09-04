# CloudPrism

[![Platform](https://img.shields.io/badge/Platform-Windows-lightgrey.svg)](https://github.com/Sagiri-lzumi/CloudPrism)
[![License](https://img.shields.io/badge/License-AGPL--3.0-green.svg)](LICENSE)

**CloudPrism** 是一个端到端加密的云盘解决方案：以云端存储为介质，本地设备负责全部加解密——云端只存密文，隐私与安全由本地掌握。

This solution uses cloud storage as the medium, with local devices handling encryption and decryption of cloud data to ensure file privacy and security.

---

## 🧩 双客户端实现

CloudPrism 有两套功能对等、**密库格式字节级兼容**的 Windows 客户端，可互换
使用、互读同一份密库（协议对照见各自 README）：

| | [WindowsGo/](WindowsGo/README.md)（Go 版，推荐） | [WindowsPy/](WindowsPy/)（Python 版） |
|---|---|---|
| 技术栈 | Go + Wails v2 + WebView2（无 cgo） | Python 3.12 + PySide6 |
| 产物形态 | 绿色便携单 exe，无任何运行时依赖 | 需本机环境或 PyInstaller 打包 |
| Release 命名 | `<日期>-<tag>-Go-exe` / `-Go-dir` | PyInstaller 双形态包 |
| 体积参考 | ~14MB | ~70MB |

## ✨ 核心特性

- **端到端加密**：文件在本地加密后才上传，下载后在本地解密，云端（网盘/服务器）永远只存密文
- **流式播放**：视频、音频无需整文件下载即可边解密边播放；图片、文本即点即预览
- **多存储位置**：支持本地文件夹、WebDAV 服务器与百度网盘（需开放平台应用凭证）
- **可选文件名加密**：初始化密库时可开启，连远端目录结构也完全不可见
- **快速重连**：最近使用的密库会自动记录，双击即可一键重连，只需输入一次主密码
- **绿色便携**：全部设置随程序目录走（`data/` 子目录），不写注册表、不碰用户目录，单文件/免安装版拷到 U 盘即用
- **密码零落盘**：主密码仅存在内存中，退出即清零，任何地方都不会保存你的密码

## 🔐 安全设计

- 主密码经高强度密钥派生算法（PBKDF2 + 随机盐）处理，密钥用完即弃
- 文件采用 AES 流式加密，支持任意位置的随机访问解密
- 密库元信息经 AES-GCM 认证加密，任何篡改都会立即被发现
- 日志全程脱敏，不会记录密码、密钥等敏感信息

## 🖥️ 界面一览

仿 IDE 风格的三栏布局桌面界面（Go 版视觉高度还原 Python 版，两版交互一致）：

- **文件**：浏览密库中的目录树，支持上传（含文件夹）、下载、重命名、删除与拖放
- **传输**：实时查看上传/下载队列与进度
- **密库**：查看密库信息、最近连接记录，一键快速重连
- **设置**：外观、缓存、传输、安全（自动锁定、恢复码）、百度网盘凭证等选项

## 🚀 快速开始

1. 从 [Release](https://github.com/Sagiri-lzumi/CloudPrism/tree/main/Release) 下载**Go 版**：
   推荐 `…-Go-exe\CloudPrismGo.exe`（单文件绿色版，拷到 U 盘即用）或 `…-Go-dir`
   便携目录版（附图标与百度网盘接入说明）；Windows 11 通常已自带 WebView2
   运行时，缺失时程序会自动打开官方下载页引导安装。Python 版同样可用
2. 首次启动会进入初始化向导：选择存储位置（本地文件夹 / WebDAV / 百度网盘）→ 测试连接 → 设置主密码 → 选择是否加密文件名
3. 完成后即可浏览、上传、下载密库中的文件，选中即可预览或播放

> ⚠️ 请牢记你的主密码。它不会保存在任何地方；忘记后可凭恢复码找回访问（需在设置页提前生成），否则将**无法找回**密库中的数据。
>
> ⚠️ 文件名加密选项在密库创建后**无法中途修改**，请在初始化时谨慎选择。

## 💾 便携化存储

CloudPrism 的全部本地数据（界面设置、最近密库记录、百度网盘凭证等）均保存在**程序目录旁的 `data/` 子目录**，不写注册表、不写用户目录：

- 单文件 exe / 绿色免安装版：把 exe 拷到任何位置（如 U 盘），设置都随程序整体迁移；
- 建议将程序解压到可写目录（如桌面、D 盘），若所在目录不可写，应用仍可运行但设置不会持久化；
- 旧版本写入注册表的设置会在首次启动时自动迁移到 `data/`，无需手动操作。

## 🔄 检查更新

**Python 版**：在**设置 → 关于**分组中点击「检查更新」，即可对比当前版本与
GitHub 上最新的 Release 版本；发现新版本时会给出下载页链接。检查为手动触发，
不会在后台自动请求。

> Go 版此项尚未接线（`pkg/update` 引擎已有，缺 UI 入口），新版本请留意
> [Release](https://github.com/Sagiri-lzumi/CloudPrism/tree/main/Release) 页。

## 🗺️ 路线图

- [x] Windows 客户端（Python 版）：加密浏览、上传下载、流式播放、文件预览
- [x] WebDAV 后端支持
- [x] 密库快速重连
- [x] 百度网盘后端接入（开放平台应用凭证 + 授权登录）
- [x] 恢复码 / 文件夹同步 / 自动锁定 / 便携化存储 / 检查更新
- [x] **WindowsGo**（Go + Wails v2 + WebView2）全功能对等重写，密库字节级兼容
- [ ] Android 客户端（与 Windows 端通用同一套加密格式）

## 📄 许可证

本项目基于 [AGPL-3.0](LICENSE) 开源。
