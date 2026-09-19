# CloudPrism

[![License](https://img.shields.io/badge/License-AGPL--3.0-green.svg)](LICENSE)

端到端加密的云盘客户端。文件在本地完成加解密，云端只保存密文。

## 组成

仓库包含一个 Windows 客户端（`WindowsGo/`），是一个纯 Go 程序：编译为单个
exe，内嵌本地 HTTP 服务与系统托盘，界面在系统默认浏览器中打开。

| 项 | 取值 |
|---|---|
| 语言 | Go（`CGO_ENABLED=0`，无 cgo 依赖） |
| 界面 | Vue 3 + Vite + TypeScript，构建产物内嵌进 exe |
| 托盘与系统集成 | 纯 syscall 实现，无第三方运行时 |
| 构建 | `go build` |
| 运行依赖 | 无 |
| 发布产物 | 单文件 `CloudPrismGo.exe` |

## 功能

- **上传与下载**：文件在本地加密后上传，下载后在本地解密
- **流式播放与预览**：音视频边解密边播放，图片与文本直接预览
- **存储后端**：本地文件夹、WebDAV、百度网盘（需开放平台应用凭证）
- **文件名加密**：建库时可开启，远端目录结构不可见
- **文件夹上传**：递归展开，在密库中保留目录结构
- **拖放上传**：文件或文件夹拖到界面任意位置即入队
- **局域网访问**：开启后同一网络内的设备可凭访问令牌打开界面
- **快速重连**：记录最近使用的密库，重连只需输入一次主密码
- **便携存储**：设置与数据保存在程序目录旁的 `data/`，不写注册表与用户目录

## 加密

- 主密码经 PBKDF2 加随机盐派生，密钥仅驻留内存，退出即清除
- 文件用 AES-CTR 流式加密，支持从任意位置开始解密
- 密库元信息用 AES-GCM 认证加密，篡改可被发现
- 上传暂存的明文在任务结束后回收
- 局域网访问采用令牌闸门：回环来源免令牌，非回环必须携带令牌，
  且取不到令牌时回退为仅本机监听
- 日志脱敏，不记录密码与密钥

## 使用

从 [Release](https://github.com/Sagiri-lzumi/CloudPrism/tree/main/Release) 取
`CloudPrismGo.exe` 运行。程序常驻系统托盘，并自动打开浏览器指向本地服务
（默认端口 7840，被占用时顺延）。

首次运行进入初始化向导：选择存储位置 → 测试连接 → 设置主密码 → 选择是否
加密文件名。其中两项在建库后不可更改：

- 主密码不落盘。忘记后只能凭恢复码找回访问，恢复码需事先在设置页生成，
  否则密库中的数据无法恢复。
- 文件名加密在建库时确定，之后无法修改。

## 从源码构建

需要 Go ≥ 1.25。前端产物已入库，纯 Go 构建不需要 Node。

```powershell
cd WindowsGo
$env:CGO_ENABLED = "0"
go build -ldflags "-s -w -H windowsgui" -o build\bin\CloudPrismGo.exe .
```

改动前端源码时先用 Node ≥ 20 重建产物：

```powershell
npm --prefix frontend run build
```

发布打包用 `WindowsGo/build/release.ps1`，产出 `Release/` 下的便携目录版与
单文件版。

## 已知限制

- exe 没有图标与版本元数据（纯 `go build` 不生成资源段）
- 「检查更新」引擎已实现，界面暂无入口
- 传输速度与缓存占用指标当前不上屏

## 文档

| 文件 | 内容 |
|---|---|
| `WindowsGo/docs/ARCHITECTURE.md` | 分层原则、目录职责、构建与验证 |
| `WindowsGo/docs/manual_smoke.md` | 手工冒烟清单 |
| `WindowsGo/docs/self_test_guide.md` | 自测指南 |
| `WindowsGo/docs/baidu_guide.md` | 百度网盘开放平台接入步骤 |
| `WindowsGo/interop/README.md` | 密文格式的冻结黄金向量 |

## 许可证

AGPL-3.0，见 [LICENSE](LICENSE)。
