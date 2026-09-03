"""统一存储后端接口。

上层只依赖 StorageBackend 接口，不感知具体后端类型（本地文件夹 / WebDAV /
未来云盘 API）。接口只负责传输与存放密文、文件头、目录结构，不接触明文内容
或主密码；密钥派生与加解密只在上层完成。

方法语义两端一致，方法名按 Python 习惯（snake_case）；与 Android 端
StorageBackend（camelCase）一一对应，互操作时按语义对齐。

详见 Plan/Plan.md §2.9 与 Plan/Framework_Windows_Client.md §5.7。
"""

from __future__ import annotations

from typing import Iterator, NamedTuple, Protocol, runtime_checkable


class RemoteEntry(NamedTuple):
    """目录条目（文件或子目录）。"""

    name: str       # 条目名称（云端显示名，可能已加密）
    is_dir: bool    # 是否为目录
    size: int       # 字节大小（目录通常为 0）


@runtime_checkable
class StorageBackend(Protocol):
    """统一存储后端接口。

    所有路径参数均为相对于后端根目录的相对路径（以 / 分隔），不包含协议前缀。
    """

    def list_dir(self, path: str) -> list[RemoteEntry]:
        """列出指定目录下的条目。"""
        ...

    def get_size(self, path: str) -> int:
        """取文件字节大小（断点续传与流式代理总量计算基准）。"""
        ...

    def download_range(self, path: str, start: int, end: int) -> bytes:
        """按字节范围（含两端）拉取密文段。

        参数:
            path: 文件相对路径
            start: 起始字节偏移（含）
            end: 结束字节偏移（含）
        返回:
            [start, end] 区间的字节
        """
        ...

    def upload_chunked(
        self, local_path: str, remote_path: str, chunk: int = ...
    ) -> Iterator[float]:
        """分块写入，yield 进度 0.0~1.0。

        支持断点续传：实现应先检查远端已传字节数（head），从该偏移续传。
        """
        ...

    def head(self, path: str) -> int:
        """取远端文件元信息（大小），断点续传基准。"""
        ...

    def mkdir(self, path: str) -> None:
        """创建目录。"""
        ...

    def rename(self, old: str, new: str) -> None:
        """重命名/移动。"""
        ...

    def delete(self, path: str) -> None:
        """删除文件或空目录。"""
        ...

    def exists(self, path: str) -> bool:
        """判断路径是否存在。"""
        ...
