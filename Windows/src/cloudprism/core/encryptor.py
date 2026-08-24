"""加密上传管线。

Encryptor 负责把本地明文文件加密为 .cpenc 容器并上传到存储后端：

    1. 生成随机 salt + iv
    2. 用 Session 派生密钥
    3. 构建文件头
    4. 分块读取明文 -> AES-CTR 加密 -> 拼接密文
    5. 上传（头 + 密文）到后端

流式加密无填充，明文长度 = 密文长度。文件名加密（可选）由调用方在
上传前用 FilenameCipher 处理，Encryptor 不关心文件名。
"""

from __future__ import annotations

import os
from typing import Iterator

from Crypto.Cipher import AES
from Crypto.Random import get_random_bytes

from cloudprism import constants
from cloudprism.core.session import Session
from cloudprism.crypto.header import FileHeader
from cloudprism.storage.backend import StorageBackend


class Encryptor:
    """加密器：明文文件 -> .cpenc 容器 -> 上传后端。"""

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

    def encrypt_and_upload(
        self,
        local_path: str,
        remote_path: str,
        flags: int = 0x00,
        version: int = constants.VERSION,
    ) -> Iterator[float]:
        """加密本地文件并上传，yield 进度 0.0~1.0。

        参数:
            local_path: 本地明文文件路径
            remote_path: 后端上的目标路径（含 .cpenc 扩展名）
            flags: 文件头保留标志
            version: 文件格式版本

        yield:
            进度 0.0~1.0

        安全要点:
            密文流式写入临时文件（不在内存中累积），上传后安全删除。
        """
        import tempfile
        from Crypto.Util import Counter

        # 1. 生成随机 salt + iv
        salt = get_random_bytes(constants.SALT_LEN)
        iv = get_random_bytes(constants.IV_LEN)

        # 2. 派生密钥（Session 内部缓存）
        key = self.session.derive_key(salt)

        # 3. 构建文件头
        header = FileHeader.build(salt, iv, flags=flags, version=version)

        # 4. 准备 CTR 加密器：用 iv 作计数器初值
        initial_value = int.from_bytes(iv, "big")
        ctr = Counter.new(
            128, initial_value=initial_value, allow_wraparound=True
        )
        cipher = AES.new(key, AES.MODE_CTR, counter=ctr)

        # 5. 流式加密：读一块明文 -> 加密 -> 直接写入临时文件
        #    不在内存中累积密文，避免大文件 OOM
        total = os.path.getsize(local_path)
        tmp_path = None
        try:
            fd, tmp_path = tempfile.mkstemp(suffix=".cpenc")
            with os.fdopen(fd, "wb") as tmp:
                # 先写文件头
                tmp.write(header)
                # 流式加密明文主体
                with open(local_path, "rb") as f:
                    written = 0
                    while True:
                        plain = f.read(self.chunk)
                        if not plain:
                            break
                        tmp.write(cipher.encrypt(plain))
                        written += len(plain)
                        # 加密阶段占前半（0~0.5）
                        yield 0.5 * (written / total) if total else 0.5

            # 加密完成，cipher 对象不再需要，解除引用
            del cipher

            # 6. 上传临时文件（头+密文）到后端
            for p in self.backend.upload_chunked(tmp_path, remote_path, chunk=self.chunk):
                yield 0.5 + 0.5 * p
        finally:
            # 安全删除临时文件：无论成功或异常都确保清理
            if tmp_path is not None:
                try:
                    os.unlink(tmp_path)
                except OSError:
                    pass

        if total == 0:
            yield 1.0

    def encrypt_to_bytes(
        self,
        local_path: str,
        flags: int = 0x00,
        version: int = constants.VERSION,
    ) -> bytes:
        """加密本地文件为字节序列（头+密文），不上传。

        供测试与小文件场景使用。
        """
        salt = get_random_bytes(constants.SALT_LEN)
        iv = get_random_bytes(constants.IV_LEN)
        key = self.session.derive_key(salt)
        header = FileHeader.build(salt, iv, flags=flags, version=version)

        from Crypto.Util import Counter

        initial_value = int.from_bytes(iv, "big")
        ctr = Counter.new(
            128, initial_value=initial_value, allow_wraparound=True
        )
        cipher = AES.new(key, AES.MODE_CTR, counter=ctr)

        out = bytearray(header)
        with open(local_path, "rb") as f:
            while True:
                plain = f.read(self.chunk)
                if not plain:
                    break
                out.extend(cipher.encrypt(plain))
        return bytes(out)
