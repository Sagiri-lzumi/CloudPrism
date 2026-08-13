"""主密码会话与密钥派生。

Session 持有用户主密码（仅存内存，不落盘、不上传）与可选的密钥缓存。
Vault Metadata 中的 salt 用于派生文件加密密钥；同一会话内对相同 salt 的
派生结果可缓存，避免重复 PBKDF2（200000 次迭代较慢）。

退出会话时应调用 close() 清零密码与缓存密钥。
"""

from __future__ import annotations

import ctypes

from cloudprism.crypto.kdf import Kdf


class Session:
    """主密码会话。

    持有主密码并按 salt 派生密钥；派生结果缓存在内存中以便复用。
    """

    def __init__(self, master_password: str) -> None:
        # 主密码仅存内存；用可变 bytearray 便于清零
        self._password = bytearray(master_password.encode("utf-8"))
        # salt -> 派生密钥 的缓存
        self._key_cache: dict[bytes, bytes] = {}

    @property
    def master_password(self) -> str:
        """获取主密码（UTF-8 解码）。"""
        return self._password.decode("utf-8")

    def derive_key(self, salt: bytes) -> bytes:
        """按 salt 派生 32 字节密钥，结果缓存。

        同一 salt 多次调用只派生一次，避免重复 PBKDF2 开销。
        """
        if salt not in self._key_cache:
            self._key_cache[salt] = Kdf.derive_key(self.master_password, salt)
        return self._key_cache[salt]

    def clear_cache(self) -> None:
        """清空密钥缓存。"""
        for key in self._key_cache.values():
            _zeroize(key)
        self._key_cache.clear()

    def close(self) -> None:
        """关闭会话：清零密码与缓存密钥。"""
        self.clear_cache()
        _zeroize(self._password)
        self._password = bytearray()


def _zeroize(buf: bytearray | bytes) -> None:
    """尽力把内存清零（ctypes memset）。"""
    try:
        n = len(buf)
        addr = (ctypes.c_char * n).from_buffer(buf)
        ctypes.memset(addr, 0, n)
    except (TypeError, BufferError):
        # 不可变 bytes 无法原地清零，仅解除引用
        pass
