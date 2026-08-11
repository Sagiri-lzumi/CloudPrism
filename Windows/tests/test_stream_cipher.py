"""AES-CTR 随机访问解密单元测试。

核心思路：用 PyCryptodome 内置 AES-CTR 整段加密同一明文作为基准，
再用自定义 AesCtrStreamCipher.decrypt_range 对随机区间解密，逐字节比对。
"""

import os

import pytest
from Crypto.Cipher import AES

from cloudprism.crypto.stream_cipher import AesCtrStreamCipher


def _ctr_encrypt_full(key: bytes, iv: bytes, plaintext: bytes) -> bytes:
    """用 PyCryptodome 内置 CTR 整段加密，作为基准。

    计数器初值取 IV 的 128-bit 大端整数（与自定义实现一致）。
    """
    from Crypto.Util import Counter

    initial_value = int.from_bytes(iv, "big")
    ctr = Counter.new(
        128,
        initial_value=initial_value,
        # 允许计数器溢出回绕（与 mod 2^128 一致）
        allow_wraparound=True,
    )
    cipher = AES.new(key, AES.MODE_CTR, counter=ctr)
    return cipher.encrypt(plaintext)


@pytest.fixture
def cipher_key_iv():
    """固定 key 与 iv，便于复现。"""
    key = bytes(range(32))           # 32 字节 AES-256 密钥
    iv = bytes(range(100, 116))       # 16 字节 IV
    return key, iv


@pytest.fixture
def large_plaintext():
    """大于多块的明文（4096+ 字节）。"""
    return os.urandom(5000)


class TestAesCtrStreamCipher:
    """AES-CTR seek 解密正确性。"""

    def test_full_decrypt_matches_builtin(self, cipher_key_iv, large_plaintext):
        """整段解密与内置 CTR 输出一致（即 XOR 回原文）。"""
        key, iv = cipher_key_iv
        ct = _ctr_encrypt_full(key, iv, large_plaintext)

        cipher = AesCtrStreamCipher(key, iv)
        # 从 block 0 解整段
        pt = cipher.decrypt_range(ct, 0, 0, len(large_plaintext))
        assert pt == large_plaintext

    def test_random_range_several_offsets(self, cipher_key_iv, large_plaintext):
        """随机 [start, end) seek 解密与原明文逐字节一致。"""
        key, iv = cipher_key_iv
        ct = _ctr_encrypt_full(key, iv, large_plaintext)
        cipher = AesCtrStreamCipher(key, iv)

        BS = AesCtrStreamCipher.BLOCK
        for start in [0, 1, 15, 16, 17, 255, 256, 1000, 4095, 4096, 4999]:
            for span in [1, 16, 17, 100, 5000 - start]:
                end = min(start + span, len(large_plaintext))
                if end <= start:
                    continue
                first_block = start // BS
                last_block = (end - 1) // BS
                # 按块对齐从密文文件偏移拉取对应整块
                ct_start = first_block * BS
                ct_end = (last_block + 1) * BS
                ct_slice = ct[ct_start:ct_end]
                pt = cipher.decrypt_range(ct_slice, first_block, start, end)
                assert pt == large_plaintext[start:end], (
                    f"区间 [{start},{end}) 解密不符"
                )

    def test_cross_block_boundary(self, cipher_key_iv, large_plaintext):
        """跨块边界的区间解密正确。"""
        key, iv = cipher_key_iv
        ct = _ctr_encrypt_full(key, iv, large_plaintext)
        cipher = AesCtrStreamCipher(key, iv)
        BS = AesCtrStreamCipher.BLOCK

        # start 在块 0 中部，end 跨到块 2
        start = 8
        end = 2 * BS + 5
        first_block = start // BS
        last_block = (end - 1) // BS
        ct_slice = ct[first_block * BS:(last_block + 1) * BS]
        pt = cipher.decrypt_range(ct_slice, first_block, start, end)
        assert pt == large_plaintext[start:end]

    def test_end_at_block_middle(self, cipher_key_iv, large_plaintext):
        """end 落在块中部时解密正确。"""
        key, iv = cipher_key_iv
        ct = _ctr_encrypt_full(key, iv, large_plaintext)
        cipher = AesCtrStreamCipher(key, iv)
        BS = AesCtrStreamCipher.BLOCK

        start = 0
        end = 2 * BS + 7       # 2 整块 + 7 字节
        first_block = 0
        last_block = (end - 1) // BS
        ct_slice = ct[0:(last_block + 1) * BS]
        pt = cipher.decrypt_range(ct_slice, first_block, start, end)
        assert pt == large_plaintext[start:end]
        assert len(pt) == end - start

    def test_end_at_file_end(self, cipher_key_iv, large_plaintext):
        """end 恰为文件末尾时解密正确。"""
        key, iv = cipher_key_iv
        total = len(large_plaintext)
        ct = _ctr_encrypt_full(key, iv, large_plaintext)
        cipher = AesCtrStreamCipher(key, iv)
        BS = AesCtrStreamCipher.BLOCK

        start = total - 100
        end = total
        first_block = start // BS
        last_block = (end - 1) // BS
        ct_slice = ct[first_block * BS:(last_block + 1) * BS]
        pt = cipher.decrypt_range(ct_slice, first_block, start, end)
        assert pt == large_plaintext[start:end]

    def test_single_byte(self, cipher_key_iv, large_plaintext):
        """单字节区间解密正确。"""
        key, iv = cipher_key_iv
        ct = _ctr_encrypt_full(key, iv, large_plaintext)
        cipher = AesCtrStreamCipher(key, iv)
        BS = AesCtrStreamCipher.BLOCK

        start = 2500
        end = 2501
        first_block = start // BS
        ct_slice = ct[first_block * BS:(first_block + 1) * BS]
        pt = cipher.decrypt_range(ct_slice, first_block, start, end)
        assert pt == large_plaintext[start:end]
        assert len(pt) == 1

    def test_empty_range(self, cipher_key_iv, large_plaintext):
        """空区间返回空字节。"""
        key, iv = cipher_key_iv
        ct = _ctr_encrypt_full(key, iv, large_plaintext)
        cipher = AesCtrStreamCipher(key, iv)
        pt = cipher.decrypt_range(ct[:16], 0, 5, 5)
        assert pt == b""

    def test_invalid_key_length(self):
        """密钥长度非 32 抛 ValueError。"""
        try:
            AesCtrStreamCipher(b"\x00" * 16, b"\x00" * 16)
            assert False, "应抛出 ValueError"
        except ValueError:
            pass

    def test_invalid_iv_length(self):
        """IV 长度非 16 抛 ValueError。"""
        try:
            AesCtrStreamCipher(b"\x00" * 32, b"\x00" * 12)
            assert False, "应抛出 ValueError"
        except ValueError:
            pass
