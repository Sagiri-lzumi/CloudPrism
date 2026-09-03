"""本地文件夹存储后端。

把本地任意目录作为存储根目录，方法直接映射到 pathlib 文件系统读写。
适合单机使用、局域网共享目录与开发调试。本地后端不涉及网络，但加密文件
格式与云端完全一致，可随时把整个目录迁移到其他后端。

与 WebDavBackend 实现同一套 StorageBackend 接口，可无缝互换。
"""

from __future__ import annotations

import os
import shutil
from pathlib import Path
from typing import Iterator

from cloudprism.storage.backend import RemoteEntry


class LocalFolderBackend:
    """本地文件夹存储后端。

    所有相对路径基于 root 解析；root 之外的路径访问会被拒绝。
    """

    def __init__(self, root: str | os.PathLike) -> None:
        self.root = Path(root).resolve()
        if not self.root.exists():
            raise FileNotFoundError(f"后端根目录不存在：{self.root}")
        if not self.root.is_dir():
            raise NotADirectoryError(f"后端根不是目录：{self.root}")

    # ------------------------------------------------------------------
    # 路径解析与安全校验：防止相对路径逃逸到 root 之外
    # ------------------------------------------------------------------

    def _resolve(self, path: str) -> Path:
        """把相对路径解析为 root 内的绝对路径，越界抛 ValueError。"""
        # 规范化相对路径：空路径或 "/" 视为根
        rel = path.strip("/")
        if not rel:
            return self.root
        target = (self.root / rel).resolve()
        # 校验 target 仍在 root 之下（或等于 root）
        try:
            target.relative_to(self.root)
        except ValueError:
            raise ValueError(f"路径越界，禁止访问后端根之外：{path}")
        return target

    # ------------------------------------------------------------------
    # StorageBackend 实现
    # ------------------------------------------------------------------

    def list_dir(self, path: str) -> list[RemoteEntry]:
        """列出目录下条目。"""
        p = self._resolve(path)
        if not p.exists():
            raise FileNotFoundError(f"路径不存在：{path}")
        if not p.is_dir():
            raise NotADirectoryError(f"不是目录：{path}")
        out: list[RemoteEntry] = []
        for child in p.iterdir():
            out.append(
                RemoteEntry(
                    name=child.name,
                    is_dir=child.is_dir(),
                    size=child.stat().st_size if child.is_file() else 0,
                )
            )
        return out

    def get_size(self, path: str) -> int:
        """取文件字节大小。"""
        p = self._resolve(path)
        if not p.exists():
            raise FileNotFoundError(f"路径不存在：{path}")
        if not p.is_file():
            raise IsADirectoryError(f"不是文件：{path}")
        return p.stat().st_size

    def download_range(self, path: str, start: int, end: int) -> bytes:
        """按字节范围 [start, end]（含两端）读取文件段。"""
        if start < 0 or end < start:
            raise ValueError(f"非法范围：[{start}, {end}]")
        p = self._resolve(path)
        if not p.is_file():
            raise FileNotFoundError(f"文件不存在：{path}")
        size = p.stat().st_size
        if start >= size:
            return b""
        # end 限制在文件末尾内
        real_end = min(end, size - 1)
        with p.open("rb") as f:
            f.seek(start)
            return f.read(real_end - start + 1)

    def upload_chunked(
        self,
        local_path: str,
        remote_path: str,
        chunk: int = 1 << 20,
    ) -> Iterator[float]:
        """从本地文件流式写入远端（本地后端即复制），yield 进度。

        支持断点续传：若远端已有部分字节，从该偏移续传。
        """
        src = Path(local_path)
        if not src.is_file():
            raise FileNotFoundError(f"本地源文件不存在：{local_path}")
        dst = self._resolve(remote_path)
        dst.parent.mkdir(parents=True, exist_ok=True)

        total = src.stat().st_size
        # 断点续传基准：远端已存在的字节数
        offset = dst.stat().st_size if dst.exists() else 0
        if offset > total:
            offset = 0       # 远端比源大，异常，重传

        with src.open("rb") as fin, dst.open("r+b" if offset else "wb") as fout:
            fin.seek(offset)
            if offset:
                fout.seek(offset)
            remaining = total - offset
            written = 0
            while remaining > 0:
                buf = fin.read(min(chunk, remaining))
                if not buf:
                    break
                fout.write(buf)
                written += len(buf)
                remaining -= len(buf)
                # 进度 = 已写入（含续传部分）/ 总大小
                yield (offset + written) / total if total else 1.0
        if total == 0:
            yield 1.0

    def head(self, path: str) -> int:
        """取远端文件大小（断点续传基准）。"""
        p = self._resolve(path)
        if not p.exists():
            raise FileNotFoundError(f"路径不存在：{path}")
        if not p.is_file():
            raise IsADirectoryError(f"不是文件：{path}")
        return p.stat().st_size

    def mkdir(self, path: str) -> None:
        """创建目录（含父目录）。"""
        p = self._resolve(path)
        p.mkdir(parents=True, exist_ok=True)

    def rename(self, old: str, new: str) -> None:
        """重命名/移动。"""
        src = self._resolve(old)
        dst = self._resolve(new)
        if not src.exists():
            raise FileNotFoundError(f"源路径不存在：{old}")
        dst.parent.mkdir(parents=True, exist_ok=True)
        shutil.move(str(src), str(dst))

    def delete(self, path: str) -> None:
        """删除文件或目录树。"""
        p = self._resolve(path)
        if not p.exists():
            raise FileNotFoundError(f"路径不存在：{path}")
        if p.is_dir():
            shutil.rmtree(p)
        else:
            p.unlink()

    def exists(self, path: str) -> bool:
        """判断路径是否存在。"""
        return self._resolve(path).exists()
