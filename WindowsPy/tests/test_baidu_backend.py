"""百度网盘存储后端单元测试。

使用内存 FakeSession 按序返回预置响应，验证：
  - 路径拼接（相对 -> 百度绝对路径）
  - list_dir / get_size / exists / download_range（dlink 与缓存）
  - 简单上传与三步分片上传（MD5 分块、进度序列）
  - 断点续传命中（远端已存在同大小直接跳过）
  - token 过期（errno=111）自动刷新重试
  - mkdir/delete 幂等、rename 同目录与跨目录分支
  - BaiduCredentialStore 加密落盘往返

不依赖真实凭证与网络。
"""

from __future__ import annotations

import hashlib
import json

import pytest

from cloudprism.storage.baidu_backend import (
    PART_SIZE,
    BaiduApiError,
    BaiduCredentialStore,
    BaiduNetdiskBackend,
)


# ---------------------------------------------------------------------------
# Fake session
# ---------------------------------------------------------------------------


class FakeResponse:
    """预置响应：json 数据或二进制内容。"""

    def __init__(self, data: dict | None = None, content: bytes = b"", status: int = 200):
        self._data = data
        self.content = content
        self.status_code = status
        self.reason = ""

    def json(self) -> dict:
        if self._data is None:
            raise ValueError("no json")
        return self._data


class FakeSession:
    """按序返回预置响应的会话；记录全部调用。"""

    def __init__(self, responses: list[FakeResponse]):
        self.queue = list(responses)
        # 每项：(method, url, params, extra)
        self.calls: list[tuple] = []

    def get(self, url, params=None, headers=None):
        self.calls.append(("GET", url, params or {}, headers or {}))
        return self.queue.pop(0)

    def post(self, url, params=None, data=None, files=None):
        self.calls.append(("POST", url, params or {}, {"data": data, "files": files}))
        return self.queue.pop(0)


def make_backend(session: FakeSession, **kw) -> BaiduNetdiskBackend:
    """构造测试后端。"""
    return BaiduNetdiskBackend("AK", "SK", "tok", session=session, **kw)


def list_resp(items: list[dict]) -> FakeResponse:
    """method=list 成功响应。"""
    return FakeResponse({"errno": 0, "list": items})


# ---------------------------------------------------------------------------
# 路径与目录列表
# ---------------------------------------------------------------------------


def test_abs_path_conversion():
    """相对路径统一转为 / 开头的绝对路径。"""
    b = make_backend(FakeSession([]))
    assert b._abs("vault/a.enc") == "/vault/a.enc"
    assert b._abs("/vault/") == "/vault"
    assert b._abs("") == "/"


def test_list_dir_params_and_entries():
    """list_dir 拼接绝对目录并解析条目。"""
    s = FakeSession([list_resp([
        {"server_filename": "a.enc", "isdir": 0, "size": 10},
        {"server_filename": "dir", "isdir": 1, "size": 0},
    ])])
    b = make_backend(s)
    entries = b.list_dir("vault")
    assert [(e.name, e.is_dir, e.size) for e in entries] == [
        ("a.enc", False, 10),
        ("dir", True, 0),
    ]
    method, url, params, _ = s.calls[0]
    assert method == "GET" and params["method"] == "list"
    assert params["dir"] == "/vault"
    assert params["access_token"] == "tok"


# ---------------------------------------------------------------------------
# 条目查询
# ---------------------------------------------------------------------------


def test_get_size_and_exists():
    """get_size / exists 经父目录列表匹配条目。"""
    item = {"server_filename": "a.enc", "isdir": 0, "size": 42, "fs_id": 7}
    s = FakeSession([list_resp([item]), list_resp([item]), list_resp([])])
    b = make_backend(s)
    assert b.get_size("vault/a.enc") == 42
    assert b.exists("vault/a.enc") is True
    assert b.exists("vault/none.enc") is False


def test_get_size_missing_raises():
    """不存在的文件抛 FileNotFoundError。"""
    s = FakeSession([list_resp([])])
    b = make_backend(s)
    with pytest.raises(FileNotFoundError):
        b.get_size("vault/none.enc")


# ---------------------------------------------------------------------------
# 下载（dlink + Range）
# ---------------------------------------------------------------------------


def test_download_range_uses_dlink_and_range_header():
    """下载经 filemetas 取 dlink，并按 Range 取字节。"""
    item = {"server_filename": "a.enc", "isdir": 0, "size": 100, "fs_id": 7}
    s = FakeSession([
        list_resp([item]),  # _entry_of
        FakeResponse({"errno": 0, "list": [{"dlink": "https://dl/x", "fs_id": 7}]}),
        FakeResponse(content=b"hello", status=206),
    ])
    b = make_backend(s)
    data = b.download_range("vault/a.enc", 5, 9)
    assert data == b"hello"
    method, url, params, headers = s.calls[2]
    assert url == "https://dl/x"
    assert headers["Range"] == "bytes=5-9"
    assert headers["User-Agent"] == "pan.baidu.com"
    assert params["access_token"] == "tok"


def test_dlink_cache_skips_filemetas():
    """第二次下载命中 dlink 缓存，不再请求 filemetas。"""
    item = {"server_filename": "a.enc", "isdir": 0, "size": 100, "fs_id": 7}
    s = FakeSession([
        list_resp([item]),
        FakeResponse({"errno": 0, "list": [{"dlink": "https://dl/x"}]}),
        FakeResponse(content=b"1", status=206),
        # 第二次：仅父目录列表 + dlink GET，无 filemetas
        list_resp([item]),
        FakeResponse(content=b"2", status=206),
    ])
    b = make_backend(s)
    assert b.download_range("vault/a.enc", 0, 0) == b"1"
    assert b.download_range("vault/a.enc", 1, 1) == b"2"
    filemetas_calls = [c for c in s.calls if "filemetas" in str(c[2])]
    assert len(filemetas_calls) == 1


def test_download_invalid_range():
    """非法范围抛 ValueError。"""
    b = make_backend(FakeSession([]))
    with pytest.raises(ValueError):
        b.download_range("a.enc", 5, 1)


# ---------------------------------------------------------------------------
# 上传
# ---------------------------------------------------------------------------


def test_upload_simple_small_file(tmp_path):
    """<=4MB 走 method=upload，进度直接 1.0。"""
    f = tmp_path / "small.bin"
    f.write_bytes(b"x" * 10)
    s = FakeSession([
        FakeResponse({"errno": -9}),  # get_size：远端不存在
        FakeResponse({"errno": 0}),   # upload
    ])
    b = make_backend(s)
    progress = list(b.upload_chunked(str(f), "vault/small.bin"))
    assert progress == [1.0]
    method, url, params, extra = s.calls[1]
    assert method == "POST" and params["method"] == "upload"
    assert extra["data"]["path"] == "/vault/small.bin"


def test_upload_resume_skip_when_same_size(tmp_path):
    """远端已存在同大小文件：直接视为完成，不发起上传。"""
    f = tmp_path / "same.bin"
    f.write_bytes(b"x" * 10)
    s = FakeSession([list_resp([
        {"server_filename": "same.bin", "isdir": 0, "size": 10, "fs_id": 1}
    ])])
    b = make_backend(s)
    assert list(b.upload_chunked(str(f), "vault/same.bin")) == [1.0]
    assert len(s.calls) == 1  # 仅父目录列表


def test_upload_superfile_three_steps(tmp_path):
    """>4MB 走 precreate -> superfile2 -> create 三步。"""
    payload = b"a" * PART_SIZE + b"b" * 10
    f = tmp_path / "big.bin"
    f.write_bytes(payload)
    md5s = [
        hashlib.md5(payload[:PART_SIZE]).hexdigest(),
        hashlib.md5(payload[PART_SIZE:]).hexdigest(),
    ]
    s = FakeSession([
        FakeResponse({"errno": -9}),                     # get_size：不存在
        FakeResponse({"errno": 0, "uploadid": "up1"}),   # precreate
        FakeResponse({"errno": 0}),                      # superfile2 part 0
        FakeResponse({"errno": 0}),                      # superfile2 part 1
        FakeResponse({"errno": 0}),                      # create
    ])
    b = make_backend(s)
    progress = list(b.upload_chunked(str(f), "vault/big.bin"))
    # 进度按分片递增，最后一片后为 1.0
    assert len(progress) == 2
    assert progress[0] == pytest.approx(PART_SIZE / len(payload))
    assert progress[1] == 1.0

    # precreate：block_list 为各片 MD5 的 JSON 数组
    _, _, p_params, p_extra = s.calls[1]
    assert p_params["method"] == "precreate"
    assert json.loads(p_extra["data"]["block_list"]) == md5s
    assert p_extra["data"]["size"] == len(payload)

    # superfile2：partseq 依次 0、1，带 uploadid
    for i in (0, 1):
        _, url2, s_params, s_extra = s.calls[2 + i]
        assert url2.endswith("/superfile2") and s_params["method"] == "upload"
        assert s_extra["data"]["partseq"] == i
        assert s_extra["data"]["uploadid"] == "up1"

    # create：合并最终文件
    _, _, c_params, c_extra = s.calls[4]
    assert c_params["method"] == "create"
    assert c_extra["data"]["uploadid"] == "up1"


# ---------------------------------------------------------------------------
# token 刷新与错误码
# ---------------------------------------------------------------------------


def test_token_expired_auto_refresh():
    """errno=111 自动用 refresh_token 刷新并重试。"""
    s = FakeSession([
        FakeResponse({"errno": 111}),  # 首次 token 过期
        FakeResponse({"access_token": "tok2", "refresh_token": "r2",
                      "expires_in": 3600}),
        list_resp([]),                 # 重试成功
    ])
    b = make_backend(s, refresh_token="r1")
    assert b.list_dir("vault") == []
    assert b.access_token == "tok2"
    # 刷新请求走 oauth token 端点
    assert "oauth/2.0/token" in s.calls[1][1]


def test_token_expired_without_refresh_token():
    """无 refresh_token 时过期直接报错。"""
    s = FakeSession([FakeResponse({"errno": 111})])
    b = make_backend(s)
    with pytest.raises(ConnectionError):
        b.list_dir("vault")


def test_api_error_carries_errno():
    """非零且非 111 的 errno 抛 BaiduApiError 并携带 errno。"""
    s = FakeSession([FakeResponse({"errno": 31064})])
    b = make_backend(s)
    with pytest.raises(BaiduApiError) as ei:
        b.list_dir("vault")
    assert ei.value.errno == 31064


# ---------------------------------------------------------------------------
# 目录/文件操作
# ---------------------------------------------------------------------------


def test_mkdir_idempotent_on_exist():
    """mkdir 对已存在目录（-8）幂等。"""
    s = FakeSession([FakeResponse({"errno": -8})])
    b = make_backend(s)
    b.mkdir("vault/sub")  # 不应抛异常
    _, _, params, extra = s.calls[0]
    assert params["method"] == "create"
    assert extra["data"]["isdir"] == 1
    assert extra["data"]["path"] == "/vault/sub"


def test_delete_idempotent_on_missing():
    """delete 对不存在目标（-9/12）幂等。"""
    for errno in (-9, 12):
        s = FakeSession([FakeResponse({"errno": errno})])
        make_backend(s).delete("vault/x")  # 不应抛异常


def test_delete_filelist_payload():
    """delete 以 filemanager opera=delete 携带绝对路径列表。"""
    s = FakeSession([FakeResponse({"errno": 0})])
    make_backend(s).delete("vault/x")
    _, _, params, extra = s.calls[0]
    assert params["method"] == "filemanager" and params["opera"] == "delete"
    assert json.loads(extra["data"]["filelist"]) == ["/vault/x"]


def test_rename_same_dir_uses_rename_api():
    """同目录重命名走 method=rename。"""
    s = FakeSession([FakeResponse({"errno": 0})])
    make_backend(s).rename("vault/a.enc", "vault/b.enc")
    _, _, params, extra = s.calls[0]
    assert params["method"] == "rename"
    assert extra["data"] == {"path": "/vault/a.enc", "newname": "b.enc"}


def test_rename_cross_dir_uses_move():
    """跨目录走 filemanager opera=move。"""
    s = FakeSession([FakeResponse({"errno": 0})])
    make_backend(s).rename("vault/a.enc", "other/b.enc")
    _, _, params, extra = s.calls[0]
    assert params["method"] == "filemanager" and params["opera"] == "move"
    fl = json.loads(extra["data"]["filelist"])
    assert fl == [{"path": "/vault/a.enc", "dest": "/other", "newname": "b.enc"}]


# ---------------------------------------------------------------------------
# 凭证存储
# ---------------------------------------------------------------------------


def test_credential_store_roundtrip(tmp_path):
    """凭证加密落盘后可完整读回。"""
    path = str(tmp_path / "baidu.json")
    store = BaiduCredentialStore(path=path)
    creds = {"app_key": "AK", "secret_key": "SK", "access_token": "tok"}
    store.save(creds)
    assert store.load() == creds
    # 落盘内容带加密前缀（DPAPI 或降级 Base64）
    with open(path, "rb") as f:
        raw = f.read()
    assert raw.startswith((b"DPAPI:", b"PLAIN:"))


def test_credential_store_missing_returns_none(tmp_path):
    """文件不存在时返回 None。"""
    store = BaiduCredentialStore(path=str(tmp_path / "nope.json"))
    assert store.load() is None
