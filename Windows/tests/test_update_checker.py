"""检查更新单元测试：版本标签归一化、版本比较与 release 拉取。"""

from __future__ import annotations

import pytest

from cloudprism.core import update_checker
from cloudprism.core.update_checker import (
    compare_versions,
    fetch_latest_release,
    normalize_tag,
)


class TestNormalizeTag:
    """版本标签归一化。"""

    def test_strip_v_prefix(self):
        assert normalize_tag("v1.2.3") == "1.2.3"
        assert normalize_tag("V1.2.3") == "1.2.3"

    def test_strip_whitespace(self):
        assert normalize_tag("  1.0.0  ") == "1.0.0"

    def test_empty_and_none(self):
        assert normalize_tag("") == ""
        assert normalize_tag(None) == ""


class TestCompareVersions:
    """按点分段数字比较。"""

    def test_equal(self):
        assert compare_versions("1.0.0", "1.0.0") == 0

    def test_less(self):
        assert compare_versions("1.0.0", "1.0.1") == -1
        # 数字比较而非字符串比较：9 < 10
        assert compare_versions("1.9", "1.10") == -1

    def test_greater(self):
        assert compare_versions("2.0.0", "1.9.9") == 1

    def test_v_prefix_and_missing_segments(self):
        # 缺位段补 0：1.0 == 1.0.0
        assert compare_versions("v1.0", "1.0.0") == 0
        assert compare_versions("1.0", "v1.0.1") == -1

    def test_non_numeric_segment_tolerated(self):
        # 非数字段（如 -beta 后缀）容错为 0，不抛异常
        assert compare_versions("1.0.0-beta", "1.0.0") == 0
        assert compare_versions("abc", "0.0.0") == 0


class _FakeResponse:
    """requests 响应假对象。"""

    def __init__(self, status_code: int = 200, payload=None, json_error=False):
        self.status_code = status_code
        self._payload = payload or {}
        self._json_error = json_error

    def json(self):
        if self._json_error:
            raise ValueError("bad json")
        return self._payload


class TestFetchLatestRelease:
    """GitHub release 拉取（monkeypatch requests，不发起真实网络请求）。"""

    def test_success_parses_fields(self, monkeypatch):
        monkeypatch.setattr(
            update_checker.requests, "get",
            lambda *a, **kw: _FakeResponse(200, {
                "tag_name": "v1.2.0",
                "name": "第二版发布",
                "html_url": "https://example.com/rel",
            }),
        )
        out = fetch_latest_release()
        assert out["tag"] == "1.2.0"  # v 前缀已归一化
        assert out["name"] == "第二版发布"
        assert out["url"] == "https://example.com/rel"

    def test_missing_html_url_falls_back_to_releases_page(self, monkeypatch):
        monkeypatch.setattr(
            update_checker.requests, "get",
            lambda *a, **kw: _FakeResponse(200, {"tag_name": "1.0.0"}),
        )
        assert fetch_latest_release()["url"] == update_checker.RELEASES_PAGE

    def test_404_raises_lookup_error(self, monkeypatch):
        """仓库尚无 release：抛 LookupError（上层走"暂无发布版本"分支）。"""
        monkeypatch.setattr(
            update_checker.requests, "get", lambda *a, **kw: _FakeResponse(404)
        )
        with pytest.raises(LookupError):
            fetch_latest_release()

    def test_403_rate_limit(self, monkeypatch):
        monkeypatch.setattr(
            update_checker.requests, "get", lambda *a, **kw: _FakeResponse(403)
        )
        with pytest.raises(ConnectionError, match="限流"):
            fetch_latest_release()

    def test_other_status_code(self, monkeypatch):
        monkeypatch.setattr(
            update_checker.requests, "get", lambda *a, **kw: _FakeResponse(500)
        )
        with pytest.raises(ConnectionError, match="500"):
            fetch_latest_release()

    def test_network_error_wrapped(self, monkeypatch):
        import requests as rq

        def boom(*a, **kw):
            raise rq.ConnectionError("不可达")

        monkeypatch.setattr(update_checker.requests, "get", boom)
        with pytest.raises(ConnectionError, match="网络"):
            fetch_latest_release()

    def test_bad_json_wrapped(self, monkeypatch):
        monkeypatch.setattr(
            update_checker.requests, "get",
            lambda *a, **kw: _FakeResponse(200, json_error=True),
        )
        with pytest.raises(ConnectionError, match="JSON"):
            fetch_latest_release()
