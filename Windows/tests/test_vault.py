"""Vault Marker 创建与校验单元测试。"""

import struct
import uuid

import pytest

from cloudprism import constants
from cloudprism.crypto.vault import VaultMarker, VaultMetadata


@pytest.fixture
def master_password():
    return "my_master_pw_2024"


@pytest.fixture
def meta_enc_on():
    """文件名加密开启的 VaultMetadata。"""
    return VaultMetadata(
        version=1,
        vault_id=uuid.uuid4().bytes,
        salt=b"\x00" * 16,
        iv=b"\x11" * 16,
        filename_enc=True,
        protocol_version=1,
    )


@pytest.fixture
def meta_enc_off():
    """文件名加密关闭的 VaultMetadata。"""
    return VaultMetadata(
        version=1,
        vault_id=uuid.uuid4().bytes,
        salt=b"\x22" * 16,
        iv=b"\x33" * 16,
        filename_enc=False,
        protocol_version=1,
    )


class TestVaultMarkerCreate:
    """Vault Marker 创建。"""

    def test_create_starts_with_magic(self, meta_enc_on, master_password):
        """文件以 12 字节魔数开头。"""
        data = VaultMarker.create(meta_enc_on, master_password)
        assert data[:12] == constants.VAULT_MAGIC == b"CPRISM_VAULT"

    def test_create_version_big_endian(self, meta_enc_on, master_password):
        """版本号大端 uint32。"""
        data = VaultMarker.create(meta_enc_on, master_password)
        assert struct.unpack(">I", data[12:16])[0] == meta_enc_on.version

    def test_create_prefix_preserves_vault_id_salt_iv(
        self, meta_enc_on, master_password
    ):
        """明文前缀原样保留 vault_id / salt / iv。"""
        data = VaultMarker.create(meta_enc_on, master_password)
        assert data[16:32] == meta_enc_on.vault_id
        assert data[32:48] == meta_enc_on.salt
        assert data[48:64] == meta_enc_on.iv

    def test_create_payload_length(self, meta_enc_on, master_password):
        """PayloadLen 字段 = 加密载荷（密文+标签）总长。"""
        data = VaultMarker.create(meta_enc_on, master_password)
        payload_len = struct.unpack(">I", data[64:68])[0]
        # 载荷从偏移 68 到末尾
        assert payload_len == len(data) - 68
        # 内部明文 1+4+8+4 = 17 字节，GCM 不填充，密文 17 + 标签 16 = 33
        assert payload_len == 17 + 16

    def test_create_total_length(self, meta_enc_on, master_password):
        """总长 = 前缀 68 + 载荷 33 = 101。"""
        data = VaultMarker.create(meta_enc_on, master_password)
        assert len(data) == 101


class TestVaultMarkerVerify:
    """Vault Marker 校验。"""

    def test_verify_correct_password_enc_on(self, meta_enc_on, master_password):
        """正确密码返回 metadata，filename_enc=True。"""
        data = VaultMarker.create(meta_enc_on, master_password)
        result = VaultMarker.verify(data, master_password)
        assert result is not None
        assert result.version == meta_enc_on.version
        assert result.vault_id == meta_enc_on.vault_id
        assert result.salt == meta_enc_on.salt
        assert result.iv == meta_enc_on.iv
        assert result.filename_enc is True
        assert result.protocol_version == meta_enc_on.protocol_version

    def test_verify_correct_password_enc_off(self, meta_enc_off, master_password):
        """正确密码返回 metadata，filename_enc=False。"""
        data = VaultMarker.create(meta_enc_off, master_password)
        result = VaultMarker.verify(data, master_password)
        assert result is not None
        assert result.filename_enc is False
        assert result.protocol_version == 1

    def test_verify_wrong_password_returns_none(self, meta_enc_on, master_password):
        """错误密码返回 None（GCM 标签校验失败）。"""
        data = VaultMarker.create(meta_enc_on, master_password)
        result = VaultMarker.verify(data, "wrong_password")
        assert result is None

    def test_verify_tampered_ciphertext_returns_none(
        self, meta_enc_on, master_password
    ):
        """篡改密文字节使 GCM 失败。"""
        data = bytearray(VaultMarker.create(meta_enc_on, master_password))
        # 翻转载荷区一字节（偏移 68 之后）
        data[70] ^= 0xFF
        result = VaultMarker.verify(bytes(data), master_password)
        assert result is None

    def test_verify_tampered_tag_returns_none(self, meta_enc_on, master_password):
        """篡改认证标签使 GCM 失败。"""
        data = bytearray(VaultMarker.create(meta_enc_on, master_password))
        # 末字节位于标签区
        data[-1] ^= 0xFF
        result = VaultMarker.verify(bytes(data), master_password)
        assert result is None

    def test_verify_corrupted_magic_returns_none(self, meta_enc_on, master_password):
        """魔数不符返回 None。"""
        data = bytearray(VaultMarker.create(meta_enc_on, master_password))
        data[0] = ord("X")
        result = VaultMarker.verify(bytes(data), master_password)
        assert result is None

    def test_verify_truncated_returns_none(self, meta_enc_on, master_password):
        """截断文件返回 None。"""
        data = VaultMarker.create(meta_enc_on, master_password)
        # 仅保留前 30 字节
        result = VaultMarker.verify(data[:30], master_password)
        assert result is None

    def test_verify_preserves_salt_iv(self, meta_enc_on, master_password):
        """校验返回的 salt/iv 与原始一致（多端密钥派生基准）。"""
        data = VaultMarker.create(meta_enc_on, master_password)
        result = VaultMarker.verify(data, master_password)
        assert result.salt == meta_enc_on.salt
        assert result.iv == meta_enc_on.iv


class TestVaultMarkerGenerateMetadata:
    """便捷生成 metadata。"""

    def test_generate_random_defaults(self):
        """未指定参数时自动随机生成 vault_id/salt/iv。"""
        meta = VaultMarker.generate_metadata(filename_enc=True)
        assert len(meta.vault_id) == 16
        assert len(meta.salt) == constants.SALT_LEN
        assert len(meta.iv) == constants.IV_LEN
        assert meta.filename_enc is True
        assert meta.version == constants.VAULT_VERSION
        assert meta.protocol_version == constants.VERSION

    def test_generate_two_calls_distinct(self):
        """两次生成应得到不同的随机值。"""
        m1 = VaultMarker.generate_metadata(filename_enc=False)
        m2 = VaultMarker.generate_metadata(filename_enc=False)
        assert m1.vault_id != m2.vault_id
        assert m1.salt != m2.salt
        assert m1.iv != m2.iv

    def test_generate_then_create_verify_roundtrip(self, master_password):
        """生成 metadata -> create -> verify round-trip。"""
        meta = VaultMarker.generate_metadata(filename_enc=True)
        data = VaultMarker.create(meta, master_password)
        result = VaultMarker.verify(data, master_password)
        assert result is not None
        assert result.vault_id == meta.vault_id
        assert result.filename_enc is True
