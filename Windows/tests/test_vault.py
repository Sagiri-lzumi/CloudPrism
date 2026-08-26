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
        # v2 内部明文 1+4+8+4+2（名称长度字段，空名称）= 19 字节，
        # GCM 不填充，密文 19 + 标签 16 = 35
        assert payload_len == 19 + 16

    def test_create_total_length(self, meta_enc_on, master_password):
        """总长 = 前缀 68 + 载荷 35 = 103（v2；空名称仍写长度字段）。"""
        data = VaultMarker.create(meta_enc_on, master_password)
        assert len(data) == 103


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


class TestVaultMarkerName:
    """用户自定义密库名称（v2 内部明文尾部字段）。"""

    def test_name_roundtrip(self, master_password):
        """名称随 Marker 加密保存，create/verify 往返一致。"""
        meta = VaultMarker.generate_metadata(
            filename_enc=False, name="我的网盘密库"
        )
        data = VaultMarker.create(meta, master_password)
        result = VaultMarker.verify(data, master_password)
        assert result is not None
        assert result.name == "我的网盘密库"

    def test_empty_name_default(self, master_password):
        """未指定名称时 verify 返回空字符串。"""
        meta = VaultMarker.generate_metadata(filename_enc=False)
        data = VaultMarker.create(meta, master_password)
        result = VaultMarker.verify(data, master_password)
        assert result is not None
        assert result.name == ""

    def test_v1_legacy_file_compat(self, master_password):
        """v1 旧文件（内部明文无名称字段）解析兼容：name 为空。

        手工构造 v1 布局（17 字节内部明文）并 GCM 加密，
        验证新版解析器向后兼容存量密库。
        """
        from Crypto.Cipher import AES

        from cloudprism.crypto.kdf import Kdf

        salt = b"\x44" * 16
        iv = b"\x55" * 16
        inner = (
            bytes([0x00])                       # filename_enc 关
            + struct.pack(">I", 1)             # protocol_version
            + b"\x00" * constants.VAULT_RESERVED_LEN
            + constants.VAULT_VERIFY_MAGIC      # 共 17 字节，无名称字段
        )
        key = Kdf.derive_key(master_password, salt)
        cipher = AES.new(key, AES.MODE_GCM, nonce=iv)
        ct, tag = cipher.encrypt_and_digest(inner)
        payload = ct + tag
        data = (
            constants.VAULT_MAGIC
            + struct.pack(">I", 1)             # v1 版本字段原样保留在明文前缀
            + uuid.uuid4().bytes
            + salt
            + iv
            + struct.pack(">I", len(payload))
            + payload
        )
        result = VaultMarker.verify(data, master_password)
        assert result is not None
        assert result.name == ""
        assert result.filename_enc is False

    def test_name_over_limit_truncated(self, master_password):
        """超过上限的名称在 create 时截断到字符上限。"""
        long_name = "名" * (constants.VAULT_NAME_MAX_LEN + 10)
        meta = VaultMarker.generate_metadata(filename_enc=False, name=long_name)
        data = VaultMarker.create(meta, master_password)
        result = VaultMarker.verify(data, master_password)
        assert result is not None
        assert result.name == "名" * constants.VAULT_NAME_MAX_LEN


class TestRecoveryBlock:
    """恢复码块（v3 文件尾部）：构造 / 解密 / 布局兼容。"""

    SECRET = b"\xAB" * constants.RECOVERY_SECRET_LEN

    def test_build_decrypt_roundtrip(self, master_password):
        """恢复块构造后凭同一密钥可还原主密码。"""
        blob = VaultMarker.build_recovery_blob(self.SECRET, master_password)
        assert VaultMarker.decrypt_recovery_blob(blob, self.SECRET) == master_password

    def test_decrypt_wrong_secret_returns_none(self, master_password):
        """错误恢复密钥：GCM 标签失败返回 None。"""
        blob = VaultMarker.build_recovery_blob(self.SECRET, master_password)
        assert VaultMarker.decrypt_recovery_blob(blob, b"\xCD" * 10) is None

    def test_decrypt_truncated_blob_returns_none(self, master_password):
        """布局异常（过短）返回 None，不抛异常。"""
        blob = VaultMarker.build_recovery_blob(self.SECRET, master_password)
        assert VaultMarker.decrypt_recovery_blob(blob[:10], self.SECRET) is None

    def test_create_with_blob_verify_has_recovery(self, master_password):
        """带恢复块的 Marker：verify 正常且 has_recovery=True。"""
        meta = VaultMarker.generate_metadata(filename_enc=False, name="v3库")
        blob = VaultMarker.build_recovery_blob(self.SECRET, master_password)
        data = VaultMarker.create(meta, master_password, recovery_blob=blob)
        result = VaultMarker.verify(data, master_password)
        assert result is not None
        assert result.has_recovery is True
        assert result.name == "v3库"
        # 主密码校验不受尾部影响：错密码仍返回 None
        assert VaultMarker.verify(data, "wrong") is None

    def test_create_without_blob_has_recovery_false(self, master_password):
        """v2 布局（无尾部）：has_recovery=False，v1/v2 兼容不受影响。"""
        meta = VaultMarker.generate_metadata(filename_enc=True)
        data = VaultMarker.create(meta, master_password)
        result = VaultMarker.verify(data, master_password)
        assert result is not None
        assert result.has_recovery is False
        assert len(data) == 103  # 无尾部时总长不变（与 v2 基线一致）

    def test_split_recovery_tail_with_blob(self, master_password):
        """拆分：主体 + 尾部（2 字节长度头 + blob），重组等于原文件。"""
        meta = VaultMarker.generate_metadata(filename_enc=False)
        blob = VaultMarker.build_recovery_blob(self.SECRET, master_password)
        data = VaultMarker.create(meta, master_password, recovery_blob=blob)
        head, tail = VaultMarker.split_recovery_tail(data)
        assert head + tail == data
        assert struct.unpack(">H", tail[:2])[0] == len(blob)
        assert tail[2:] == blob

    def test_split_recovery_tail_without_blob(self, master_password):
        """无尾部文件：尾部为空，主体为整个文件。"""
        meta = VaultMarker.generate_metadata(filename_enc=False)
        data = VaultMarker.create(meta, master_password)
        head, tail = VaultMarker.split_recovery_tail(data)
        assert head == data
        assert tail == b""

    def test_split_recovery_tail_short_file(self):
        """短于前缀的文件：原样返回不抛异常。"""
        head, tail = VaultMarker.split_recovery_tail(b"tiny")
        assert head == b"tiny"
        assert tail == b""
