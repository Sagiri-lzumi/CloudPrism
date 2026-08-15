"""明文 Range -> 密文文件偏移换算。

将播放器请求的明文字节区间 [start, end) 换算为密文文件中的字节区间，
供流式代理向后端发起精确的分块拉取。

数学（AES-CTR，块大小 16，HL = header_length）：
    first_block = start // BS
    last_block  = (end - 1) // BS
    密文起始 = HL + first_block * BS
    密文结束 = HL + (last_block + 1) * BS   （不含）
    下载范围（含两端）= [密文起始, 密文结束 - 1]

详见 Plan/Plan.md §2.7 与 Plan/Framework_Windows_Client.md §5.5、§5.8。
两端（Windows/Android）必须使用同一换算数学。
"""

from __future__ import annotations

from dataclasses import dataclass

from cloudprism import constants
from cloudprism.crypto.header import FileHeader


@dataclass(frozen=True)
class CipherRange:
    """密文文件中的字节区间。"""

    ct_start: int        # 密文起始偏移（含）
    ct_end: int          # 密文结束偏移（不含）
    first_block: int    # 起始块索引（解密时定位计数器）


class RangeMapper:
    """明文区间 -> 密文区间换算。"""

    # AES 块大小
    BLOCK_SIZE: int = constants.BLOCK_SIZE

    @staticmethod
    def plaintext_to_cipher(
        header: FileHeader, start: int, end: int
    ) -> CipherRange:
        """明文 [start, end) -> 密文区间。

        参数:
            header: 已解析的文件头（提供 header_length）
            start: 明文起始偏移（含）
            end: 明文结束偏移（不含）

        返回:
            CipherRange(ct_start, ct_end, first_block)
            其中 ct_start/ct_end 为密文文件偏移（含 header），
            可直接用于后端 download_range 的 [ct_start, ct_end-1]。
        """
        BS = RangeMapper.BLOCK_SIZE
        first_block = start // BS
        last_block = (end - 1) // BS
        hl = header.header_length
        return CipherRange(
            ct_start=hl + first_block * BS,
            ct_end=hl + (last_block + 1) * BS,
            first_block=first_block,
        )

    @staticmethod
    def plaintext_total(header: FileHeader, cipher_file_size: int) -> int:
        """明文总长度 = 密文文件大小 - 头部长度（流加密无填充）。"""
        return cipher_file_size - header.header_length
