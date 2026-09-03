"""跨端互操作测试。

验证 Windows 端加密的文件可被「模拟 Android 端」解密，反之亦然。
由于两端共享同一套二进制协议（见 Plan/Plan.md §2），互操作测试的核心是：

  1. 用 Windows 端的 Encryptor 加密 -> 用 AesCtrStreamCipher（模拟 Android）解密
  2. 用「模拟 Android」的加密流程加密 -> 用 Windows 端的 Decryptor 解密
  3. Vault Marker 的 create/verify 跨端一致性

注意：真正的 Android 端尚未实现，本测试用「独立于 Encryptor/Decryptor 的
原始密码学原语」模拟 Android 端行为，验证二进制协议兼容。
"""

from __future__ import annotations

import os
from io import BytesIO

import pytest
from Crypto.Cipher import AES
from Crypto.Util import Counter
from Crypto.Random import get_random_bytes

from cloudprism import constants
from cloudprism.core.session import Session
from cloudprism.core.encryptor import Encryptor
from cloudprism.core.decryptor import Decryptor
from cloudprism.crypto.header import FileHeader
from cloudprism.crypto.stream_cipher import AesCtrStreamCipher
from cloudprism.crypto.vault import VaultMarker, VaultMetadata
from cloudprism.crypto.kdf import Kdf
from cloudprism.storage.local_backend import LocalFolderBackend


# ---------------------------------------------------------------------------
# 模拟 Android 端的加密/解密原语（独立实现，不依赖 Encryptor/Decryptor）
# ---------------------------------------------------------------------------


def android_encrypt(
    plaintext: bytes, master_password: str
) -> bytes:
    """模拟 Android 端加密：生成 .cpenc 文件字节。

    使用与 Windows 端完全相同的协议参数（PBKDF2 迭代数、AES-CTR 计数器方案等）。
    """
    salt = get_random_bytes(constants.SALT_LEN)
    iv = get_random_bytes(constants.IV_LEN)
    key = Kdf.derive_key(master_password, salt)

    # 构建文件头
    header = FileHeader.build(salt, iv)

    # AES-CTR 加密（与 Windows 端同一计数器方案）
    initial_value = int.from_bytes(iv, "big")
    ctr = Counter.new(128, initial_value=initial_value, allow_wraparound=True)
    cipher = AES.new(key, AES.MODE_CTR, counter=ctr)
    ciphertext = cipher.encrypt(plaintext)

    return header + ciphertext


def android_decrypt(cipher_file: bytes, master_password: str) -> bytes:
    """模拟 Android 端解密：从 .cpenc 文件字节还原明文。"""
    bio = BytesIO(cipher_file)
    header = FileHeader.parse(bio)
    key = Kdf.derive_key(master_password, header.salt)

    # 读取密文主体（跳过文件头）
    ciphertext = cipher_file[header.header_length:]

    # AES-CTR 解密（与 Windows 端同一计数器方案）
    initial_value = int.from_bytes(header.iv, "big")
    ctr = Counter.new(128, initial_value=initial_value, allow_wraparound=True)
    cipher = AES.new(key, AES.MODE_CTR, counter=ctr)
    return cipher.decrypt(ciphertext)


# ---------------------------------------------------------------------------
# 互操作测试
# ---------------------------------------------------------------------------


class TestInteropWindowsToAndroid:
    """Windows 加密 -> Android 解密。"""

    def test_small_file_roundtrip(self, tmp_path):
        """小文件：Windows Encryptor 加密 -> 模拟 Android 解密。"""
        # 准备明文
        plaintext = b"Hello from Windows! This is a cross-platform test."
        plain_file = tmp_path / "plain.txt"
        plain_file.write_bytes(plaintext)

        # Windows 端加密
        storage_dir = tmp_path / "storage"
        storage_dir.mkdir()
        backend = LocalFolderBackend(str(storage_dir))
        session = Session("cross_platform_password")
        encryptor = Encryptor(session, backend)
        cipher_bytes = encryptor.encrypt_to_bytes(str(plain_file))

        # 模拟 Android 端解密
        decrypted = android_decrypt(cipher_bytes, "cross_platform_password")
        assert decrypted == plaintext

        session.close()

    def test_large_file_roundtrip(self, tmp_path):
        """大文件（跨多个 AES 块）：验证 CTR 计数器方案一致。"""
        # 4096 字节 = 256 个 AES 块，足以覆盖边界情况
        plaintext = os.urandom(4096)
        plain_file = tmp_path / "large.bin"
        plain_file.write_bytes(plaintext)

        storage_dir = tmp_path / "storage"
        storage_dir.mkdir()
        backend = LocalFolderBackend(str(storage_dir))
        session = Session("large_file_test")
        encryptor = Encryptor(session, backend)
        cipher_bytes = encryptor.encrypt_to_bytes(str(plain_file))

        decrypted = android_decrypt(cipher_bytes, "large_file_test")
        assert decrypted == plaintext

        session.close()

    def test_empty_file_roundtrip(self, tmp_path):
        """空文件：验证边界情况。"""
        plain_file = tmp_path / "empty.bin"
        plain_file.write_bytes(b"")

        storage_dir = tmp_path / "storage"
        storage_dir.mkdir()
        backend = LocalFolderBackend(str(storage_dir))
        session = Session("empty_test")
        encryptor = Encryptor(session, backend)
        cipher_bytes = encryptor.encrypt_to_bytes(str(plain_file))

        decrypted = android_decrypt(cipher_bytes, "empty_test")
        assert decrypted == b""

        session.close()


class TestInteropAndroidToWindows:
    """Android 加密 -> Windows 解密。"""

    def test_small_file_roundtrip(self, tmp_path):
        """小文件：模拟 Android 加密 -> Windows Decryptor 解密。"""
        plaintext = b"Hello from Android! Cross-platform interop test."
        master_pw = "android_to_windows_pw"

        # 模拟 Android 端加密
        cipher_bytes = android_encrypt(plaintext, master_pw)

        # 写入后端存储
        storage_dir = tmp_path / "storage"
        storage_dir.mkdir()
        cipher_path = storage_dir / "test.cpenc"
        cipher_path.write_bytes(cipher_bytes)

        backend = LocalFolderBackend(str(storage_dir))
        session = Session(master_pw)
        decryptor = Decryptor(session, backend)

        # Windows 端解密
        output_file = tmp_path / "output.txt"
        for _ in decryptor.download_and_decrypt("test.cpenc", str(output_file)):
            pass
        assert output_file.read_bytes() == plaintext

        session.close()

    def test_large_file_roundtrip(self, tmp_path):
        """大文件：模拟 Android 加密 -> Windows Decryptor 解密。"""
        plaintext = os.urandom(8192)
        master_pw = "large_android_test"

        cipher_bytes = android_encrypt(plaintext, master_pw)

        storage_dir = tmp_path / "storage"
        storage_dir.mkdir()
        cipher_path = storage_dir / "large.cpenc"
        cipher_path.write_bytes(cipher_bytes)

        backend = LocalFolderBackend(str(storage_dir))
        session = Session(master_pw)
        decryptor = Decryptor(session, backend)

        output_file = tmp_path / "output.bin"
        for _ in decryptor.download_and_decrypt("large.cpenc", str(output_file)):
            pass
        assert output_file.read_bytes() == plaintext

        session.close()


class TestInteropVaultMarker:
    """Vault Marker 跨端一致性测试。"""

    def test_create_and_verify(self):
        """Windows 创建 Vault Marker -> 模拟 Android 校验。"""
        password = "vault_interop_test"
        meta = VaultMarker.generate_metadata(filename_enc=True)
        marker_bytes = VaultMarker.create(meta, password)

        # 模拟 Android 端校验（使用相同的 VaultMarker.verify，
        # 因为 Android 端将使用完全相同的算法）
        result = VaultMarker.verify(marker_bytes, password)
        assert result is not None
        assert result.filename_enc is True
        assert result.vault_id == meta.vault_id
        assert result.salt == meta.salt
        assert result.iv == meta.iv

    def test_wrong_password_returns_none(self):
        """错误密码应返回 None。"""
        meta = VaultMarker.generate_metadata(filename_enc=False)
        marker_bytes = VaultMarker.create(meta, "correct_password")
        result = VaultMarker.verify(marker_bytes, "wrong_password")
        assert result is None

    def test_kdf_deterministic(self):
        """KDF 在两端必须产出相同密钥（相同密码+盐）。"""
        password = "deterministic_test"
        salt = b"\x01" * constants.SALT_LEN
        key1 = Kdf.derive_key(password, salt)
        key2 = Kdf.derive_key(password, salt)
        assert key1 == key2
        assert len(key1) == constants.KEY_LEN


class TestInteropStreamCipher:
    """AES-CTR 流式解密跨端一致性。"""

    def test_random_access_matches_full_decrypt(self):
        """AesCtrStreamCipher 的随机访问解密应与全量 CTR 解密一致。"""
        key = get_random_bytes(constants.KEY_LEN)
        iv = get_random_bytes(constants.IV_LEN)
        plaintext = os.urandom(1024)

        # 全量 CTR 加密（模拟加密端）
        initial_value = int.from_bytes(iv, "big")
        ctr = Counter.new(128, initial_value=initial_value, allow_wraparound=True)
        cipher = AES.new(key, AES.MODE_CTR, counter=ctr)
        ciphertext = cipher.encrypt(plaintext)

        # AesCtrStreamCipher 随机访问解密（模拟 Windows 端流式代理）
        sc = AesCtrStreamCipher(key, iv)

        # 测试多个随机区间
        import random
        random.seed(42)  # 可重现
        for _ in range(20):
            start = random.randint(0, len(plaintext) - 1)
            end = random.randint(start + 1, min(start + 256, len(plaintext)))
            # 计算需要的密文区间（块对齐）
            BS = 16
            first_block = start // BS
            last_block = (end - 1) // BS
            ct_start = first_block * BS
            ct_end = (last_block + 1) * BS
            ct_slice = ciphertext[ct_start:ct_end]
            result = sc.decrypt_range(ct_slice, first_block, start, end)
            assert result == plaintext[start:end], (
                f"区间 [{start}, {end}) 解密不一致"
            )
