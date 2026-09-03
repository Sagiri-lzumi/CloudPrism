"""WebDAV 存储后端单元测试。

使用自研轻量 requests Adapter（MockWebDavAdapter）在进程内模拟 WebDAV 服务器，
验证 PROPFIND 解析、Range 下载、分块上传续传、MKCOL/MOVE/DELETE 等方法。
无外部依赖，不监听真实端口。
"""

from __future__ import annotations

from urllib.parse import quote, unquote

import pytest
import requests
from requests.adapters import HTTPAdapter
from requests.models import Response

from cloudprism.storage.webdav_backend import WebDavBackend


# ---------------------------------------------------------------------------
# 进程内 WebDAV mock adapter
# ---------------------------------------------------------------------------


class MockWebDavAdapter(HTTPAdapter):
    """把 WebDAV HTTP 请求映射到内存文件系统的 mock adapter。

    维护 files: dict[path, bytes] 与 dirs: set[path]，模拟最小 WebDAV 语义。
    """

    BASE = "http://mock/"      # mock 基址

    def __init__(self) -> None:
        super().__init__()
        self.files: dict[str, bytes] = {}
        self.dirs: set[str] = {"/"}     # 根目录始终存在

    # ---- 辅助方法 ----

    def _path_from_url(self, url: str) -> str:
        """从 URL 提取相对路径（已解码）。"""
        rel = url.replace(self.BASE, "").strip()
        if not rel:
            return "/"
        return "/" + unquote(rel).strip("/")

    def _parent(self, path: str) -> str:
        """父目录路径。"""
        p = path.rstrip("/")
        if "/" not in p:
            return "/"
        return p.rsplit("/", 1)[0] or "/"

    def _ensure_parents(self, path: str) -> None:
        """确保父目录存在（mock 简化：自动建）。"""
        parts = [p for p in path.strip("/").split("/") if p][:-1]
        cur = ""
        for part in parts:
            cur += "/" + part
            self.dirs.add(cur)

    def _make_response(
        self, status: int, content: bytes = b"", headers: dict | None = None
    ) -> Response:
        """构造 requests.Response。"""
        r = Response()
        r.status_code = status
        r._content = content
        r.headers.update(headers or {})
        r.url = self.BASE
        r.encoding = "utf-8"
        return r

    def _propfind_response(self, path: str) -> bytes:
        """生成 PROPFIND 多状态 XML。"""
        path = path.rstrip("/") or "/"
        entries = []
        # 列出该目录下的直接子项
        prefix = path.rstrip("/") + "/"
        all_paths = set(self.files) | self.dirs
        for p in sorted(all_paths):
            if p == path:
                continue
            if p.startswith(prefix) and "/" not in p[len(prefix):]:
                # 直接子项
                name = p.rsplit("/", 1)[-1]
                href = quote(p.lstrip("/"))
                if p in self.dirs:
                    size = 0
                    rt = "<D:collection/>"
                else:
                    size = len(self.files[p])
                    rt = ""
                entries.append(
                    f"<D:response>"
                    f"<D:href>{href}</D:href>"
                    f"<D:propstat><D:prop>"
                    f"<D:resourcetype>{rt}</D:resourcetype>"
                    f"<D:getcontentlength>{size}</D:getcontentlength>"
                    f"</D:prop><D:status>HTTP/1.1 200 OK</D:status>"
                    f"</D:propstat></D:response>"
                )
        xml = (
            '<?xml version="1.0" encoding="utf-8"?>'
            '<D:multistatus xmlns:D="DAV:">'
            + "".join(entries)
            + "</D:multistatus>"
        )
        return xml.encode("utf-8")

    # ---- HTTP 方法分发 ----

    def send(self, request, **kwargs):  # noqa: D401
        method = request.method.upper()
        path = self._path_from_url(request.url)

        if method == "PROPFIND":
            if path not in self.dirs and path not in self.files:
                return self._make_response(404)
            return self._make_response(
                207, self._propfind_response(path),
                {"Content-Type": "application/xml"},
            )

        if method == "HEAD":
            if path in self.files:
                return self._make_response(
                    200, b"", {"Content-Length": str(len(self.files[path]))}
                )
            if path in self.dirs:
                return self._make_response(200)
            return self._make_response(404)

        if method == "GET":
            if path not in self.files:
                return self._make_response(404)
            data = self.files[path]
            rng = request.headers.get("Range")
            if rng:
                # 解析 bytes=start-end
                spec = rng.split("=", 1)[1]
                s, e = spec.split("-")
                s = int(s)
                e = int(e) if e else len(data) - 1
                e = min(e, len(data) - 1)
                chunk = data[s:e + 1]
                return self._make_response(
                    206, chunk,
                    {"Content-Range": f"bytes {s}-{e}/{len(data)}",
                     "Content-Length": str(len(chunk))},
                )
            return self._make_response(200, data, {"Content-Length": str(len(data))})

        if method == "PUT":
            body = request.body or b""
            if isinstance(body, str):
                body = body.encode("utf-8")
            cr = request.headers.get("Content-Range")
            self._ensure_parents(path)
            if cr:
                # 解析 bytes start-end/total（续传）
                spec = cr.split(" ", 1)[1]
                rng, total = spec.split("/")
                s, e = rng.split("-")
                s = int(s)
                e = int(e) if e else 0
                if path not in self.files:
                    self.files[path] = b""
                cur = bytearray(self.files[path])
                if len(cur) < s:
                    cur.extend(b"\x00" * (s - len(cur)))
                cur[s:e + 1] = body
                self.files[path] = bytes(cur)
            else:
                self.files[path] = body
            self.dirs.discard(path)
            return self._make_response(201)

        if method == "MKCOL":
            if path in self.dirs:
                return self._make_response(405)
            self._ensure_parents(path)
            self.dirs.add(path)
            self.files.pop(path, None)
            return self._make_response(201)

        if method == "MOVE":
            dest = request.headers.get("Destination", "")
            new_path = self._path_from_url(dest)
            if path in self.files:
                self.files[new_path] = self.files.pop(path)
            elif path in self.dirs:
                self.dirs.discard(path)
                self.dirs.add(new_path)
            return self._make_response(201)

        if method == "DELETE":
            self.files.pop(path, None)
            self.dirs.discard(path)
            return self._make_response(204)

        return self._make_response(405)


# ---------------------------------------------------------------------------
# fixtures
# ---------------------------------------------------------------------------


@pytest.fixture
def webdav_pair():
    """返回 (backend, adapter)，adapter 持有内存文件系统。"""
    adapter = MockWebDavAdapter()
    session = requests.Session()
    session.mount(MockWebDavAdapter.BASE, adapter)
    backend = WebDavBackend(
        MockWebDavAdapter.BASE, auth=("user", "pw"), session=session
    )
    return backend, adapter


# ---------------------------------------------------------------------------
# 测试
# ---------------------------------------------------------------------------


class TestWebDavBackendList:
    """PROPFIND 列目录。"""

    def test_list_empty(self, webdav_pair):
        backend, _ = webdav_pair
        assert backend.list_dir("/") == []

    def test_list_after_put(self, webdav_pair):
        backend, adapter = webdav_pair
        adapter.files["/a.cpenc"] = b"hello"
        adapter.files["/b.cpenc"] = b"world!!"
        adapter.dirs.add("/sub")
        entries = backend.list_dir("/")
        names = {e.name for e in entries}
        assert names == {"a.cpenc", "b.cpenc", "sub"}
        a = next(e for e in entries if e.name == "a.cpenc")
        assert a.is_dir is False
        assert a.size == 5
        sub = next(e for e in entries if e.name == "sub")
        assert sub.is_dir is True
        assert sub.size == 0

    def test_list_missing_raises(self, webdav_pair):
        backend, _ = webdav_pair
        with pytest.raises(ConnectionError):
            backend.list_dir("/nope")


class TestWebDavBackendSize:
    """HEAD 取大小。"""

    def test_get_size(self, webdav_pair):
        backend, adapter = webdav_pair
        adapter.files["/f.bin"] = b"\x00" * 123
        assert backend.get_size("f.bin") == 123

    def test_get_size_missing(self, webdav_pair):
        backend, _ = webdav_pair
        with pytest.raises(ConnectionError):
            backend.get_size("nope")


class TestWebDavBackendDownload:
    """GET Range 下载。"""

    def test_download_range_full(self, webdav_pair):
        backend, adapter = webdav_pair
        adapter.files["/f.bin"] = bytes(range(256))
        assert backend.download_range("f.bin", 0, 255) == bytes(range(256))

    def test_download_range_partial(self, webdav_pair):
        backend, adapter = webdav_pair
        adapter.files["/f.bin"] = bytes(range(256))
        assert backend.download_range("f.bin", 10, 20) == bytes(range(10, 21))

    def test_download_range_single_byte(self, webdav_pair):
        backend, adapter = webdav_pair
        adapter.files["/f.bin"] = b"ABCDE"
        assert backend.download_range("f.bin", 2, 2) == b"C"

    def test_download_range_invalid_raises(self, webdav_pair):
        backend, _ = webdav_pair
        with pytest.raises(ValueError):
            backend.download_range("f.bin", 5, 3)

    def test_download_range_200_fallback_sliced(self):
        """服务器不支持 Range 回 200 整文件：后端本地切出请求段。

        若原样返回整文件，代理按请求偏移取密文会错位解密。
        """

        class NoRangeAdapter(MockWebDavAdapter):
            """忽略 Range 头（模拟不支持 Range 的服务器）。"""

            def send(self, request, **kwargs):
                request.headers.pop("Range", None)
                return super().send(request, **kwargs)

        adapter = NoRangeAdapter()
        session = requests.Session()
        session.mount(MockWebDavAdapter.BASE, adapter)
        backend = WebDavBackend(
            MockWebDavAdapter.BASE, auth=("u", "p"), session=session
        )
        adapter.files["/f.bin"] = bytes(range(256))
        assert backend.download_range("f.bin", 10, 20) == bytes(range(10, 21))

    def test_download_range_200_fallback_short_raises(self):
        """200 整文件回退但文件比请求区间短：报错而非静默返回短段。"""

        class NoRangeAdapter(MockWebDavAdapter):
            def send(self, request, **kwargs):
                request.headers.pop("Range", None)
                return super().send(request, **kwargs)

        adapter = NoRangeAdapter()
        session = requests.Session()
        session.mount(MockWebDavAdapter.BASE, adapter)
        backend = WebDavBackend(
            MockWebDavAdapter.BASE, auth=("u", "p"), session=session
        )
        adapter.files["/f.bin"] = bytes(range(64))
        with pytest.raises(ConnectionError):
            backend.download_range("f.bin", 50, 100)


class TestWebDavBackendUpload:
    """PUT 分块上传。"""

    def test_upload_full_file(self, webdav_pair, tmp_path):
        backend, adapter = webdav_pair
        src = tmp_path / "src.bin"
        src.write_bytes(bytes(range(256)))
        progress = list(backend.upload_chunked(str(src), "dst.bin", chunk=64))
        assert adapter.files["/dst.bin"] == bytes(range(256))
        assert progress[-1] == 1.0
        assert all(0 <= p <= 1 for p in progress)

    def test_upload_empty_file(self, webdav_pair, tmp_path):
        backend, adapter = webdav_pair
        src = tmp_path / "empty.bin"
        src.write_bytes(b"")
        progress = list(backend.upload_chunked(str(src), "empty.bin"))
        assert adapter.files["/empty.bin"] == b""
        assert progress[-1] == 1.0

    def test_upload_resume(self, webdav_pair, tmp_path):
        """断点续传：远端已有部分内容时从偏移续传。"""
        backend, adapter = webdav_pair
        src = tmp_path / "src.bin"
        full = bytes(range(256))
        src.write_bytes(full)
        # 预置远端前 100 字节
        adapter.files["/dst.bin"] = full[:100]
        list(backend.upload_chunked(str(src), "dst.bin", chunk=64))
        assert adapter.files["/dst.bin"] == full


class TestWebDavBackendManage:
    """MKCOL / MOVE / DELETE / exists。"""

    def test_mkdir(self, webdav_pair):
        backend, adapter = webdav_pair
        backend.mkdir("a/b/c")
        assert "/a/b/c" in adapter.dirs

    def test_rename(self, webdav_pair):
        backend, adapter = webdav_pair
        adapter.files["/old"] = b"data"
        backend.rename("old", "new")
        assert "/old" not in adapter.files
        assert adapter.files["/new"] == b"data"

    def test_delete(self, webdav_pair):
        backend, adapter = webdav_pair
        adapter.files["/f.bin"] = b"x"
        backend.delete("f.bin")
        assert "/f.bin" not in adapter.files

    def test_delete_missing_ok(self, webdav_pair):
        backend, _ = webdav_pair
        # 404 视为已删除，不抛
        backend.delete("nope")

    def test_exists_true(self, webdav_pair):
        backend, adapter = webdav_pair
        adapter.files["/f.bin"] = b"x"
        assert backend.exists("f.bin") is True

    def test_exists_false(self, webdav_pair):
        backend, _ = webdav_pair
        assert backend.exists("nope") is False


class TestWebDavBackendUrlEncoding:
    """URL 路径编码。"""

    def test_url_encodes_spaces(self, webdav_pair):
        backend, _ = webdav_pair
        url = backend._url("my dir/file name.cpenc")
        # 空格应被编码为 %20
        assert "%20" in url
        assert " " not in url

    def test_url_preserves_slash(self, webdav_pair):
        backend, _ = webdav_pair
        url = backend._url("a/b/c")
        assert url.count("/") >= 3   # 保留路径分隔
