"""存储后端契约测试。

两类后端（LocalFolderBackend / WebDavBackend）跑同一组用例，验证
StorageBackend 接口语义一致，可无缝互换。上层依赖该契约，不感知后端类型。
"""

from __future__ import annotations

import pytest
import requests

from cloudprism.storage.local_backend import LocalFolderBackend
from cloudprism.storage.webdav_backend import WebDavBackend

# 复用 WebDAV 测试中的内存 mock adapter
from test_webdav_backend import MockWebDavAdapter


# ---------------------------------------------------------------------------
# fixture：返回两类后端，对同一组操作应表现一致
# ---------------------------------------------------------------------------


def _local_backend(root) -> LocalFolderBackend:
    return LocalFolderBackend(root)


def _webdav_backend() -> WebDavBackend:
    adapter = MockWebDavAdapter()
    session = requests.Session()
    session.mount(MockWebDavAdapter.BASE, adapter)
    return WebDavBackend(MockWebDavAdapter.BASE, auth=("u", "p"), session=session)


@pytest.fixture(params=["local", "webdav"])
def backend(request, tmp_path):
    """参数化 fixture：同一套用例跑本地与 WebDAV 两类后端。

    本地后端使用 tmp_path 下的专用子目录作为根，避免与源文件混淆。
    """
    if request.param == "local":
        root = tmp_path / "backend_root"
        root.mkdir()
        return _local_backend(root)
    return _webdav_backend()


@pytest.fixture
def sample_file_content() -> bytes:
    """256 字节测试数据。"""
    return bytes(range(256))


# ---------------------------------------------------------------------------
# 契约用例：两类后端必须行为一致
# ---------------------------------------------------------------------------


class TestBackendContract:
    """StorageBackend 契约一致性。"""

    def test_list_empty_dir(self, backend):
        """空根目录列表为空。"""
        assert backend.list_dir("/") == []

    def test_upload_then_list(self, backend, tmp_path, sample_file_content):
        """上传文件后能列出，且元信息正确。"""
        src = tmp_path / "src.bin"
        src.write_bytes(sample_file_content)
        list(backend.upload_chunked(str(src), "data.cpenc", chunk=64))
        entries = backend.list_dir("/")
        assert len(entries) == 1
        e = entries[0]
        assert e.name == "data.cpenc"
        assert e.is_dir is False
        assert e.size == len(sample_file_content)

    def test_get_size_matches_uploaded(
        self, backend, tmp_path, sample_file_content
    ):
        """get_size 与上传文件大小一致。"""
        src = tmp_path / "src.bin"
        src.write_bytes(sample_file_content)
        list(backend.upload_chunked(str(src), "f.bin", chunk=64))
        assert backend.get_size("f.bin") == len(sample_file_content)

    def test_download_range_full(self, backend, tmp_path, sample_file_content):
        """整文件下载还原。"""
        src = tmp_path / "src.bin"
        src.write_bytes(sample_file_content)
        list(backend.upload_chunked(str(src), "f.bin", chunk=64))
        assert backend.download_range("f.bin", 0, 255) == sample_file_content

    def test_download_range_partial(self, backend, tmp_path, sample_file_content):
        """部分范围下载正确。"""
        src = tmp_path / "src.bin"
        src.write_bytes(sample_file_content)
        list(backend.upload_chunked(str(src), "f.bin", chunk=64))
        assert backend.download_range("f.bin", 50, 99) == sample_file_content[50:100]

    def test_download_range_single_byte(self, backend, tmp_path, sample_file_content):
        """单字节下载正确。"""
        src = tmp_path / "src.bin"
        src.write_bytes(sample_file_content)
        list(backend.upload_chunked(str(src), "f.bin", chunk=64))
        assert backend.download_range("f.bin", 100, 100) == sample_file_content[100:101]

    def test_exists_after_upload(self, backend, tmp_path, sample_file_content):
        """上传后存在性判断为真。"""
        src = tmp_path / "src.bin"
        src.write_bytes(sample_file_content)
        list(backend.upload_chunked(str(src), "f.bin", chunk=64))
        assert backend.exists("f.bin") is True
        assert backend.exists("nope.bin") is False

    def test_head_equals_get_size(self, backend, tmp_path, sample_file_content):
        """head 与 get_size 结果一致（断点续传基准）。"""
        src = tmp_path / "src.bin"
        src.write_bytes(sample_file_content)
        list(backend.upload_chunked(str(src), "f.bin", chunk=64))
        assert backend.head("f.bin") == backend.get_size("f.bin")

    def test_mkdir_list_subdir(self, backend):
        """创建目录后能列出。"""
        backend.mkdir("subdir")
        entries = backend.list_dir("/")
        assert any(e.name == "subdir" and e.is_dir for e in entries)

    def test_rename(self, backend, tmp_path, sample_file_content):
        """重命名后旧名不存在、新名可读。"""
        src = tmp_path / "src.bin"
        src.write_bytes(sample_file_content)
        list(backend.upload_chunked(str(src), "old.bin", chunk=64))
        backend.rename("old.bin", "new.bin")
        assert backend.exists("old.bin") is False
        assert backend.exists("new.bin") is True
        assert backend.get_size("new.bin") == len(sample_file_content)

    def test_delete(self, backend, tmp_path, sample_file_content):
        """删除后不再存在。"""
        src = tmp_path / "src.bin"
        src.write_bytes(sample_file_content)
        list(backend.upload_chunked(str(src), "f.bin", chunk=64))
        backend.delete("f.bin")
        assert backend.exists("f.bin") is False

    def test_upload_progress_monotonic(
        self, backend, tmp_path, sample_file_content
    ):
        """上传进度单调非减且末值 1.0。"""
        src = tmp_path / "src.bin"
        src.write_bytes(sample_file_content)
        progress = list(backend.upload_chunked(str(src), "f.bin", chunk=32))
        assert progress[-1] == pytest.approx(1.0)
        # 单调非减
        for a, b in zip(progress, progress[1:]):
            assert b >= a - 1e-9

    def test_upload_resume_completes(
        self, backend, tmp_path, sample_file_content
    ):
        """断点续传：预置部分内容后续传完成，最终内容完整。"""
        src = tmp_path / "src.bin"
        src.write_bytes(sample_file_content)
        # 直接预置前半段（绕过 upload，模拟已传部分）
        if hasattr(backend, "root"):
            # 本地后端：直接写文件
            (backend.root / "dst.bin").write_bytes(sample_file_content[:100])
        else:
            # WebDAV 后端：直接放内存
            backend.session.adapters[MockWebDavAdapter.BASE].files["/dst.bin"] = (
                sample_file_content[:100]
            )
        list(backend.upload_chunked(str(src), "dst.bin", chunk=64))
        assert backend.download_range("dst.bin", 0, len(sample_file_content) - 1) == (
            sample_file_content
        )
