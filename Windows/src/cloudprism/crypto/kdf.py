"""密钥派生函数封装。

使用 PBKDF2-HMAC-SHA256 从用户主密码 + 盐派生 32 字节 AES 密钥。
派生出的密钥仅存在于内存中，不落盘、不上传云端。

参数（SALT_LEN / ITERATIONS / KEY_LEN）必须与 Android 端完全一致，
否则同主密码在两端会派生出不同的密钥，导致互操作失败。
"""

from Crypto.Hash import SHA256, HMAC
from Crypto.Protocol.KDF import PBKDF2

from cloudprism import constants


class Kdf:
    """主密码派生对称密钥。

    所有方法均为静态，不持有任何状态；密钥由调用方临时持有并在用后清零。
    """

    # 盐值长度（字节）
    SALT_LEN: int = constants.SALT_LEN

    # PBKDF2 迭代次数
    ITERATIONS: int = constants.KDF_ITERATIONS

    # 派生密钥长度（字节），32 对应 AES-256
    KEY_LEN: int = constants.KEY_LEN

    # 算法标识
    ALGO: str = constants.KDF_ALGO

    @staticmethod
    def derive_key(master_password: str, salt: bytes) -> bytes:
        """从主密码 + 盐派生 32 字节对称密钥。

        参数:
            master_password: 用户主密码（明文，仅存内存）
            salt: KDF 盐值，长度建议为 SALT_LEN

        返回:
            32 字节派生密钥；调用方负责在使用后清零该内存
        """
        # 主密码统一 UTF-8 编码，保证 Windows 与 Android 字节级一致
        pw = master_password.encode("utf-8")

        # 显式指定 prf 为 HMAC-SHA256，避免不同平台 PBKDF2 默认 PRF 差异
        prf = lambda p, s: HMAC.new(p, s, SHA256).digest()

        return PBKDF2(
            password=pw,
            salt=salt,
            dkLen=Kdf.KEY_LEN,
            count=Kdf.ITERATIONS,
            prf=prf,
        )
