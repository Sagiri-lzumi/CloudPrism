# CloudPrism

[![Python](https://img.shields.io/badge/Python-3.12%2B-blue.svg)](https://www.python.org/)
[![Platform](https://img.shields.io/badge/Platform-Windows-lightgrey.svg)](https://github.com/Sagiri-lzumi/CloudPrism)
[![License](https://img.shields.io/badge/License-AGPL--3.0-green.svg)](LICENSE)

**CloudPrism** 是一个端到端加密的云盘解决方案：以云端存储为介质，本地设备负责全部加解密——云端只存密文，隐私与安全由本地掌握。

This solution uses cloud storage as the medium, with local devices handling encryption and decryption of cloud data to ensure file privacy and security.

---

## ✨ 核心特性

- **端到端加密**：文件上传前在本地加密、下载后在本地解密，云端（网盘/服务器）仅存密文
- **流式解密播放**：视频/音频无需整文件下载，边解密边播放；图片、文本即点即预览
- **多存储后端**：本地文件夹、WebDAV，以及预留的百度网盘接入（统一 `StorageBackend` 接口，可插拔扩展）
- **可选文件名加密**：密库初始化时可开启，远端目录结构同样不可见
- **快速连接**：最近密库记录（上限 8 条），双击一键重连，仅输一次主密码，免重走初始化向导
- **密码零落盘**：主密码仅存内存、退出清零；任何配置与记录均不持久化密码

## 🔐 安全模型

| 环节 | 设计 |
| --- | --- |
| 密钥派生 | 主密码经 PBKDF2（高迭代 + 随机 Salt）派生，结果用完即弃 |
| 文件加密 | AES-CTR 流式加密，支持任意字节区间的随机访问解密 |
| 完整性 | 密库元信息（Vault Marker）使用 AES-GCM 保护，篡改即校验失败 |
| 主密码 | 仅驻内存，退出时清零；不落盘、不上传 |
| 日志 | 全链路脱敏，不输出密码、密钥与 Salt |

密库以存储后端根目录的隐藏文件 `.cloudprism_vault` 为元信息入口，连接时下载并用主密码校验。

## 🖥️ Windows 客户端

基于 **PySide6** 的 IDE 风格桌面客户端：

- 三栏布局：活动栏 → 侧面板（文件 / 传输 / 密库 / 设置）→ 预览区
- 文件树浏览、上传（支持文件夹）、下载、重命名、删除、拖放
- 传输队列实时进度；密库信息页展示占用、文件数等统计
- 初始化向导：后端类型选择 → 配置与测试连接 → 主密码 → 文件名加密开关

## 📁 项目结构

```
CloudPrism/
├── Windows/          # Windows 桌面客户端（PySide6，已实现）
│   ├── src/cloudprism/
│   │   ├── core/     # 加密、密库管理、会话、设置存储
│   │   ├── storage/  # 存储后端（本地 / WebDAV / 百度网盘）
│   │   ├── gui/      # 界面（主窗口、向导、快速连接、预览等）
│   │   └── app.py    # 组合根与控制器
│   └── tests/        # pytest 测试（323 项）
├── Android/          # Android 客户端（规划中）
└── Plan/             # 设计文档与协议规范
```

## 🚀 快速开始

环境要求：**Windows + Python ≥ 3.12**

```bash
cd Windows

# 安装运行依赖
python -m pip install -r requirements.txt

# 或安装含 GUI 与开发/测试依赖
pip install .[gui,dev]

# 启动客户端（首次启动将进入初始化向导）
cloudprism
```

### 运行测试

```bash
cd Windows
pytest        # 全量测试（323 通过 / 1 跳过）
```

### 打包发布

使用 PyInstaller 双模式打包（spec 已配置图标与资源）：

```bash
# 目录模式：产物含 CloudPrism.exe + _internal
python -m PyInstaller cloudprism.spec

# 单文件模式：产物为独立 CloudPrism.exe
python -m PyInstaller cloudprism_onefile.spec
```

## 🗺️ 路线图

- [x] Windows 客户端：加密/解密、浏览、传输、流式播放、预览
- [x] WebDAV 后端与测试连接验证
- [x] 密库快速连接与最近记录
- [ ] 百度网盘后端真实联调（开放平台凭证申请中）
- [ ] Android 客户端（与 Windows 端共享同一套加密协议）

## 📄 许可证

本项目基于 [AGPL-3.0](LICENSE) 开源。
