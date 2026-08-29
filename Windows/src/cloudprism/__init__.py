"""CloudPrism 端到端加密云盘客户端（Windows 端）。

包结构：
- crypto/    加密原语（KDF、文件头、流加密、文件名加密、Vault Marker）
- storage/   存储后端（本地文件夹 / WebDAV / 百度网盘）
- streaming/ 流式解密代理（边下载边解密播放）
- core/      密库管理、传输管线与会话逻辑
- gui/       PySide6 + Fluent 风格界面
"""

__version__ = "1.0.0"
