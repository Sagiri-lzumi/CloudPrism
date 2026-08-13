"""下载解密管线。

Decryptor 负责从存储后端下载 .cpenc 容器并解密为本地明文文件：

    1. 从后端下载文件头（前 64 字节足够覆盖 51B 头）
    2. 解析头获取 salt / iv / header_length
    3. 用 Session 派生密钥
    4. 分块下载密文主体 -> AES-CTR 解密 -> 写本地明文

流式解密无填充，明文长度 = 密文长度 = 文件大小 - header_length。
"""

from __future__ import annotations

import os
from io import BytesIO
from typing import Iterator

from Crypto.Cipher import AES

from cloudprism import constants
from cloudprism.core.session import Session
from cloudprism.crypto.header import FileHeader
from cloudprism.storage.backend import StorageBackend


class Decryptor:
    """解密器：下载 .cpenc 容器 -> 解密 -> 写本地明文。"""

    # 默认分块大小（字节）：1 MiB
    DEFAULT_CHUNK: int = 1 << 20

    def __init__(
        self,
        session: Session,
        backend: StorageBackend,
        chunk: int = DEFAULT_CHUNK,
    ) -> None:
        self.session = session
        self.backend = backend
        self.chunk = chunk

    def _fetch_header(self, remote_path: str) -> FileHeader:
        """取文件头并解析。

        前 64 字节足够覆盖默认 51 字节头（salt16/iv16）。
        """
        head_bytes = self.backend.download_range(remote_path, 0, 63)
        return FileHeader.parse(BytesIO(head_bytes))

    def download_and_decrypt(
        self,
        remote_path: str,
        local_path: str,
    ) -> Iterator[float]:
        """从后端下载并解密到本地，yield 进度 0.0~1.0。

        参数:
            remote_path: 后端上的 .cpenc 文件路径
            local_path: 本地明文输出路径
        """
        # 1. 取文件头
        header = self._fetch_header(remote_path)
        total_cipher = self.backend.get_size(remote_path)
        total_plain = total_cipher - header.header_length

        # 2. 派生密钥
        key = self.session.derive_key(header.salt)

        # 3. 准备 CTR 解密器（与加密用同一计数器方案）
        from Crypto.Util import Counter

        initial_value = int.from_bytes(header.iv, "big")
        ctr = Counter.new(
            128, initial_value=initial_value, allow_wraparound=True
        )
        cipher = AES.new(key, AES.MODE_CTR, counter=ctr)

        # 4. 分块下载密文主体（跳过文件头）-> 解密 -> 写明文
        offset = header.header_length
        written = 0
        with open(local_path, "wb") as f:
            while written < total_plain:
                # 本块要下载的字节数（密文=明文，等长）
                want = min(self.chunk, total_plain - written)
                # 密文文件偏移 = header_length + 已解密字节数
                ct_start = offset + written
                ct_end = ct_start + want - 1
                ct_block = self.backend.download_range(
                    remote_path, ct_start, ct_end
                )
                f.write(cipher.decrypt(ct_block))
                written += len(ct_block)
                yield written / total_plain if total_plain else 1.0

        if total_plain == 0:
            yield 1.0

    def decrypt_range_to_bytes(
        self,
        remote_path: str,
        header: FileHeader | None = None,
        start: int = 0,
        end: int | None = None,
    ) -> bytes:
        """解密远程文件的指定明文区间 [start, end) 为字节。

        供流式代理与测试使用：按需拉取密文块、内存解密、不落盘。
        """
        if header is None:
            header = self._fetch_header(remote_path)

        total_cipher = self.backend.get_size(remote_path)
        total_plain = total_cipher - header.header_length
        if end is None:
            end = total_plain
        if start >= end:
            return b""

        # 用 AesCtrStreamCipher 按块对齐解密（支持随机访问）
        from cloudprism.crypto.stream_cipher import AesCtrStreamCipher

        key = self.session.derive_key(header.salt)
        sc = AesCtrStreamCipher(key, header.iv)
        BS = sc.BLOCK
        first_block = start // BS
        last_block = (end - 1) // BS
        # 密文文件偏移（含 header_length）
        ct_start = header.header_length + first_block * BS
        ct_end = header.header_length + (last_block + 1) * BS - 1
        # end 限制在文件末尾
        ct_end = min(ct_end, total_cipher - 1)
        ct = self.backend.download_range(remote_path, ct_start, ct_end)
        return sc.decrypt_range(ct, first_block, start, end)
