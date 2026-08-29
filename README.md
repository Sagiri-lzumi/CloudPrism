# CloudPrism

[![Platform](https://img.shields.io/badge/Platform-Windows-lightgrey.svg)](https://github.com/Sagiri-lzumi/CloudPrism)
[![License](https://img.shields.io/badge/License-AGPL--3.0-green.svg)](LICENSE)

**CloudPrism** 是一个端到端加密的云盘解决方案：以云端存储为介质，本地设备负责全部加解密——云端只存密文，隐私与安全由本地掌握。

This solution uses cloud storage as the medium, with local devices handling encryption and decryption of cloud data to ensure file privacy and security.

---

## ✨ 核心特性

- **端到端加密**：文件在本地加密后才上传，下载后在本地解密，云端（网盘/服务器）永远只存密文
- **流式播放**：视频、音频无需整文件下载即可边解密边播放；图片、文本即点即预览
- **多存储位置**：支持本地文件夹、WebDAV 服务器与百度网盘（需开放平台应用凭证）
- **可选文件名加密**：初始化密库时可开启，连远端目录结构也完全不可见
- **快速重连**：最近使用的密库会自动记录，双击即可一键重连，只需输入一次主密码
- **密码零落盘**：主密码仅存在内存中，退出即清零，任何地方都不会保存你的密码

## 🔐 安全设计

- 主密码经高强度密钥派生算法（PBKDF2 + 随机盐）处理，密钥用完即弃
- 文件采用 AES 流式加密，支持任意位置的随机访问解密
- 密库元信息经 AES-GCM 认证加密，任何篡改都会立即被发现
- 日志全程脱敏，不会记录密码、密钥等敏感信息

## 🖥️ 界面一览

仿 IDE 风格的三栏布局桌面界面：

- **文件**：浏览密库中的目录树，支持上传（含文件夹）、下载、重命名、删除与拖放
- **传输**：实时查看上传/下载队列与进度
- **密库**：查看密库信息、最近连接记录，一键快速重连
- **设置**：外观、缓存、传输、安全（自动锁定、恢复码）、百度网盘凭证等选项

## 🚀 快速开始

1. 从 [Release](https://github.com/Sagiri-lzumi/CloudPrism/tree/main/Release) 下载 `CloudPrism.exe` 并运行
2. 首次启动会进入初始化向导：选择存储位置（本地文件夹 / WebDAV / 百度网盘）→ 测试连接 → 设置主密码 → 选择是否加密文件名
3. 完成后即可浏览、上传、下载密库中的文件，选中即可预览或播放

> ⚠️ 请牢记你的主密码。它不会保存在任何地方；忘记后可凭恢复码找回访问（需在设置页提前生成），否则将**无法找回**密库中的数据。
>
> ⚠️ 文件名加密选项在密库创建后**无法中途修改**，请在初始化时谨慎选择。

## 🗺️ 路线图

- [x] Windows 客户端：加密浏览、上传下载、流式播放、文件预览
- [x] WebDAV 后端支持
- [x] 密库快速重连
- [x] 百度网盘后端接入（开放平台应用凭证 + 授权登录）
- [x] 恢复码 / 文件夹同步 / 自动锁定
- [ ] Android 客户端（与 Windows 端通用同一套加密格式）

## 📄 许可证

本项目基于 [AGPL-3.0](LICENSE) 开源。
