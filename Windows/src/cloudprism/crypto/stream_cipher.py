"""AES-CTR 流式随机访问解密。

将 16 字节 IV 视为 128-bit 大端计数器初值，对任意明文字节区间 [start, end)
按需解密，支持流式代理的 Range 请求。明文长度 = 密文长度（流加密无填充）。

计数器方案：
    counter = (IV + block_index) mod 2^128
    keystream_block = AES_ECB_Encrypt(key, counter)
    pt[p] = ct[p] XOR keystream_block[p % 16]

详见 Plan/Plan.md §2.3、§2.7 与 Plan/Framework_Windows_Client.md §5.5。
两端（Windows/Android）必须使用同一计数器方案，否则 seek 后解密错位。
"""

from __future__ import annotations

from Crypto.Cipher import AES

from cloudprism import constants


class AesCtrStreamCipher:
    """AES-CTR 随机访问解密器。

    持有 key 与 IV（派生为 128-bit 大端整数）；不持有任何密文状态，
    可对同一文件的任意区间重复调用 decrypt_range。
    """

    # AES 块大小（字节）
    BLOCK: int = constants.BLOCK_SIZE

    def __init__(self, key: bytes, iv: bytes) -> None:
        if len(key) != constants.KEY_LEN:
            raise ValueError(f"密钥长度必须为 {constants.KEY_LEN} 字节")
        if len(iv) != constants.IV_LEN:
            raise ValueError(f"IV 长度必须为 {constants.IV_LEN} 字节")
        self.key = key
        # IV 作为 128-bit 大端计数器初值
        self.iv_int = int.from_bytes(iv, "big")

    def _keystream_block(self, block_index: int) -> bytes:
        """生成第 block_index 块的 16 字节密钥流。"""
        # 计数器 = (IV + block_index) mod 2^128
        counter = (self.iv_int + block_index) % (1 << 128)
        counter_bytes = counter.to_bytes(16, "big")
        # AES-ECB 加密单块即得 CTR 密钥流
        ecb = AES.new(self.key, AES.MODE_ECB)
        return ecb.encrypt(counter_bytes)

    def decrypt_range(
        self,
        ct: bytes,
        first_block: int,
        start: int,
        end: int,
    ) -> bytes:
        """解密覆盖若干整块的密文，再切片到 [start, end) 明文区间。

        参数:
            ct: 密文字节序列，必须覆盖 first_block..(first_block + len(ct)//16 - 1)
                这些整块；通常由调用方按块对齐从文件偏移拉取
            first_block: ct 起始对应的块索引（明文偏移 start // 16）
            start: 期望明文起始偏移（相对于整个明文）
            end: 期望明文结束偏移（不含，相对于整个明文）

        返回:
            [start, end) 区间的明文字节
        """
        if end <= start:
            return b""
        # 解密 ct 覆盖的全部整块
        out = bytearray()
        for i in range(0, len(ct), self.BLOCK):
            ks = self._keystream_block(first_block + i // self.BLOCK)
            block = ct[i:i + self.BLOCK]
            out.extend(b ^ k for b, k in zip(block, ks))

        # 切片到 [start, end)（转换为相对于 first_block 的局部偏移）
        local_start = start - first_block * self.BLOCK
        local_end = end - first_block * self.BLOCK
        return bytes(out[local_start:local_end])
