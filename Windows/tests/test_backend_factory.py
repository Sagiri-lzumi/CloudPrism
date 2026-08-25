"""backend_factory 单元测试：按参数构造后端与友好描述。"""

from __future__ import annotations

import pytest

from cloudprism.core.backend_factory import (
    build_backend_from_params,
    describe_backend,
)
from cloudprism.storage.baidu_backend import (
    BaiduCredentialStore,
    BaiduNetdiskBackend,
)
from cloudprism.storage.local_backend import LocalFolderBackend
from cloudprism.storage.webdav_backend import WebDavBackend


# ---------------------------------------------------------------------------
# build_backend_from_params
# ---------------------------------------------------------------------------


def test_local_backend(tmp_path):
    """local：目录存在即构造成功。"""
    b = build_backend_from_params("local", local_dir=str(tmp_path))
    assert isinstance(b, LocalFolderBackend)
    assert str(b.root) == str(tmp_path.resolve())


def test_local_backend_missing_dir(tmp_path):
    """local：目录不存在抛错（LocalFolderBackend 契约）。"""
    with pytest.raises(FileNotFoundError):
        build_backend_from_params("local", local_dir=str(tmp_path / "nope"))


def test_webdav_backend():
    """webdav：url 与账号透传。"""
    b = build_backend_from_params(
        "webdav",
        webdav_url="https://dav.example.com/d/",
        webdav_user="u1",
        webdav_pass="p1",
    )
    assert isinstance(b, WebDavBackend)
    assert b.base_url == "https://dav.example.com/d"
    assert b.auth == ("u1", "p1")


def test_baidu_without_credentials(tmp_path):
    """baidu：无凭证抛 ConnectionError。"""
    store = BaiduCredentialStore(path=str(tmp_path / "baidu.json"))
    with pytest.raises(ConnectionError):
        build_backend_from_params("baidu", baidu_store=store)


def test_baidu_with_credentials(tmp_path):
    """baidu：有凭证构造成功且携带刷新存储。"""
    store = BaiduCredentialStore(path=str(tmp_path / "baidu.json"))
    store.save({"app_key": "AK", "secret_key": "SK", "access_token": "tok"})
    b = build_backend_from_params("baidu", baidu_store=store)
    assert isinstance(b, BaiduNetdiskBackend)
    assert b.access_token == "tok"


def test_unknown_type_raises():
    """未知类型抛 ValueError。"""
    with pytest.raises(ValueError):
        build_backend_from_params("aliyun")


# ---------------------------------------------------------------------------
# describe_backend
# ---------------------------------------------------------------------------


def test_describe_local(tmp_path):
    b = build_backend_from_params("local", local_dir=str(tmp_path))
    label, path, key = describe_backend(b)
    assert (label, key) == ("本地文件夹", "local")
    assert path == str(tmp_path.resolve())


def test_describe_webdav():
    b = build_backend_from_params(
        "webdav", webdav_url="https://d.x/", webdav_user="u", webdav_pass="p"
    )
    label, path, key = describe_backend(b)
    assert (label, path, key) == ("WebDAV", "https://d.x", "webdav")


def test_describe_baidu(tmp_path):
    store = BaiduCredentialStore(path=str(tmp_path / "baidu.json"))
    store.save({"app_key": "AK", "secret_key": "SK", "access_token": "tok"})
    b = build_backend_from_params("baidu", baidu_store=store)
    label, path, key = describe_backend(b)
    assert key == "baidu"
    assert label == "百度网盘"
    assert "百度网盘" in path
