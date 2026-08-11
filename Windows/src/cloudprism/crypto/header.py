"""加密文件头（.cpenc）构建与解析。

文件头布局（全部大端序）：
    偏移  长度  字段
    0     8     Magic         b"CPRISM\\x00\\x01"
    8     4     Version       uint32 BE
    12    4     HeaderLength  uint32 BE = 19 + SaltLen + IVLen
    16    1     SaltLen       uint8
    17    N     Salt          KDF 盐
    17+N  1     IVLen         uint8
    18+N  M     IV/Nonce      流加密初始向量
    18+N+M 1   Flags         保留，0x00

详见 Plan/Plan.md §2.5.2 与 Plan/Framework_Windows_Client.md §5.3。
"""

from __future__ import annotations

import struct
from io import BytesIO
from typing import BinaryIO

from cloudprism import constants


class HeaderError(Exception):
    """文件头解析异常（魔数不符、长度异常等）。"""


class FileHeader:
    """加密文件头。

    build/parse 为静态方法，纯函数式，不持有状态。
    """

    # 文件头魔数（8 字节）
    MAGIC: bytes = constants.MAGIC

    __slots__ = ("version", "header_length", "salt", "iv", "flags")

    def __init__(
        self,
        version: int,
        header_length: int,
        salt: bytes,
        iv: bytes,
        flags: int,
    ) -> None:
        self.version = version
        self.header_length = header_length
        self.salt = salt
        self.iv = iv
        self.flags = flags

    @staticmethod
    def build(
        salt: bytes,
        iv: bytes,
        flags: int = 0x00,
        version: int = constants.VERSION,
    ) -> bytes:
        """构建 .cpenc 文件头字节序列（全部大端序）。

        参数:
            salt: KDF 盐值
            iv: 流加密初始向量
            flags: 保留标志，默认 0x00
            version: 文件格式版本，默认 1

        返回:
            文件头字节序列；调用方在之后拼接密文主体
        """
        out = bytearray()
        out += FileHeader.MAGIC                       # 8 魔数
        out += struct.pack(">I", version)             # 4 版本（大端）
        # 头部长度 = 8(magic)+4(ver)+4(hl)+1(saltlen)+salt+1(ivlen)+iv+1(flags)
        hl = 8 + 4 + 4 + 1 + len(salt) + 1 + len(iv) + 1
        out += struct.pack(">I", hl)                  # 4 头部长度（大端）
        out += struct.pack(">B", len(salt))            # 1 盐长
        out += salt                                   # N 盐
        out += struct.pack(">B", len(iv))             # 1 IV 长
        out += iv                                      # M IV
        out += struct.pack(">B", flags)               # 1 标志
        return bytes(out)

    @staticmethod
    def parse(stream: BinaryIO) -> FileHeader:
        """从可读二进制流解析文件头。

        参数:
            stream: 已定位到文件起始的可读流（如 open(...,'rb') 或 BytesIO）

        返回:
            FileHeader 实例

        异常:
            HeaderError: 魔数不匹配或数据不足
        """
        def _read(n: int) -> bytes:
            data = stream.read(n)
            if len(data) != n:
                raise HeaderError(
                    f"文件头数据不足：期望 {n} 字节，实际 {len(data)} 字节"
                )
            return data

        magic = _read(8)
        if magic != FileHeader.MAGIC:
            raise HeaderError("魔数不匹配，非 CloudPrism 加密文件")

        version = struct.unpack(">I", _read(4))[0]
        header_length = struct.unpack(">I", _read(4))[0]

        salt_len = _read(1)[0]
        salt = _read(salt_len)

        iv_len = _read(1)[0]
        iv = _read(iv_len)

        flags = _read(1)[0]

        return FileHeader(version, header_length, salt, iv, flags)

    @staticmethod
    def parse_bytes(data: bytes) -> FileHeader:
        """从字节序列解析文件头（便捷方法）。"""
        return FileHeader.parse(BytesIO(data))
