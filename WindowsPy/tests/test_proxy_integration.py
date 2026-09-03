"""流式解密代理集成测试。

启动真实本地代理（ThreadingHTTPServer）+ 两类后端，用标准库 HTTP 客户端
验证 Range 请求 -> 密文拉取 -> 内存解密 -> 206 响应全链路，并与磁盘明文
逐字节比对；HEAD 返回正确明文总量；确认代理不落明文文件。
"""

from __future__ import annotations

import urllib.error
import urllib.request

import pytest

from cloudprism.core.encryptor import Encryptor
from cloudprism.core.session import Session
from cloudprism.streaming.proxy_server import (
    parse_range_header,
    start_proxy,
    stop_proxy,
)
from cloudprism.storage.local_backend import LocalFolderBackend

from test_webdav_backend import MockWebDavAdapter


MASTER_PW = "proxy_test_pw"


# ---------------------------------------------------------------------------
# fixtures
# ---------------------------------------------------------------------------


@pytest.fixture(params=["local", "webdav"])
def env(request, tmp_path):
    """返回 (proxy_url_prefix, plaintext, backend_root_for_check)。

    准备一个已加密上传的文件，并启动本地代理；用例结束后关闭。
    """
    import requests

    if request.param == "local":
        root = tmp_path / "backend"
        root.mkdir()
        backend = LocalFolderBackend(root)
        root_for_check = root
    else:
        adapter = MockWebDavAdapter()
        sess = requests.Session()
        sess.mount(MockWebDavAdapter.BASE, adapter)
        from cloudprism.storage.webdav_backend import WebDavBackend

        backend = WebDavBackend(
            MockWebDavAdapter.BASE, auth=("u", "p"), session=sess
        )
        root_for_check = None

    # 加密上传一个已知明文
    plaintext = bytes(range(256)) * 20          # 5120 字节
    src = tmp_path / "plain.bin"
    src.write_bytes(plaintext)

    session = Session(MASTER_PW)
    enc = Encryptor(session, backend, chunk=256)
    list(enc.encrypt_and_upload(str(src), "media/video.cpenc"))

    # 启动代理
    server, port = start_proxy(session, backend)
    url_prefix = f"http://127.0.0.1:{port}"

    yield url_prefix, plaintext, root_for_check

    stop_proxy(server)      # 用例结束停止代理（退出服务循环+关闭套接字）


def _http_get(url: str, headers: dict | None = None):
    """发起 GET，返回 (status, data, resp_headers)。"""
    req = urllib.request.Request(url, headers=headers or {})
    try:
        with urllib.request.urlopen(req) as r:
            return r.status, r.read(), dict(r.headers)
    except urllib.error.HTTPError as e:
        return e.code, e.read(), dict(e.headers)


def _http_head(url: str):
    """发起 HEAD，返回 (status, resp_headers)。"""
    req = urllib.request.Request(url, method="HEAD")
    with urllib.request.urlopen(req) as r:
        return r.status, dict(r.headers)


# ---------------------------------------------------------------------------
# Range 头解析单测
# ---------------------------------------------------------------------------


class TestParseRangeHeader:
    """parse_range_header 解析。"""

    def test_no_header(self):
        """无 Range 头返回整个文件。"""
        assert parse_range_header(None, 1000) == (0, 999)

    def test_explicit_range(self):
        assert parse_range_header("bytes=100-199", 1000) == (100, 199)

    def test_open_ended_range(self):
        """bytes=N- 取到文件末尾。"""
        assert parse_range_header("bytes=100-", 1000) == (100, 999)

    def test_end_beyond_total(self):
        """end 超过总量时截断。"""
        assert parse_range_header("bytes=900-5000", 1000) == (900, 999)

    def test_start_beyond_total(self):
        """start 超过总量时返回 None（调用方回 416，不回退整文件）。"""
        assert parse_range_header("bytes=2000-", 1000) is None

    def test_start_after_end(self):
        """start > end 的非法区间同样返回 None。"""
        assert parse_range_header("bytes=500-100", 1000) is None

    def test_zero_total(self):
        assert parse_range_header(None, 0) == (0, 0)

    def test_invalid_format(self):
        """非法格式回退全文件。"""
        assert parse_range_header("items=1-5", 1000) == (0, 999)


# ---------------------------------------------------------------------------
# 代理集成测试
# ---------------------------------------------------------------------------


class TestProxyIntegration:
    """本地代理全链路。"""

    def test_get_full_file(self, env):
        """无 Range：206 且明文一致（文件小于上限，单响应完整返回）。"""
        url_prefix, plaintext, _ = env
        url = f"{url_prefix}/media/video.cpenc"
        status, data, headers = _http_get(url)
        assert status == 206
        assert data == plaintext
        assert headers.get("Content-Length") == str(len(plaintext))
        assert headers.get("Accept-Ranges") == "bytes"

    def test_get_with_range(self, env):
        """带 Range：区间明文逐字节一致。"""
        url_prefix, plaintext, _ = env
        url = f"{url_prefix}/media/video.cpenc"
        status, data, headers = _http_get(
            url, {"Range": "bytes=100-299"}
        )
        assert status == 206
        assert data == plaintext[100:300]
        # Content-Range 正确
        cr = headers.get("Content-Range")
        assert cr == f"bytes 100-299/{len(plaintext)}"

    @pytest.mark.parametrize(
        "start,end",
        [
            (0, 0),            # 单字节
            (0, 15),           # 恰一块
            (0, 16),           # 一块+1
            (15, 16),          # 跨块边界
            (1000, 1500),      # 中段
            (5119, 5119),      # 末字节
            (5100, 5119),      # 末尾段
        ],
    )
    def test_get_various_ranges(self, env, start, end):
        """多种区间（含跨块/末尾边界）明文一致。"""
        url_prefix, plaintext, _ = env
        url = f"{url_prefix}/media/video.cpenc"
        status, data, _ = _http_get(
            url, {"Range": f"bytes={start}-{end}"}
        )
        assert status == 206
        assert data == plaintext[start : end + 1]

    def test_get_open_ended_range(self, env):
        """bytes=N- 到末尾。"""
        url_prefix, plaintext, _ = env
        url = f"{url_prefix}/media/video.cpenc"
        status, data, _ = _http_get(url, {"Range": "bytes=5000-"})
        assert status == 206
        assert data == plaintext[5000:]

    def test_head_returns_plaintext_size(self, env):
        """HEAD 返回明文总大小（非密文文件大小）。"""
        url_prefix, plaintext, _ = env
        url = f"{url_prefix}/media/video.cpenc"
        status, headers = _http_head(url)
        assert status == 200
        assert headers.get("Content-Length") == str(len(plaintext))

    def test_get_missing_file_404(self, env):
        """不存在的文件 404。"""
        url_prefix, _, _ = env
        status, _data, _ = _http_get(f"{url_prefix}/nope.cpenc")
        assert status == 404

    def test_range_beyond_total_416(self, env):
        """start 越界的 Range 回 416（带 Content-Range: */total）。"""
        url_prefix, plaintext, _ = env
        url = f"{url_prefix}/media/video.cpenc"
        status, _data, headers = _http_get(
            url, {"Range": f"bytes={len(plaintext) + 100}-"}
        )
        assert status == 416
        assert headers.get("Content-Range") == f"bytes */{len(plaintext)}"

    def test_no_plaintext_files_leaked(self, env):
        """代理工作目录不落明文文件（本地后端根内无明文副本）。

        本地后端根目录只应有密文 .cpenc 与目录，不应出现明文 plain.bin
        或任何包含明文内容的临时文件。
        """
        url_prefix, plaintext, root_for_check = env
        if root_for_check is None:
            pytest.skip("WebDAV mock 后端无本地根目录可扫描")
        # 先请求一段，确保代理实际工作过
        _http_get(f"{url_prefix}/media/video.cpenc", {"Range": "bytes=0-99"})
        # 扫描后端根目录：所有文件应为 .cpenc（密文），且内容不等于明文段
        for p in root_for_check.rglob("*"):
            if p.is_file():
                content = p.read_bytes()
                assert content != plaintext, f"发现明文泄露：{p}"
                # 密文不应包含明文前 256 字节的原文
                assert plaintext[:256] not in content


class TestProxyLargeFileCap:
    """单次响应上限：开口区间被截断为多次有界 206（大视频播放修复）。"""

    @pytest.fixture
    def big_env(self, tmp_path, monkeypatch):
        """较大明文 + 调低上限，验证截断与多段拼接。"""
        import cloudprism.streaming.proxy_server as ps

        monkeypatch.setattr(ps, "MAX_RESPONSE_BYTES", 1024)

        root = tmp_path / "backend_big"
        root.mkdir()
        backend = LocalFolderBackend(root)
        plaintext = bytes(range(256)) * 16        # 4096 字节（> 上限）
        src = tmp_path / "big.bin"
        src.write_bytes(plaintext)

        session = Session(MASTER_PW)
        enc = Encryptor(session, backend, chunk=512)
        list(enc.encrypt_and_upload(str(src), "media/big.cpenc"))

        server, port = start_proxy(session, backend)
        yield f"http://127.0.0.1:{port}", plaintext
        stop_proxy(server)

    def test_open_range_capped(self, big_env):
        """bytes=0- 不再整文件缓冲：单段不超过上限，Content-Range 反映截断。"""
        url_prefix, plaintext = big_env
        url = f"{url_prefix}/media/big.cpenc"
        status, data, headers = _http_get(url, {"Range": "bytes=0-"})
        assert status == 206
        assert len(data) == 1024
        assert data == plaintext[:1024]
        assert headers.get("Content-Range") == (
            f"bytes 0-1023/{len(plaintext)}"
        )

    def test_no_range_capped(self, big_env):
        """无 Range GET 同样受上限截断。"""
        url_prefix, plaintext = big_env
        status, data, _ = _http_get(f"{url_prefix}/media/big.cpenc")
        assert status == 206
        assert len(data) == 1024
        assert data == plaintext[:1024]

    def test_stitch_all_segments(self, big_env):
        """按续请逻辑逐段拉取，拼接后与完整明文逐字节一致。"""
        url_prefix, plaintext = big_env
        url = f"{url_prefix}/media/big.cpenc"
        total = len(plaintext)
        got = bytearray()
        pos = 0
        while pos < total:
            status, data, headers = _http_get(url, {"Range": f"bytes={pos}-"})
            assert status == 206
            got.extend(data)
            # 按 Content-Range 推进（播放器续请依据）
            cr = headers["Content-Range"]          # bytes s-e/total
            end = int(cr.split(" ")[1].split("/")[0].split("-")[1])
            pos = end + 1
            assert len(got) == pos
        assert bytes(got) == plaintext
