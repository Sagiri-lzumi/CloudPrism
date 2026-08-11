"""加密文件头（.cpenc）构建与解析单元测试。"""

import struct

from cloudprism.crypto.header import FileHeader, HeaderError


class TestFileHeaderBuild:
    """文件头构建。"""

    def test_build_length_default(self):
        """salt16/iv16 -> 头部长度 51 字节。"""
        salt = b"\x11" * 16
        iv = b"\x22" * 16
        header = FileHeader.build(salt, iv)
        # hl = 8 + 4 + 4 + 1 + 16 + 1 + 16 + 1 = 51
        assert len(header) == 51

    def test_build_magic_prefix(self):
        """头部以 8 字节魔数开头。"""
        header = FileHeader.build(b"\x00" * 16, b"\x00" * 16)
        assert header[:8] == FileHeader.MAGIC == b"CPRISM\x00\x01"

    def test_build_big_endian_version(self):
        """版本号为大端 uint32：1 -> \\x00\\x00\\x00\\x01。"""
        header = FileHeader.build(b"\x00" * 16, b"\x00" * 16, version=1)
        assert header[8:12] == b"\x00\x00\x00\x01"

    def test_build_header_length_field(self):
        """HeaderLength 字段值与公式一致。"""
        salt = b"\x00" * 16
        iv = b"\x00" * 16
        header = FileHeader.build(salt, iv)
        hl_field = struct.unpack(">I", header[12:16])[0]
        assert hl_field == 51
        assert hl_field == len(header)

    def test_build_variable_salt_iv(self):
        """变长 salt/iv 时头部长度正确。"""
        salt = b"\x00" * 8      # 非标准长度，验证变长
        iv = b"\x00" * 12
        header = FileHeader.build(salt, iv)
        expected_hl = 8 + 4 + 4 + 1 + 8 + 1 + 12 + 1
        assert len(header) == expected_hl

    def test_build_salt_and_iv_embedded(self):
        """salt 与 iv 原样写入头部。"""
        salt = bytes(range(16))
        iv = bytes(range(16, 32))
        header = FileHeader.build(salt, iv)
        # salt 紧跟在 saltlen 字节后（偏移 17）
        assert header[16] == 16              # saltlen
        assert header[17:33] == salt
        assert header[33] == 16              # ivlen
        assert header[34:50] == iv

    def test_build_flags(self):
        """flags 字段写入。"""
        header = FileHeader.build(b"\x00" * 16, b"\x00" * 16, flags=0x01)
        # flags 是 salt/iv 后的 1 字节
        assert header[50] == 0x01


class TestFileHeaderParse:
    """文件头解析。"""

    def test_roundtrip_default(self):
        """build -> parse round-trip 各字段一致。"""
        salt = bytes(range(16))
        iv = bytes(range(16, 32))
        flags = 0x00
        header_bytes = FileHeader.build(salt, iv, flags=flags, version=1)
        parsed = FileHeader.parse_bytes(header_bytes)
        assert parsed.version == 1
        assert parsed.header_length == 51
        assert parsed.salt == salt
        assert parsed.iv == iv
        assert parsed.flags == flags

    def test_roundtrip_variable_lengths(self):
        """变长 salt/iv round-trip。"""
        salt = b"\xaa" * 8
        iv = b"\xbb" * 12
        header_bytes = FileHeader.build(salt, iv, flags=0x02, version=2)
        parsed = FileHeader.parse_bytes(header_bytes)
        assert parsed.version == 2
        assert parsed.salt == salt
        assert parsed.iv == iv
        assert parsed.flags == 0x02
        assert parsed.header_length == len(header_bytes)

    def test_parse_magic_mismatch_raises(self):
        """魔数不符抛 HeaderError。"""
        bad = b"NOTPRISM\x00\x01" + b"\x00" * 43
        try:
            FileHeader.parse_bytes(bad)
            assert False, "应抛出 HeaderError"
        except HeaderError:
            pass

    def test_parse_truncated_raises(self):
        """数据不足抛 HeaderError。"""
        # 仅 4 字节，连魔数都不全
        try:
            FileHeader.parse_bytes(b"CPRI")
            assert False, "应抛出 HeaderError"
        except HeaderError:
            pass

    def test_parse_from_file_like_stream(self):
        """从 BytesIO 流解析与 parse_bytes 结果一致。"""
        from io import BytesIO
        salt = b"\x00" * 16
        iv = b"\x00" * 16
        header_bytes = FileHeader.build(salt, iv)
        parsed_stream = FileHeader.parse(BytesIO(header_bytes))
        parsed_bytes = FileHeader.parse_bytes(header_bytes)
        assert parsed_stream.salt == parsed_bytes.salt
        assert parsed_stream.iv == parsed_bytes.iv

    def test_parse_big_endian_version(self):
        """解析出的版本号正确（大端解读）。"""
        header_bytes = FileHeader.build(b"\x00" * 16, b"\x00" * 16, version=1)
        parsed = FileHeader.parse_bytes(header_bytes)
        assert parsed.version == 1
        # 验证大端：若按小端解读会得到 16777216，显然错误
        assert parsed.version != struct.unpack("<I", header_bytes[8:12])[0]
