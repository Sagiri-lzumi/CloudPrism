"""文件名加密与 Base32 URL-safe 编码。

文件名加密采用 AES-256-GCM：输出 Base32(nonce ‖ ciphertext ‖ tag)，去除 = 填充。
选择 Base32 是为了避免云盘对大小写敏感或特殊字符限制；去除 = 填充以保持文件名
整洁，解码时补齐。

编码格式（云端文件名主体）：
    Base32( nonce[12] ‖ ct ‖ tag[16] )，去 = 填充
最终云端文件名 = 上述编码 + ".cpenc"

详见 Plan/Plan.md §2.8 与 Plan/Framework_Windows_Client.md §5.6。
两端 Base32 字母表、去填充规则必须一致。
"""

from __future__ import annotations

import base64

from Crypto.Cipher import AES
from Crypto.Random import get_random_bytes

from cloudprism import constants


def b32_encode_nopad(data: bytes) -> str:
    """Base32 URL-safe 编码（RFC 4648 字母表），去除 = 填充。

    参数:
        data: 任意字节

    返回:
        不含 = 填充的 Base32 字符串（大写）
    """
    encoded = base64.b32encode(data).decode("ascii")
    return encoded.rstrip("=")


def b32_decode_nopad(s: str) -> bytes:
    """Base32 解码，接受去填充的输入。

    规则：
      1. 转大写（Base32 大小写不敏感，统一大写）
      2. 补齐 = 到 8 的倍数（标准解码要求）
      3. 解码

    参数:
        s: 不含 = 填充的 Base32 字符串

    返回:
        原始字节

    异常:
        值错误（base32decode 抛出）当含非法字符时
    """
    up = s.upper()
    # 补齐到 8 的倍数
    pad = (-len(up)) % 8
    up += "=" * pad
    return base64.b32decode(up)


class FilenameCipher:
    """文件名 AES-256-GCM 加解密。"""

    # GCM nonce 长度（字节）
    NONCE_LEN: int = constants.FILENAME_NONCE_LEN

    # GCM 认证标签长度（字节）
    TAG_LEN: int = constants.GCM_TAG_LEN

    @staticmethod
    def encrypt(plain: str, key: bytes) -> str:
        """加密文件名。

        参数:
            plain: 原始文件名（UTF-8）
            key: 32 字节 AES-256 密钥

        返回:
            Base32(nonce ‖ ct ‖ tag) 去 = 填充
        """
        nonce = get_random_bytes(FilenameCipher.NONCE_LEN)
        cipher = AES.new(key, AES.MODE_GCM, nonce=nonce)
        ct, tag = cipher.encrypt_and_digest(plain.encode("utf-8"))
        # 拼接 nonce + 密文 + 标签，整体 Base32 编码去填充
        return b32_encode_nopad(nonce + ct + tag)

    @staticmethod
    def decrypt(encoded: str, key: bytes) -> str:
        """解密文件名。

        参数:
            encoded: Base32 去 = 填充的编码字符串
            key: 32 字节 AES-256 密钥

        返回:
            原始文件名（UTF-8）

        异常:
            ValueError: GCM 标签校验失败（文件名被篡改或密钥错误）
        """
        raw = b32_decode_nopad(encoded)
        nonce = raw[: FilenameCipher.NONCE_LEN]
        ct = raw[FilenameCipher.NONCE_LEN : -FilenameCipher.TAG_LEN]
        tag = raw[-FilenameCipher.TAG_LEN :]
        cipher = AES.new(key, AES.MODE_GCM, nonce=nonce)
        # 标签校验失败时 decrypt_and_verify 抛 ValueError
        return cipher.decrypt_and_verify(ct, tag).decode("utf-8")
