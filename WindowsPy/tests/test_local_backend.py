"""本地文件夹存储后端单元测试。

用 tmp_path fixture 建临时后端根目录，验证各方法语义；与 test_webdav_backend.py
共享同一组契约用例（见 test_backend_contract.py）。
"""

import os

import pytest

from cloudprism.storage.backend import RemoteEntry
from cloudprism.storage.local_backend import LocalFolderBackend


@pytest.fixture
def backend(tmp_path) -> LocalFolderBackend:
    """临时本地文件夹后端。"""
    return LocalFolderBackend(tmp_path)


class TestLocalFolderBackendBasic:
    """基本读写与目录。"""

    def test_list_dir_empty(self, backend):
        """空目录列表为空。"""
        assert backend.list_dir("/") == []

    def test_list_dir_after_write(self, backend):
        """写入文件后能列出。"""
        # 直接写一个文件到后端根
        (backend.root / "a.cpenc").write_bytes(b"hello")
        (backend.root / "sub").mkdir()
        entries = backend.list_dir("/")
        names = {e.name for e in entries}
        assert names == {"a.cpenc", "sub"}
        # is_dir / size 正确
        a = next(e for e in entries if e.name == "a.cpenc")
        assert a.is_dir is False
        assert a.size == 5
        sub = next(e for e in entries if e.name == "sub")
        assert sub.is_dir is True

    def test_get_size(self, backend):
        (backend.root / "f.bin").write_bytes(b"\x00" * 123)
        assert backend.get_size("f.bin") == 123

    def test_get_size_missing_raises(self, backend):
        with pytest.raises(FileNotFoundError):
            backend.get_size("nope.bin")

    def test_exists(self, backend):
        (backend.root / "f.bin").write_bytes(b"x")
        assert backend.exists("f.bin") is True
        assert backend.exists("nope") is False

    def test_mkdir(self, backend):
        backend.mkdir("a/b/c")
        assert (backend.root / "a" / "b" / "c").is_dir()

    def test_rename(self, backend):
        (backend.root / "old").write_bytes(b"data")
        backend.rename("old", "new")
        assert not (backend.root / "old").exists()
        assert (backend.root / "new").read_bytes() == b"data"

    def test_delete(self, backend):
        (backend.root / "f.bin").write_bytes(b"x")
        (backend.root / "d").mkdir()
        backend.delete("f.bin")
        backend.delete("d")
        assert not (backend.root / "f.bin").exists()
        assert not (backend.root / "d").exists()


class TestLocalFolderBackendDownloadRange:
    """字节范围读取。"""

    def test_download_range_full(self, backend):
        (backend.root / "f.bin").write_bytes(bytes(range(256)))
        assert backend.download_range("f.bin", 0, 255) == bytes(range(256))

    def test_download_range_partial(self, backend):
        (backend.root / "f.bin").write_bytes(bytes(range(256)))
        assert backend.download_range("f.bin", 10, 20) == bytes(range(10, 21))

    def test_download_range_single_byte(self, backend):
        (backend.root / "f.bin").write_bytes(b"ABCDE")
        assert backend.download_range("f.bin", 2, 2) == b"C"

    def test_download_range_end_beyond_file(self, backend):
        """end 超过文件末尾时截断到末尾。"""
        (backend.root / "f.bin").write_bytes(b"ABCDE")
        assert backend.download_range("f.bin", 3, 100) == b"DE"

    def test_download_range_start_at_end(self, backend):
        """start 等于文件大小时返回空。"""
        (backend.root / "f.bin").write_bytes(b"ABCDE")
        assert backend.download_range("f.bin", 5, 10) == b""

    def test_download_range_invalid_raises(self, backend):
        (backend.root / "f.bin").write_bytes(b"ABCDE")
        with pytest.raises(ValueError):
            backend.download_range("f.bin", -1, 3)
        with pytest.raises(ValueError):
            backend.download_range("f.bin", 5, 3)


class TestLocalFolderBackendUpload:
    """分块上传（本地后端即复制）。"""

    def test_upload_full_file(self, backend, tmp_path):
        src = tmp_path / "src.bin"
        src.write_bytes(b"\x00" * 1024)
        progress = list(backend.upload_chunked(str(src), "dst.bin", chunk=256))
        assert (backend.root / "dst.bin").read_bytes() == b"\x00" * 1024
        # 进度单调递增，末尾到 1.0
        assert progress[-1] == 1.0
        assert all(0 <= p <= 1 for p in progress)

    def test_upload_empty_file(self, backend, tmp_path):
        src = tmp_path / "empty.bin"
        src.write_bytes(b"")
        progress = list(backend.upload_chunked(str(src), "empty.bin"))
        assert (backend.root / "empty.bin").read_bytes() == b""
        assert progress[-1] == 1.0

    def test_upload_resume(self, backend, tmp_path):
        """断点续传：远端已有部分内容时从偏移续传。"""
        src = tmp_path / "src.bin"
        full = bytes(range(256))
        src.write_bytes(full)
        # 预先写入前 100 字节
        dst = backend.root / "dst.bin"
        dst.write_bytes(full[:100])
        list(backend.upload_chunked(str(src), "dst.bin", chunk=64))
        assert dst.read_bytes() == full


class TestLocalFolderBackendSecurity:
    """路径越界防护。"""

    def test_path_traversal_rejected(self, backend):
        with pytest.raises(ValueError):
            backend._resolve("../../etc/passwd")

    def test_path_traversal_in_list(self, backend):
        with pytest.raises(ValueError):
            backend.list_dir("../")
