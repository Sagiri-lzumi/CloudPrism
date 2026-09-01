"""加解密管线端到端单元测试。

验证 Encryptor 加密 -> 上传 -> Decryptor 下载 -> 解密 还原明文。
用 LocalFolderBackend（内存文件系统）与 WebDAV mock 两类后端各跑一遍。
"""

from __future__ import annotations

import os

import pytest

from cloudprism.core.decryptor import Decryptor
from cloudprism.core.encryptor import Encryptor
from cloudprism.core.session import Session
from cloudprism.crypto.header import FileHeader
from cloudprism.storage.local_backend import LocalFolderBackend
from cloudprism.storage.webdav_backend import WebDavBackend

from test_webdav_backend import MockWebDavAdapter


# ---------------------------------------------------------------------------
# fixtures
# ---------------------------------------------------------------------------


MASTER_PW = "master_pw_2024"


@pytest.fixture
def session():
    """主密码会话。"""
    return Session(MASTER_PW)


def _local_backend(root) -> LocalFolderBackend:
    return LocalFolderBackend(root)


def _webdav_backend() -> WebDavBackend:
    import requests

    adapter = MockWebDavAdapter()
    session = requests.Session()
    session.mount(MockWebDavAdapter.BASE, adapter)
    return WebDavBackend(MockWebDavAdapter.BASE, auth=("u", "p"), session=session)


@pytest.fixture(params=["local", "webdav"])
def backend(request, tmp_path):
    """参数化：本地与 WebDAV 两类后端。"""
    if request.param == "local":
        root = tmp_path / "backend"
        root.mkdir()
        return _local_backend(root)
    return _webdav_backend()


# ---------------------------------------------------------------------------
# 测试数据
# ---------------------------------------------------------------------------


SAMPLE_SIZES = [0, 1, 15, 16, 17, 255, 256, 4096, 10000]


# ---------------------------------------------------------------------------
# 测试
# ---------------------------------------------------------------------------


class TestEncryptDecryptRoundtrip:
    """加密上传 -> 下载解密 还原明文（两类后端）。"""

    @pytest.mark.parametrize("size", SAMPLE_SIZES)
    def test_roundtrip_local_to_backend(self, session, backend, tmp_path, size):
        """端到端：本地明文 -> 加密上传 -> 下载解密 -> 比对明文。"""
        # 1. 准备明文
        plaintext = os.urandom(size)
        src = tmp_path / "plain.bin"
        src.write_bytes(plaintext)

        # 2. 加密上传
        enc = Encryptor(session, backend, chunk=256)
        progress = list(enc.encrypt_and_upload(str(src), "stored.cpenc"))
        if size > 0:
            assert progress[-1] == pytest.approx(1.0)
        assert backend.exists("stored.cpenc")

        # 3. 下载解密
        dec = Decryptor(session, backend, chunk=256)
        out = tmp_path / "out.bin"
        list(dec.download_and_decrypt("stored.cpenc", str(out)))

        # 4. 比对明文
        assert out.read_bytes() == plaintext

    def test_encrypt_produces_valid_header(self, session, backend, tmp_path):
        """加密产物以合法文件头开头（魔数）。"""
        src = tmp_path / "plain.bin"
        src.write_bytes(b"hello world")
        enc = Encryptor(session, backend)
        # 用 encrypt_to_bytes 直接拿密文
        data = enc.encrypt_to_bytes(str(src))
        assert data[:8] == FileHeader.MAGIC
        # 头部可解析
        header = FileHeader.parse_bytes(data)
        assert header.version == 1
        assert header.header_length == 51

    def test_ciphertext_length_equals_plaintext(self, session, backend, tmp_path):
        """流加密无填充：密文长度 = 头部长度 + 明文长度。"""
        plaintext = b"x" * 1000
        src = tmp_path / "plain.bin"
        src.write_bytes(plaintext)
        enc = Encryptor(session, backend)
        data = enc.encrypt_to_bytes(str(src))
        assert len(data) == 51 + len(plaintext)


class TestParallelEncrypt:
    """多核并行加密（ProcessPoolExecutor 路径）。"""

    def test_parallel_roundtrip_and_progress(self, session, tmp_path):
        """≥8MB 文件 max_workers=2：加密上传 -> 下载解密还原明文；
        并行路径进度序列单调不减且终值 1.0。"""
        root = tmp_path / "backend"
        root.mkdir()
        backend = _local_backend(root)
        plaintext = os.urandom(8 * 1024 * 1024)  # 8MB：触发并行分段（每段至少 4MB）
        src = tmp_path / "big.bin"
        src.write_bytes(plaintext)

        # 加密上传（多进程路径）
        enc = Encryptor(session, backend)
        progress = list(
            enc.encrypt_and_upload(str(src), "big.cpenc", max_workers=2)
        )
        assert backend.exists("big.cpenc")
        # 进度单调不减、终值 1.0（各值均在 0~1 区间）
        assert all(0.0 <= p <= 1.0 for p in progress)
        assert progress == sorted(progress)
        assert progress[-1] == pytest.approx(1.0)

        # 下载解密还原明文（分段拼接密文与流式密文格式等价）
        dec = Decryptor(session, backend)
        out = tmp_path / "out.bin"
        list(dec.download_and_decrypt("big.cpenc", str(out)))
        assert out.read_bytes() == plaintext

    def test_parallel_unavailable_falls_back_to_sequential(
        self, session, tmp_path, monkeypatch
    ):
        """并行机制不可用（Pipe 被环境拒绝，WinError 5）时降级单核流式：
        加密上传仍成功且解密还原明文（打包态受管环境实测根因的回归）。
        """
        import concurrent.futures

        class _BrokenPool:
            def __init__(self, *args, **kwargs):
                # 复刻实测异常：创建进程间管道被拒绝访问
                raise PermissionError(5, "拒绝访问")

        monkeypatch.setattr(concurrent.futures, "ProcessPoolExecutor", _BrokenPool)

        root = tmp_path / "backend"
        root.mkdir()
        backend = _local_backend(root)
        plaintext = os.urandom(8 * 1024 * 1024)  # ≥8MB：本应走并行路径
        src = tmp_path / "big.bin"
        src.write_bytes(plaintext)

        enc = Encryptor(session, backend)
        progress = list(
            enc.encrypt_and_upload(str(src), "big.cpenc", max_workers=2)
        )
        assert backend.exists("big.cpenc")
        assert progress[-1] == pytest.approx(1.0)

        dec = Decryptor(session, backend)
        out = tmp_path / "out.bin"
        list(dec.download_and_decrypt("big.cpenc", str(out)))
        assert out.read_bytes() == plaintext


class TestDecryptorRangeAccess:
    """Decryptor 的随机范围解密（流式代理基础）。"""

    def test_decrypt_range_full(self, session, backend, tmp_path):
        """decrypt_range_to_bytes 整文件解密。"""
        plaintext = bytes(range(256)) * 20     # 5120 字节
        src = tmp_path / "plain.bin"
        src.write_bytes(plaintext)
        enc = Encryptor(session, backend, chunk=256)
        list(enc.encrypt_and_upload(str(src), "f.cpenc"))

        dec = Decryptor(session, backend)
        pt = dec.decrypt_range_to_bytes("f.cpenc")
        assert pt == plaintext

    def test_decrypt_range_partial(self, session, backend, tmp_path):
        """随机区间解密与原明文一致。"""
        plaintext = bytes(range(256)) * 20
        src = tmp_path / "plain.bin"
        src.write_bytes(plaintext)
        enc = Encryptor(session, backend, chunk=256)
        list(enc.encrypt_and_upload(str(src), "f.cpenc"))

        dec = Decryptor(session, backend)
        for start, end in [(0, 100), (100, 200), (1000, 1500), (5000, 5120), (255, 257)]:
            pt = dec.decrypt_range_to_bytes("f.cpenc", start=start, end=end)
            assert pt == plaintext[start:end], f"区间 [{start},{end}) 不符"


class TestSessionKeyCaching:
    """Session 密钥派生缓存。"""

    def test_same_salt_cached(self, session):
        """同 salt 多次派生只算一次。"""
        salt = b"\x00" * 16
        k1 = session.derive_key(salt)
        k2 = session.derive_key(salt)
        assert k1 == k2
        # 缓存命中（内部字典有该 salt）
        assert salt in session._key_cache

    def test_close_clears(self, session):
        """close() 清空缓存与密码。"""
        session.derive_key(b"\x00" * 16)
        assert session._key_cache
        session.close()
        assert not session._key_cache


class TestWrongPasswordFails:
    """错误密码解密失败。"""

    def test_wrong_password_garbled(self, tmp_path, backend):
        """A 密码加密，B 密码解密 -> 明文不符（CTR 无标签，静默错乱）。"""
        # 用 session A 加密
        sess_a = Session("password_A")
        plaintext = b"sensitive data here"
        src = tmp_path / "plain.bin"
        src.write_bytes(plaintext)
        enc = Encryptor(sess_a, backend)
        list(enc.encrypt_and_upload(str(src), "f.cpenc"))

        # 用 session B 解密
        sess_b = Session("password_B")
        out = tmp_path / "out.bin"
        dec = Decryptor(sess_b, backend)
        list(dec.download_and_decrypt("f.cpenc", str(out)))
        # CTR 模式无认证，错误密钥产生错乱明文（非原文）
        assert out.read_bytes() != plaintext
