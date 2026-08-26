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

文件尾部（v3，可选，紧随加密载荷；恢复码块）：
    recovery_len    uint16 BE，recovery_blob 字节长（可为 0，无块时直接缺省）
    recovery_blob   rsalt(16) ‖ AES-GCM(rkey, 主密码 UTF-8) ‖ Tag[16]
                    rkey = PBKDF2(恢复码随机密钥, rsalt)，GCM nonce 复用 rsalt；
                    恢复码（随机密钥的 Base32 形式）由用户离线保存，
                    凭恢复码可解密出主密码，实现无主密码开库

内部明文（v2/v3；v1 无名称字段，尾部到 VerifyMagic 即结束）：
    偏移 长度 字段
    0    1    FilenameEncryptionFlag   0x00/0x01
    1    4    ProtocolVersion          uint32 BE
    5    8    Reserved
    9    4    VerifyMagic              b"CPV\x00"
    17   2    NameLen                  uint16 BE，名称 UTF-8 字节长（可 0）
    19   N    Name                     UTF-8 编码的用户自定义密库名称

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
    name: str = ""             # 用户自定义密库名称（v1 旧文件缺省为空）
    has_recovery: bool = False # 是否携带恢复码块（v3 尾部；旧文件缺省为 False）


class VaultMarker:
    """Vault Marker 创建与校验。静态方法，纯函数式。"""

    # 文件魔数（12 字节）
    MAGIC: bytes = constants.VAULT_MAGIC

    @staticmethod
    def create(
        meta: VaultMetadata,
        master_password: str,
        recovery_blob: bytes = b"",
    ) -> bytes:
        """创建 Vault Marker 文件字节序列。

        参数:
            meta: Vault Metadata（version/vault_id/salt/iv/filename_enc/protocol_version）
            master_password: 用户主密码
            recovery_blob: 恢复码块内容（见 build_recovery_blob）；
                非空时以 recovery_len + blob 追加到文件尾部（v3）

        返回:
            完整的 Vault Marker 文件字节（明文前缀 + GCM 载荷 [+ 恢复块]）
        """
        # 1. 从主密码 + 盐派生 GCM 密钥
        key = Kdf.derive_key(master_password, meta.salt)

        # 2. 名称截断防御：字符数限上限，再按 UTF-8 编码（超长字节兜底截断）
        name_bytes = meta.name[: constants.VAULT_NAME_MAX_LEN].encode("utf-8")

        # 3. 构造内部明文：标志 + 协议版本 + 保留 + 校验魔数 + 名称字段（v2）
        inner = (
            bytes([0x01 if meta.filename_enc else 0x00])
            + struct.pack(">I", meta.protocol_version)
            + b"\x00" * constants.VAULT_RESERVED_LEN
            + constants.VAULT_VERIFY_MAGIC
            + struct.pack(">H", len(name_bytes))
            + name_bytes
        )

        # 4. AES-256-GCM 加密（16 字节 nonce，两端一致，不截断）
        cipher = AES.new(key, AES.MODE_GCM, nonce=meta.iv)
        ct, tag = cipher.encrypt_and_digest(inner)
        payload = ct + tag        # 密文 + 16 字节标签

        # 5. 拼接明文前缀 + 加密载荷（+ v3 恢复码块）
        prefix = (
            VaultMarker.MAGIC
            + struct.pack(">I", meta.version)
            + meta.vault_id
            + meta.salt
            + meta.iv
            + struct.pack(">I", len(payload))
        )
        data = prefix + payload
        if recovery_blob:
            data += struct.pack(">H", len(recovery_blob)) + recovery_blob
        return data

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

        # 1.5 v3 文件尾部恢复码块容错探测（仅需 PayloadLen 即可定位，
        # 无需解密载荷；无尾部/布局异常的 v1、v2 文件 has_recovery=False）
        has_recovery = False
        tail_bytes = file_bytes[68 + payload_len:]
        if len(tail_bytes) >= 2:
            recovery_len = struct.unpack(">H", tail_bytes[:2])[0]
            has_recovery = (
                recovery_len > 0 and len(tail_bytes) - 2 >= recovery_len
            )

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

        # 3. 解析内部明文（头部 17 字节；v2 尾部追加名称字段）
        filename_enc = inner[0] == 0x01
        protocol_version = struct.unpack(">I", inner[1:5])[0]

        # 名称字段容错解析：v1 旧文件无剩余字节 → name=""；
        # 长度字段越界/字节不足等异常布局同样回退空名称，不阻断校验
        name = ""
        tail = inner[17:]
        if len(tail) >= 2:
            name_len = struct.unpack(">H", tail[:2])[0]
            name_raw = tail[2: 2 + name_len]
            if len(name_raw) == name_len:
                try:
                    name = name_raw.decode("utf-8", errors="strict")
                except UnicodeDecodeError:
                    name = ""

        return VaultMetadata(
            version=version,
            vault_id=vault_id,
            salt=salt,
            iv=iv,
            filename_enc=filename_enc,
            protocol_version=protocol_version,
            name=name,
            has_recovery=has_recovery,
        )

    # ------------------------------------------------------------------
    # 恢复码块（v3）：构造 / 解密 / 尾部提取
    # ------------------------------------------------------------------

    @staticmethod
    def build_recovery_blob(recovery_secret: bytes, master_password: str) -> bytes:
        """构造恢复码块：rsalt(16) ‖ AES-GCM(rkey, 主密码) ‖ Tag[16]。

        rkey = PBKDF2(recovery_secret, rsalt)，GCM nonce 复用 rsalt
        （rsalt 每次随机生成，不存在 nonce 重用风险）。
        """
        from Crypto.Random import get_random_bytes

        rsalt = get_random_bytes(constants.SALT_LEN)
        rkey = Kdf.derive_key_raw(recovery_secret, rsalt)
        cipher = AES.new(rkey, AES.MODE_GCM, nonce=rsalt)
        ct, tag = cipher.encrypt_and_digest(master_password.encode("utf-8"))
        return rsalt + ct + tag

    @staticmethod
    def decrypt_recovery_blob(
        blob: bytes, recovery_secret: bytes,
    ) -> str | None:
        """凭恢复码随机密钥解密恢复块，还原主密码。

        返回:
            主密码字符串；密钥错误（GCM 标签失败）或布局异常返回 None
        """
        min_len = constants.SALT_LEN + constants.GCM_TAG_LEN + 1
        if len(blob) < min_len:
            return None
        rsalt = blob[: constants.SALT_LEN]
        ct = blob[constants.SALT_LEN: -constants.GCM_TAG_LEN]
        tag = blob[-constants.GCM_TAG_LEN:]
        rkey = Kdf.derive_key_raw(recovery_secret, rsalt)
        cipher = AES.new(rkey, AES.MODE_GCM, nonce=rsalt)
        try:
            pw_bytes = cipher.decrypt_and_verify(ct, tag)
        except ValueError:
            return None
        try:
            return pw_bytes.decode("utf-8", errors="strict")
        except UnicodeDecodeError:
            return None

    @staticmethod
    def split_recovery_tail(file_bytes: bytes) -> tuple[bytes, bytes]:
        """拆分 Marker 为（主体, 恢复块尾部）。

        尾部 = recovery_len(2) + recovery_blob；无尾部时返回 (原文件, b"")。
        供重命名等整体重写场景原样保留既有恢复块。
        """
        prefix_len = 68  # 12 magic + 4 version + 16 id + 16 salt + 16 iv + 4 len
        if len(file_bytes) < prefix_len + 4:
            return file_bytes, b""
        try:
            payload_len = struct.unpack(
                ">I", file_bytes[64:prefix_len]
            )[0]
        except struct.error:
            return file_bytes, b""
        head_end = prefix_len + payload_len
        if head_end > len(file_bytes):
            return file_bytes, b""
        return file_bytes[:head_end], file_bytes[head_end:]

    @staticmethod
    def generate_metadata(
        filename_enc: bool,
        protocol_version: int = constants.VERSION,
        version: int = constants.VAULT_VERSION,
        vault_id: bytes | None = None,
        salt: bytes | None = None,
        iv: bytes | None = None,
        name: str = "",
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
            name=name,
        )
