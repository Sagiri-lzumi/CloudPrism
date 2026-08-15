"""RangeMapper 明文区间 -> 密文区间换算单元测试。"""

import pytest

from cloudprism.crypto.header import FileHeader
from cloudprism.streaming.range_mapper import CipherRange, RangeMapper


@pytest.fixture
def header():
    """标准 51 字节头的 FileHeader。"""
    return FileHeader(
        version=1, header_length=51, salt=b"\x00" * 16, iv=b"\x00" * 16, flags=0
    )


class TestPlaintextToCipher:
    """换算数学正确性。"""

    def test_range_from_zero(self, header):
        """[0, N) 从头开始。"""
        cr = RangeMapper.plaintext_to_cipher(header, 0, 100)
        assert cr.first_block == 0
        assert cr.ct_start == 51            # 头部之后
        # last_block = 99//16 = 6 -> ct_end = 51 + 7*16 = 163
        assert cr.ct_end == 51 + 7 * 16

    def test_range_single_byte_at_zero(self, header):
        """[0, 1) 单字节。"""
        cr = RangeMapper.plaintext_to_cipher(header, 0, 1)
        assert cr.first_block == 0
        assert cr.ct_start == 51
        assert cr.ct_end == 51 + 16         # 一个整块

    def test_range_aligned_blocks(self, header):
        """块对齐区间（start/end 都是 16 的倍数）。"""
        cr = RangeMapper.plaintext_to_cipher(header, 16, 32)
        assert cr.first_block == 1
        assert cr.ct_start == 51 + 16
        # last_block = 31//16 = 1 -> ct_end = 51 + 2*16
        assert cr.ct_end == 51 + 2 * 16

    def test_range_cross_multiple_blocks(self, header):
        """跨多块区间。"""
        cr = RangeMapper.plaintext_to_cipher(header, 10, 200)
        # first_block = 10//16 = 0
        assert cr.first_block == 0
        # last_block = 199//16 = 12 -> ct_end = 51 + 13*16 = 259
        assert cr.ct_end == 51 + 13 * 16

    def test_range_mid_block_start_end(self, header):
        """start/end 都在块中部。"""
        cr = RangeMapper.plaintext_to_cipher(header, 21, 38)
        # first = 21//16 = 1, last = 37//16 = 2
        assert cr.first_block == 1
        assert cr.ct_start == 51 + 16
        assert cr.ct_end == 51 + 3 * 16

    def test_range_end_at_block_boundary(self, header):
        """end 恰为块边界。"""
        cr = RangeMapper.plaintext_to_cipher(header, 0, 16)
        # last = 15//16 = 0
        assert cr.ct_end == 51 + 16

    def test_download_range_ends_inclusive(self, header):
        """ct_end 是不含端；换算为含两端下载区间时减一。"""
        cr = RangeMapper.plaintext_to_cipher(header, 0, 16)
        # download_range(remote, cr.ct_start, cr.ct_end - 1)
        assert cr.ct_end - cr.ct_start == 16

    def test_roundtrip_all_starts(self, header):
        """全量起点扫描：换算覆盖的字节数恰为整块数*16。"""
        BS = 16
        for start in range(0, 200):
            end = start + 100
            cr = RangeMapper.plaintext_to_cipher(header, start, end)
            covered = cr.ct_end - cr.ct_start
            n_blocks = (end - 1) // BS - start // BS + 1
            assert covered == n_blocks * BS


class TestPlaintextTotal:
    """明文总量计算。"""

    def test_total(self, header):
        """总量 = 密文文件大小 - 头部长度。"""
        assert RangeMapper.plaintext_total(header, 1051) == 1000
        assert RangeMapper.plaintext_total(header, 51) == 0
