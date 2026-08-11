"""CloudPrism 端到端加密云盘客户端（Windows 端）。

包结构：
- crypto/   加密原语（KDF、文件头、流加密、文件名加密、Vault Marker）
- storage/   存储后端（后续步骤）
- streaming/ 流式解密代理（后续步骤）
- core/      加解密管线与会话（后续步骤）
- gui/       PySide6 界面（后续步骤）
"""

__version__ = "0.1.0"
