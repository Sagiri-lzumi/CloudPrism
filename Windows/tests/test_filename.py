"""文件名加密与 Base32 编码单元测试。"""

import base64

import pytest

from cloudprism.crypto.filename import (
    FilenameCipher,
    b32_decode_nopad,
    b32_encode_nopad,
)


@pytest.fixture
def cipher_key():
    """32 字节 AES-256 密钥。"""
    return bytes(range(32))


class TestBase32NoPad:
    """Base32 去填充编解码。"""

    def test_encode_no_padding(self):
        """编码结果不含 = 填充。"""
        # 11 字节输入 -> 标准 Base32 有 = 填充
        data = b"hello test!"  # 11 字节，非 5 的倍数，标准编码会有 =
        standard = base64.b32encode(data).decode("ascii")
        assert "=" in standard
        nopad = b32_encode_nopad(data)
        assert "=" not in nopad

    def test_roundtrip(self):
        """编码后解码还原。"""
        for data in [b"", b"a", b"ab", b"abc", b"\x00" * 16, bytes(range(50))]:
            assert b32_decode_nopad(b32_encode_nopad(data)) == data

    def test_lowercase_input_decodes(self):
        """小写输入可解码（大小写不敏感）。"""
        data = b"some data"
        encoded = b32_encode_nopad(data)
        # 转小写后仍应能解码
        assert b32_decode_nopad(encoded.lower()) == data

    def test_non_base32_char_raises(self):
        """非 Base32 字符抛异常。"""
        with pytest.raises(Exception):
            b32_decode_nopad("!!!1abc")

    def test_alphabet_correct(self):
        """使用 RFC 4648 字母表。"""
        # 0x00 -> 前 5 位 00000 = 0 -> 'A'，剩余 3 位补零 00000 = 0 -> 'A'
        assert b32_encode_nopad(b"\x00") == "AA"
        # 0x1f (00011111) -> 前 5 位 00011 = 3 -> 'D'，剩余 3 位 111 补零 11100 = 28 -> '4'
        assert b32_encode_nopad(b"\x1f") == "D4"


class TestFilenameCipher:
    """文件名 AES-GCM 加解密。"""

    def test_roundtrip_ascii(self, cipher_key):
        """ASCII 文件名 round-trip。"""
        name = "video_2024.mp4"
        enc = FilenameCipher.encrypt(name, cipher_key)
        dec = FilenameCipher.decrypt(enc, cipher_key)
        assert dec == name

    def test_roundtrip_chinese(self, cipher_key):
        """中文文件名 round-trip（UTF-8）。"""
        name = "我的视频_机密.mp4"
        enc = FilenameCipher.encrypt(name, cipher_key)
        dec = FilenameCipher.decrypt(enc, cipher_key)
        assert dec == name

    def test_roundtrip_special_chars(self, cipher_key):
        """特殊字符文件名 round-trip。"""
        name = "file (1) [final] #2 & more.mkv"
        enc = FilenameCipher.encrypt(name, cipher_key)
        dec = FilenameCipher.decrypt(enc, cipher_key)
        assert dec == name

    def test_output_no_equals(self, cipher_key):
        """加密输出不含 = 填充。"""
        enc = FilenameCipher.encrypt("any_name.txt", cipher_key)
        assert "=" not in enc

    def test_output_is_base32(self, cipher_key):
        """输出仅含 Base32 字母表字符。"""
        enc = FilenameCipher.encrypt("x", cipher_key)
        alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
        assert all(c in alphabet for c in enc)

    def test_different_nonces_each_call(self, cipher_key):
        """每次加密使用随机 nonce，密文不同。"""
        name = "same_name.txt"
        enc1 = FilenameCipher.encrypt(name, cipher_key)
        enc2 = FilenameCipher.encrypt(name, cipher_key)
        assert enc1 != enc2
        # 但两者都能正确解密
        assert FilenameCipher.decrypt(enc1, cipher_key) == name
        assert FilenameCipher.decrypt(enc2, cipher_key) == name

    def test_wrong_key_fails(self, cipher_key):
        """错误密钥解密失败（GCM 标签校验）。"""
        enc = FilenameCipher.encrypt("secret.mp4", cipher_key)
        wrong_key = bytes(range(32, 64))
        with pytest.raises(ValueError):
            FilenameCipher.decrypt(enc, wrong_key)

    def test_tampered_ciphertext_fails(self, cipher_key):
        """篡改密文后标签校验失败。"""
        enc = FilenameCipher.encrypt("secret.mp4", cipher_key)
        raw = b32_decode_nopad(enc)
        # 翻转密文区一字节（跳过 nonce）
        tampered = bytearray(raw)
        tampered[13] ^= 0xFF
        tampered_enc = b32_encode_nopad(bytes(tampered))
        with pytest.raises(ValueError):
            FilenameCipher.decrypt(tampered_enc, cipher_key)

    def test_empty_filename(self, cipher_key):
        """空文件名 round-trip。"""
        enc = FilenameCipher.encrypt("", cipher_key)
        dec = FilenameCipher.decrypt(enc, cipher_key)
        assert dec == ""
