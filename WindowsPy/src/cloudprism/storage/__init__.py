"""存储后端子包：统一 StorageBackend 接口与两类后端实现。

上层（加解密管线、流式代理、目录树）只依赖 StorageBackend 接口，不感知
具体后端类型。当前实现两类后端：
  - LocalFolderBackend：本地文件夹（pathlib）
  - WebDavBackend：WebDAV（requests）

后续接入新云盘 API 时，只需新增一个 StorageBackend 实现，上层零改动。
详见 Plan/Plan.md §2.9 与 Plan/Framework_Windows_Client.md §5.7。
"""
