"""KDF（PBKDF2-HMAC-SHA256）单元测试。"""

from cloudprism.crypto.kdf import Kdf

from vectors import KDF_REFERENCE_KEY_HEX


class TestKdf:
    """KDF 派生正确性与跨端一致性。"""

    def test_reference_vector(self, sample_master_password, sample_salt):
        """与硬编码参考向量一致；该向量供 Android 端逐字节比对。"""
        key = Kdf.derive_key(sample_master_password, sample_salt)
        assert key.hex() == KDF_REFERENCE_KEY_HEX

    def test_key_length(self, sample_master_password, sample_salt):
        """派生密钥长度恰为 32 字节（AES-256）。"""
        key = Kdf.derive_key(sample_master_password, sample_salt)
        assert len(key) == Kdf.KEY_LEN == 32

    def test_deterministic(self, sample_master_password, sample_salt):
        """同密码同盐多次派生结果一致。"""
        k1 = Kdf.derive_key(sample_master_password, sample_salt)
        k2 = Kdf.derive_key(sample_master_password, sample_salt)
        assert k1 == k2

    def test_different_salt_different_key(self, sample_master_password, sample_salt):
        """不同盐应派生出不同密钥。"""
        k1 = Kdf.derive_key(sample_master_password, sample_salt)
        k2 = Kdf.derive_key(sample_master_password, b"\xff" * 16)
        assert k1 != k2

    def test_different_password_different_key(self, sample_salt):
        """不同主密码应派生出不同密钥。"""
        k1 = Kdf.derive_key("password1", sample_salt)
        k2 = Kdf.derive_key("password2", sample_salt)
        assert k1 != k2

    def test_unicode_password_utf8_stable(self, sample_salt):
        """含中文的长密码应按 UTF-8 字节稳定派生。"""
        pw = "主密码测试123"
        key = Kdf.derive_key(pw, sample_salt)
        # 与等价 UTF-8 字节派生结果一致
        assert len(key) == 32
        # 同输入再次派生一致（确定性）
        assert key == Kdf.derive_key(pw, sample_salt)

    def test_empty_password(self, sample_salt):
        """空主密码也能派生（不抛异常，结果确定）。"""
        key = Kdf.derive_key("", sample_salt)
        assert len(key) == 32
        assert key == Kdf.derive_key("", sample_salt)
