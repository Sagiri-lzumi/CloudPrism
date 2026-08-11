"""Vault Marker 创建与校验。

Vault Marker 文件（.cloudprism_vault）用于识别云盘是否被本系统加密，并作为
用户主密码的校验入口。文件由「明文前缀」与「AES-256-GCM 加密载荷」两段组成：

明文前缀（大端序）：
    偏移  长度   字段
    0     12     Magic          b"CPRISM_VAULT"
    12    4      Version        uint32 BE
    16    16     Vault ID       UUID
    32    16     KDF Salt       PBKDF2 盐（明文，非密钥）
    48    16     IV/Nonce       GCM nonce（16 字节，两端不截断）
    64    4      PayloadLen     uint32 BE，加密载荷（密文+标签）总长

加密载荷（偏移 68，长度 = PayloadLen）：
    AES-256-GCM(内部明文) ‖ GCM_Tag[16]

内部明文：
    偏移 长度 字段
    0    1    FilenameEncryptionFlag   0x00/0x01
    1    4    ProtocolVersion          uint32 BE
    5    8    Reserved
    9    4    VerifyMagic              b"CPV\x00"

密钥由主密码 + KDF Salt 派生（见 Kdf.derive_key）；GCM 认证标签即密码校验器，
解密标签通过 = 密码正确，失败 = 密码错误。

详见 Plan/Plan.md §2.6 与 Plan/Framework_Windows_Client.md §5.4。
"""

from __future__ import annotations

import struct
import uuid
from dataclasses import dataclass
from io import BytesIO

from Crypto.Cipher import AES

from cloudprism import constants
from cloudprism.crypto.kdf import Kdf


@dataclass
class VaultMetadata:
    """Vault Metadata（明文前缀字段 + 内部明文中的配置）。"""

    version: int               # Vault Marker 格式版本
    vault_id: bytes            # 16 字节 UUID
    salt: bytes                # 16 字节 KDF 盐
    iv: bytes                  # 16 字节 GCM nonce
    filename_enc: bool         # 文件名加密开关
    protocol_version: int      # 协议版本，与加密文件 VERSION 一致


class VaultMarker:
    """Vault Marker 创建与校验。静态方法，纯函数式。"""

    # 文件魔数（12 字节）
    MAGIC: bytes = constants.VAULT_MAGIC

    @staticmethod
    def create(meta: VaultMetadata, master_password: str) -> bytes:
        """创建 Vault Marker 文件字节序列。

        参数:
            meta: Vault Metadata（version/vault_id/salt/iv/filename_enc/protocol_version）
            master_password: 用户主密码

        返回:
            完整的 Vault Marker 文件字节（明文前缀 + GCM 载荷）
        """
        # 1. 从主密码 + 盐派生 GCM 密钥
        key = Kdf.derive_key(master_password, meta.salt)

        # 2. 构造内部明文：标志 + 协议版本 + 保留 + 校验魔数
        inner = (
            bytes([0x01 if meta.filename_enc else 0x00])
            + struct.pack(">I", meta.protocol_version)
            + b"\x00" * constants.VAULT_RESERVED_LEN
            + constants.VAULT_VERIFY_MAGIC
        )

        # 3. AES-256-GCM 加密（16 字节 nonce，两端一致，不截断）
        cipher = AES.new(key, AES.MODE_GCM, nonce=meta.iv)
        ct, tag = cipher.encrypt_and_digest(inner)
        payload = ct + tag        # 密文 + 16 字节标签

        # 4. 拼接明文前缀 + 加密载荷
        prefix = (
            VaultMarker.MAGIC
            + struct.pack(">I", meta.version)
            + meta.vault_id
            + meta.salt
            + meta.iv
            + struct.pack(">I", len(payload))
        )
        return prefix + payload

    @staticmethod
    def verify(file_bytes: bytes, master_password: str) -> VaultMetadata | None:
        """校验主密码并解析 Vault Metadata。

        参数:
            file_bytes: Vault Marker 文件完整字节
            master_password: 待校验的主密码

        返回:
            校验成功返回 VaultMetadata；密码错误或格式不符返回 None
        """
        bio = BytesIO(file_bytes)

        def _read(n: int) -> bytes:
            data = bio.read(n)
            if len(data) != n:
                return None
            return data

        # 1. 解析明文前缀
        magic = _read(len(VaultMarker.MAGIC))
        if magic != VaultMarker.MAGIC:
            return None

        version_bytes = _read(4)
        if version_bytes is None:
            return None
        version = struct.unpack(">I", version_bytes)[0]

        vault_id = _read(16)
        salt = _read(16)
        iv = _read(16)
        if vault_id is None or salt is None or iv is None:
            return None

        payload_len_bytes = _read(4)
        if payload_len_bytes is None:
            return None
        payload_len = struct.unpack(">I", payload_len_bytes)[0]

        payload = _read(payload_len)
        if payload is None:
            return None
        if len(payload) < constants.GCM_TAG_LEN:
            return None

        # 2. 派生密钥并 GCM 解密
        ct = payload[: -constants.GCM_TAG_LEN]
        tag = payload[-constants.GCM_TAG_LEN :]
        key = Kdf.derive_key(master_password, salt)
        cipher = AES.new(key, AES.MODE_GCM, nonce=iv)
        try:
            inner = cipher.decrypt_and_verify(ct, tag)
        except ValueError:
            # GCM 标签校验失败 = 密码错误
            return None

        # 3. 解析内部明文
        filename_enc = inner[0] == 0x01
        protocol_version = struct.unpack(">I", inner[1:5])[0]

        return VaultMetadata(
            version=version,
            vault_id=vault_id,
            salt=salt,
            iv=iv,
            filename_enc=filename_enc,
            protocol_version=protocol_version,
        )

    @staticmethod
    def generate_metadata(
        filename_enc: bool,
        protocol_version: int = constants.VERSION,
        version: int = constants.VAULT_VERSION,
        vault_id: bytes | None = None,
        salt: bytes | None = None,
        iv: bytes | None = None,
    ) -> VaultMetadata:
        """便捷生成 VaultMetadata，未指定参数时自动随机生成。

        供初始化向导调用：生成 Vault ID、Salt、IV 后封装为 metadata。
        """
        from Crypto.Random import get_random_bytes

        return VaultMetadata(
            version=version,
            vault_id=vault_id or uuid.uuid4().bytes,
            salt=salt or get_random_bytes(constants.SALT_LEN),
            iv=iv or get_random_bytes(constants.IV_LEN),
            filename_enc=filename_enc,
            protocol_version=protocol_version,
        )
