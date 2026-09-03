"""缩略图获取与缓存（端到端加密友好）。

缩略图来源：`Decryptor.decrypt_range_to_bytes` 取远程图片的头部明文
（AES-CTR 支持随机访问，无需整文件下载）：

  - JPEG 头部即可渐进出图
  - PNG/BMP 等需完整数据，但多数小图 < 512KB 可直接命中
  - 解密失败或数据不足返回 None，调用方静默回退占位图标，不阻塞列表

缓存两级：
  - 内存 LRU（默认 200 条，key = sha256(remote_path)）
  - 磁盘缓存**加密落盘**（AES-GCM，密钥由会话派生）：
    隐私要求缩略图不得以明文形式写入本地磁盘
"""

from __future__ import annotations

import hashlib
import os
import tempfile
from collections import OrderedDict
from typing import TYPE_CHECKING

from Crypto.Cipher import AES

if TYPE_CHECKING:
    from cloudprism.core.session import Session
    from cloudprism.storage.backend import StorageBackend

# 缩略图解密的最大明文字节数（多数预览图足够；超限部分不取）
DEFAULT_MAX_BYTES = 512 * 1024

# 磁盘缓存默认目录（临时目录下独立子目录）
_DEFAULT_CACHE_SUBDIR = "cloudprism_thumbs"

# 缓存密钥派生用固定盐（密钥本体来自会话主密码，盐不敏感）
_CACHE_SALT = hashlib.sha256(b"cloudprism-thumb-cache").digest()[:16]

# GCM nonce 长度
_NONCE_LEN = 12


class ThumbnailCache:
    """缩略图两级缓存（内存 LRU + 加密磁盘）。

    与一次会话绑定：缓存密钥由会话派生，锁库后缓存实例应丢弃。
    """

    def __init__(
        self,
        session: "Session",
        cache_dir: str | None = None,
        max_memory: int = 200,
    ) -> None:
        self._session = session
        self._dir = cache_dir or os.path.join(
            tempfile.gettempdir(), _DEFAULT_CACHE_SUBDIR
        )
        self._mem: OrderedDict[str, bytes] = OrderedDict()
        self._max_memory = max_memory
        self._key: bytes | None = None  # 惰性派生（PBKDF2 较重）

    # ------------------------------------------------------------------
    # 公开接口
    # ------------------------------------------------------------------

    @staticmethod
    def key_for(remote_path: str) -> str:
        """缓存键：远端路径的 sha256 十六进制。"""
        return hashlib.sha256(remote_path.encode("utf-8")).hexdigest()

    def get(self, remote_path: str) -> bytes | None:
        """命中返回缩略图明文（内存优先，其次磁盘解密）；未命中 None。"""
        key = self.key_for(remote_path)
        data = self._mem.get(key)
        if data is not None:
            self._mem.move_to_end(key)
            return data
        data = self._read_disk(key)
        if data is not None:
            self._mem_put(key, data)
        return data

    def put(self, remote_path: str, data: bytes) -> None:
        """写入两级缓存（磁盘为加密形式）。"""
        key = self.key_for(remote_path)
        self._mem_put(key, data)
        self._write_disk(key, data)

    # ------------------------------------------------------------------
    # 内部实现
    # ------------------------------------------------------------------

    def _mem_put(self, key: str, data: bytes) -> None:
        self._mem[key] = data
        self._mem.move_to_end(key)
        while len(self._mem) > self._max_memory:
            self._mem.popitem(last=False)

    def _derive_key(self) -> bytes:
        if self._key is None:
            self._key = self._session.derive_key(_CACHE_SALT)
        return self._key

    def _path_for(self, key: str) -> str:
        return os.path.join(self._dir, f"{key}.cthumb")

    def _write_disk(self, key: str, data: bytes) -> None:
        """AES-GCM 加密后落盘：nonce + tag + 密文。"""
        try:
            os.makedirs(self._dir, exist_ok=True)
            cipher = AES.new(self._derive_key(), AES.MODE_GCM)
            ct, tag = cipher.encrypt_and_digest(data)
            with open(self._path_for(key), "wb") as f:
                f.write(cipher.nonce + tag + ct)
        except Exception:  # noqa: BLE001
            pass  # 缓存写失败不阻断（内存缓存仍生效）

    def _read_disk(self, key: str) -> bytes | None:
        """读磁盘缓存并解密；损坏/篡改/缺失返回 None。"""
        path = self._path_for(key)
        try:
            with open(path, "rb") as f:
                blob = f.read()
            nonce = blob[:_NONCE_LEN]
            tag = blob[_NONCE_LEN:_NONCE_LEN + 16]
            ct = blob[_NONCE_LEN + 16:]
            cipher = AES.new(self._derive_key(), AES.MODE_GCM, nonce=nonce)
            return cipher.decrypt_and_verify(ct, tag)
        except Exception:  # noqa: BLE001
            return None


def fetch_thumbnail(
    session: "Session",
    backend: "StorageBackend",
    remote_path: str,
    max_bytes: int = DEFAULT_MAX_BYTES,
    cache: ThumbnailCache | None = None,
) -> bytes | None:
    """获取远程图片的缩略图数据（头部明文）。

    返回可直接交给 QImage.loadFromData 的字节；解密失败或空数据返回
    None（调用方回退占位图标）。注意：返回数据可能是不完整的图片
    （头部截取），能否出图由调用方以 QImage 解析结果判定。
    """
    if cache is not None:
        hit = cache.get(remote_path)
        if hit is not None:
            return hit

    from cloudprism.core.decryptor import Decryptor

    try:
        data = Decryptor(session, backend).decrypt_range_to_bytes(
            remote_path, start=0, end=max_bytes
        )
    except Exception:  # noqa: BLE001
        return None
    if not data:
        return None

    if cache is not None:
        cache.put(remote_path, data)
    return data
