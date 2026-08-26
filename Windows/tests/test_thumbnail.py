"""缩略图获取与缓存（thumbnail）单元测试。

真实加密上传管线（Encryptor）+ 本地后端：头部解密出图、
两级缓存命中、磁盘缓存加密落盘、异常回退。
"""

from __future__ import annotations

import pytest

pytest.importorskip("PySide6")

from PySide6.QtGui import QImage

from cloudprism.core.encryptor import Encryptor
from cloudprism.core.session import Session
from cloudprism.core.thumbnail import ThumbnailCache, fetch_thumbnail
from cloudprism.storage.local_backend import LocalFolderBackend

MASTER_PW = "thumb_test_pw"


@pytest.fixture
def session():
    return Session(MASTER_PW)


@pytest.fixture
def backend(tmp_path):
    root = tmp_path / "backend"
    root.mkdir()
    return LocalFolderBackend(root)


@pytest.fixture
def png_path(tmp_path):
    """生成一张小 PNG（QImage，测试环境无需外部素材）。"""
    img = QImage(8, 8, QImage.Format.Format_RGB32)
    img.fill(0xFF336699)
    path = tmp_path / "pic.png"
    assert img.save(str(path), "PNG")
    return path


def upload_encrypted(session, backend, local_path, remote="pic.png.cpenc"):
    """走真实加密上传管线。"""
    for _ in Encryptor(session, backend).encrypt_and_upload(
        str(local_path), remote
    ):
        pass


# ---------------------------------------------------------------------------
# 头部解密出图
# ---------------------------------------------------------------------------


class TestFetch:
    def test_header_decrypt_yields_image(self, session, backend, png_path):
        """远端加密图片头部解密后可被 QImage 解析。"""
        upload_encrypted(session, backend, png_path)
        data = fetch_thumbnail(session, backend, "pic.png.cpenc")
        assert data
        img = QImage()
        assert img.loadFromData(data)
        assert img.width() == 8

    def test_missing_remote_returns_none(self, session, backend):
        """远端不存在：解密失败静默返回 None。"""
        assert fetch_thumbnail(session, backend, "nope.cpenc") is None

    def test_non_image_bytes_returned_but_unloadable(
        self, session, backend, tmp_path
    ):
        """非图片文件：返回头部字节但 QImage 无法解析（调用方判空回退）。"""
        bin_path = tmp_path / "data.bin"
        bin_path.write_bytes(bytes(range(256)) * 4)
        upload_encrypted(session, backend, bin_path, "data.bin.cpenc")
        data = fetch_thumbnail(session, backend, "data.bin.cpenc")
        assert data
        assert not QImage().loadFromData(data)


# ---------------------------------------------------------------------------
# 两级缓存
# ---------------------------------------------------------------------------


class TestCache:
    def test_memory_cache_hit(self, session, backend, png_path):
        """首次解密后缓存；远端文件删除后仍可命中。"""
        upload_encrypted(session, backend, png_path)
        cache = ThumbnailCache(session)
        first = fetch_thumbnail(session, backend, "pic.png.cpenc", cache=cache)
        assert first

        backend.delete("pic.png.cpenc")
        assert not backend.exists("pic.png.cpenc")
        second = fetch_thumbnail(session, backend, "pic.png.cpenc", cache=cache)
        assert second == first

    def test_disk_cache_hit_after_memory_eviction(
        self, session, backend, png_path, tmp_path
    ):
        """内存缓存被逐出后，磁盘缓存解密回填。"""
        upload_encrypted(session, backend, png_path)
        cache_dir = str(tmp_path / "thumb_cache")
        cache = ThumbnailCache(session, cache_dir=cache_dir)
        first = fetch_thumbnail(session, backend, "pic.png.cpenc", cache=cache)

        cache._mem.clear()  # 模拟内存缓存逐出
        second = fetch_thumbnail(session, backend, "pic.png.cpenc", cache=cache)
        assert second == first

    def test_disk_cache_is_encrypted(self, session, backend, png_path, tmp_path):
        """磁盘缓存落盘为密文：不含 PNG 魔数等明文特征。"""
        upload_encrypted(session, backend, png_path)
        cache_dir = str(tmp_path / "thumb_cache")
        cache = ThumbnailCache(session, cache_dir=cache_dir)
        data = fetch_thumbnail(session, backend, "pic.png.cpenc", cache=cache)
        assert data[:8] == b"\x89PNG\r\n\x1a\n"

        key = ThumbnailCache.key_for("pic.png.cpenc")
        disk_file = tmp_path / "thumb_cache" / f"{key}.cthumb"
        assert disk_file.exists()
        blob = disk_file.read_bytes()
        assert b"\x89PNG" not in blob

    def test_corrupted_disk_cache_returns_none(self, session, tmp_path):
        """磁盘缓存文件损坏：读失败返回 None，不抛异常。"""
        cache_dir = str(tmp_path / "thumb_cache")
        cache = ThumbnailCache(session, cache_dir=cache_dir)
        cache.put("x.cpenc", b"some-thumb")
        key = ThumbnailCache.key_for("x.cpenc")
        (tmp_path / "thumb_cache" / f"{key}.cthumb").write_bytes(b"broken")
        cache._mem.clear()
        assert cache.get("x.cpenc") is None

    def test_memory_lru_eviction(self, session, tmp_path):
        """内存 LRU 超上限逐出最旧条目。"""
        cache = ThumbnailCache(session, cache_dir=str(tmp_path / "c"), max_memory=2)
        cache.put("a", b"1")
        cache.put("b", b"2")
        cache.put("c", b"3")
        assert len(cache._mem) == 2
        assert ThumbnailCache.key_for("a") not in cache._mem
