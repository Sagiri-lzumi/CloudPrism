"""加密上传管线。

Encryptor 负责把本地明文文件加密为 .cpenc 容器并上传到存储后端：

    1. 生成随机 salt + iv
    2. 用 Session 派生密钥
    3. 构建文件头
    4. 分块读取明文 -> AES-CTR 加密 -> 拼接密文
    5. 上传（头 + 密文）到后端

流式加密无填充，明文长度 = 密文长度。文件名加密（可选）由调用方在
上传前用 FilenameCipher 处理，Encryptor 不关心文件名。

多核加密：当 max_workers > 1 时，利用 AES-CTR 的随机访问特性，
将文件分为多个段，每段用独立 counter 偏移并行加密，最后按序拼接。
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
        max_workers: int = 1,
    ) -> Iterator[float]:
        """加密本地文件并上传，yield 进度 0.0~1.0。

        参数:
            local_path: 本地明文文件路径
            remote_path: 后端上的目标路径（含 .cpenc 扩展名）
            flags: 文件头保留标志
            version: 文件格式版本
            max_workers: 并行加密内核数（1=单核流式，>1=多核并行）

        yield:
            进度 0.0~1.0

        安全要点:
            密文流式写入临时文件（不在内存中累积），上传后安全删除。
        """
        import tempfile

        # 1. 生成随机 salt + iv
        salt = get_random_bytes(constants.SALT_LEN)
        iv = get_random_bytes(constants.IV_LEN)

        # 2. 派生密钥（Session 内部缓存）
        key = self.session.derive_key(salt)

        # 3. 构建文件头
        header = FileHeader.build(salt, iv, flags=flags, version=version)

        total = os.path.getsize(local_path)
        tmp_path = None
        try:
            # 临时密文落产品自管的 data/tmp/（部分环境 %TEMP% ACL 不完整）
            from cloudprism.core.paths import temp_dir

            fd, tmp_path = tempfile.mkstemp(
                suffix=".cpenc", dir=temp_dir(create=True)
            )

            if max_workers <= 1 or total < 4 * 1024 * 1024:
                # 单核流式加密（小文件或用户限制）
                with os.fdopen(fd, "wb") as tmp:
                    fd = -1  # fd 所有权移交给 fdopen，finally 不再重复关闭
                    tmp.write(header)
                    yield from self._encrypt_sequential(
                        tmp, local_path, key, iv, total
                    )
            
                for p in self.backend.upload_chunked(tmp_path, remote_path, chunk=self.chunk):
                    yield 0.5 + 0.5 * p
            else:
                # 多核并行加密：逐段完成即回报进度（映射到 0~0.5 区间，
                # 与单核路径语义一致），避免大文件加密阶段长时间零进度观感。
                # 并行不可用时（部分受管/沙箱环境拒绝创建进程间管道，
                # Pipe 抛 WinError 5）降级单核流式：慢但保证功能可用。
                os.close(fd)
                fd = -1
                try:
                    for p in self._parallel_encrypt(
                        local_path, tmp_path, key, iv, total, max_workers, header
                    ):
                        yield 0.5 * p
                except (PermissionError, OSError):
                    with open(tmp_path, "wb") as tmp:
                        tmp.write(header)  # 降级路径同样先落文件头
                        yield from self._encrypt_sequential(
                            tmp, local_path, key, iv, total
                        )
                for p in self.backend.upload_chunked(tmp_path, remote_path, chunk=self.chunk):
                    yield 0.5 + 0.5 * p

        finally:
            if fd is not None and fd != -1:
                try:
                    os.close(fd)
                except OSError:
                    pass
            if tmp_path is not None:
                try:
                    os.unlink(tmp_path)
                except OSError:
                    pass

        if total == 0:
            yield 1.0

    def _encrypt_sequential(
        self,
        tmp,
        local_path: str,
        key: bytes,
        iv: bytes,
        total: int,
    ) -> Iterator[float]:
        """单核流式加密主体：分块读明文 -> AES-CTR 加密 -> 写临时文件。

        调用方负责打开/关闭 ``tmp`` 并在之前写入文件头。
        yield 加密阶段进度 0.0~0.5（与上层进度映射语义一致）。
        """
        from Crypto.Util import Counter

        initial_value = int.from_bytes(iv, "big")
        ctr = Counter.new(
            128, initial_value=initial_value, allow_wraparound=True
        )
        cipher = AES.new(key, AES.MODE_CTR, counter=ctr)
        with open(local_path, "rb") as f:
            written = 0
            while True:
                plain = f.read(self.chunk)
                if not plain:
                    break
                tmp.write(cipher.encrypt(plain))
                written += len(plain)
                yield 0.5 * (written / total) if total else 0.5
        del cipher

    def _parallel_encrypt(
        self,
        local_path: str,
        tmp_path: str,
        key: bytes,
        iv: bytes,
        total: int,
        max_workers: int,
        header: bytes,
    ) -> Iterator[float]:
        """多核并行加密：将文件分段，每段独立加密后按序拼接。

        AES-CTR 支持随机访问：segment i 的 counter 初值 =
        int.from_bytes(iv, 'big') + i * (segment_size // 16)

        yield:
            加密阶段进度 0.0~1.0（按已完成段数计，非字节级）
        """
        from concurrent.futures import ProcessPoolExecutor, as_completed

        # 分段：每段至少 4MB，段数不超过 max_workers；上限 4 段——
        # 打包态下每个子进程是完整的 exe（启动需数秒），段数过多只会
        # 增加进程开销，收益递减（单文件上传带宽通常才是瓶颈）
        min_segment = 4 * 1024 * 1024
        num_segments = min(max_workers, max(1, total // min_segment), 4)
        segment_size = (total + num_segments - 1) // num_segments

        iv_int = int.from_bytes(iv, "big")

        # 构建段参数列表；段数可能小于 num_segments（尾段长度归零提前终止）
        segments = []
        for i in range(num_segments):
            offset = i * segment_size
            length = min(segment_size, total - offset)
            if length <= 0:
                break
            ctr_initial = iv_int + (offset // 16)
            segments.append((local_path, offset, length, key, ctr_initial))

        # 并行加密各段：乱序完成时按段序号回填，逐段完成即回报进度。
        # 子进程启动期间（打包态数秒）无进度产出，属正常现象，
        # 由上层进度文案提示用户。
        results: list[bytes | None] = [None] * len(segments)
        with ProcessPoolExecutor(max_workers=len(segments)) as executor:
            futures = {
                executor.submit(_encrypt_segment, seg_args): idx
                for idx, seg_args in enumerate(segments)
            }
            done_count = 0
            for fut in as_completed(futures):
                results[futures[fut]] = fut.result()
                done_count += 1
                yield done_count / len(segments)

        # 按序写入临时文件：先写文件头，再写各段密文
        with open(tmp_path, "wb") as tmp:
            tmp.write(header)
            for encrypted_data in results:
                tmp.write(encrypted_data)  # type: ignore[arg-type]
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


def _encrypt_segment(args: tuple) -> bytes:
    """加密单个段（供 ProcessPoolExecutor 调用）。

    参数: (local_path, offset, length, key, ctr_initial)
    """
    from Crypto.Util import Counter

    local_path, offset, length, key, ctr_initial = args
    ctr = Counter.new(128, initial_value=ctr_initial, allow_wraparound=True)
    cipher = AES.new(key, AES.MODE_CTR, counter=ctr)

    with open(local_path, "rb") as f:
        f.seek(offset)
        data = f.read(length)
    return cipher.encrypt(data)
