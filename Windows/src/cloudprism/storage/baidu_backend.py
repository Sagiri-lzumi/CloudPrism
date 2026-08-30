"""百度网盘存储后端。

基于百度网盘开放平台（XPAN）API 实现 StorageBackend 接口：

  - list_dir        -> GET  /rest/2.0/xpan/file?method=list
  - get_size/exists -> 列父目录匹配条目（取 size / fsid / dlink）
  - download_range  -> filemetas(dlink=1) 取直链 -> GET dlink（支持 Range）
  - upload_chunked  -> <=4MB 走 method=upload；>4MB 走
                       precreate -> superfile2(4MB 分片) -> create 三步
  - mkdir           -> method=create&isdir=1
  - rename          -> method=rename（同目录）/ filemanager opera=move（跨目录）
  - delete          -> filemanager opera=delete

鉴权：OAuth2 access_token（30 天）；errno=111 时自动用 refresh_token 刷新。
凭证与 token 由 BaiduCredentialStore 管理（Windows 下 DPAPI 加密落盘）。

申请凭证流程详见 Plan/百度网盘开放平台申请指南.md。
"""

from __future__ import annotations

import base64
import hashlib
import json
import os
import time
from typing import Iterator

import requests

from cloudprism.storage.backend import RemoteEntry


# ---- 接口地址 ----
_OAUTH_TOKEN_URL = "https://openapi.baidu.com/oauth/2.0/token"
_XPAN_BASE = "https://pan.baidu.com/rest/2.0/xpan"

# 百度分片上传强制要求：单片 4MB（不能更大）
PART_SIZE = 4 * 1024 * 1024
# <= 此大小可直接简单上传
SIMPLE_UPLOAD_MAX = 4 * 1024 * 1024
# dlink 官方有效期约 8 小时，缓存保守取 6 小时
_DLINK_TTL = 6 * 3600

# 常见错误码
_ERR_TOKEN_EXPIRED = 111       # access_token 过期
_ERR_FILE_NOT_EXIST = -9       # 文件不存在（部分接口）
_ERR_ALREADY_EXIST = -8        # 目录已存在


class BaiduApiError(ConnectionError):
    """百度接口错误（携带 errno 便于上层分支）。"""

    def __init__(self, errno: int, msg: str = "") -> None:
        super().__init__(f"百度网盘接口错误 errno={errno} {msg}")
        self.errno = errno


# ---------------------------------------------------------------------------
# 凭证与 token 存储
# ---------------------------------------------------------------------------


def _dpapi_protect(data: bytes) -> bytes:
    """Windows DPAPI 加密（当前用户级），非 Windows 抛 OSError。"""
    import ctypes
    import ctypes.wintypes

    class DATA_BLOB(ctypes.Structure):
        _fields_ = [
            ("cbData", ctypes.wintypes.DWORD),
            ("pbData", ctypes.POINTER(ctypes.c_char)),
        ]

    blob_in = DATA_BLOB(len(data), ctypes.create_string_buffer(data, len(data)))
    blob_out = DATA_BLOB()
    ok = ctypes.windll.crypt32.CryptProtectData(  # type: ignore[attr-defined]
        ctypes.byref(blob_in), None, None, None, None, 0, ctypes.byref(blob_out)
    )
    if not ok:
        raise OSError("DPAPI 加密失败")
    out = ctypes.string_at(blob_out.pbData, blob_out.cbData)
    ctypes.windll.kernel32.LocalFree(blob_out.pbData)
    return out


def _dpapi_unprotect(data: bytes) -> bytes:
    """Windows DPAPI 解密，非 Windows 抛 OSError。"""
    import ctypes
    import ctypes.wintypes

    class DATA_BLOB(ctypes.Structure):
        _fields_ = [
            ("cbData", ctypes.wintypes.DWORD),
            ("pbData", ctypes.POINTER(ctypes.c_char)),
        ]

    blob_in = DATA_BLOB(len(data), ctypes.create_string_buffer(data, len(data)))
    blob_out = DATA_BLOB()
    ok = ctypes.windll.crypt32.CryptUnprotectData(  # type: ignore[attr-defined]
        ctypes.byref(blob_in), None, None, None, None, 0, ctypes.byref(blob_out)
    )
    if not ok:
        raise OSError("DPAPI 解密失败")
    out = ctypes.string_at(blob_out.pbData, blob_out.cbData)
    ctypes.windll.kernel32.LocalFree(blob_out.pbData)
    return out


class BaiduCredentialStore:
    """凭证与 token 的加密落盘存储。

    Windows：DPAPI（当前用户）加密后写程序目录旁 ``data/baidu.json``
    （便携化，跟随程序目录迁移）；其他平台（测试/移植）：Base64 明文兜底。
    """

    def __init__(self, path: str | None = None) -> None:
        if path is None:
            # 便携化：默认随程序目录，不再写 %APPDATA%
            from cloudprism.core.paths import baidu_credential_file

            path = baidu_credential_file()
        self.path = path

    def save(self, data: dict) -> None:
        """加密并写入凭证字典。"""
        os.makedirs(os.path.dirname(self.path), exist_ok=True)
        raw = json.dumps(data, ensure_ascii=False).encode("utf-8")
        try:
            payload = b"DPAPI:" + base64.b64encode(_dpapi_protect(raw))
        except (OSError, AttributeError, ImportError):
            payload = b"PLAIN:" + base64.b64encode(raw)
        with open(self.path, "wb") as f:
            f.write(payload)

    def load(self) -> dict | None:
        """读取并解密凭证；不存在或损坏时返回 None。"""
        if not os.path.exists(self.path):
            return None
        try:
            with open(self.path, "rb") as f:
                payload = f.read()
            if payload.startswith(b"DPAPI:"):
                raw = _dpapi_unprotect(base64.b64decode(payload[6:]))
            elif payload.startswith(b"PLAIN:"):
                raw = base64.b64decode(payload[6:])
            else:
                return None
            return json.loads(raw.decode("utf-8"))
        except Exception:
            return None

    def clear(self) -> None:
        """删除凭证文件（不存在时容忍）。"""
        try:
            os.remove(self.path)
        except OSError:
            pass


# ---------------------------------------------------------------------------
# 存储后端实现
# ---------------------------------------------------------------------------


class BaiduNetdiskBackend:
    """百度网盘存储后端（实现 StorageBackend 全部方法）。"""

    def __init__(
        self,
        app_key: str,
        secret_key: str,
        access_token: str,
        app_id: str = "",
        refresh_token: str = "",
        expires_at: float = 0.0,
        session: requests.Session | None = None,
        credential_store: BaiduCredentialStore | None = None,
    ) -> None:
        self.app_key = app_key
        self.secret_key = secret_key
        self.app_id = app_id
        self.access_token = access_token
        self.refresh_token = refresh_token
        self.expires_at = expires_at  # access_token 过期时间戳（0=未知）
        self.session = session or requests.Session()
        self._store = credential_store
        # dlink 缓存：fsid -> (dlink, 过期时间)
        self._dlink_cache: dict[int, tuple[str, float]] = {}

    # ------------------------------------------------------------------
    # 路径与请求基础
    # ------------------------------------------------------------------

    def _abs(self, path: str) -> str:
        """相对路径 -> 百度绝对路径（以 / 开头，根为 /）。"""
        rel = path.strip("/")
        return "/" + rel if rel else "/"

    def _api(
        self,
        url: str,
        *,
        data: dict | None = None,
        files: dict | None = None,
        params: dict | None = None,
        use_post: bool = False,
        _retried: bool = False,
    ) -> dict:
        """带 token 的 API 请求；errno=111 时自动刷新 token 重试一次。"""
        p = {"access_token": self.access_token}
        if params:
            p.update(params)
        if use_post or data is not None or files is not None:
            r = self.session.post(url, params=p, data=data or {}, files=files)
        else:
            r = self.session.get(url, params=p)
        try:
            out = r.json()
        except ValueError as e:
            raise ConnectionError(f"百度网盘接口响应非 JSON：{r.status_code}") from e
        errno = out.get("errno", 0)
        if errno == _ERR_TOKEN_EXPIRED and not _retried:
            self._refresh_access_token()
            return self._api(
                url, data=data, files=files, params=params,
                use_post=use_post, _retried=True,
            )
        if errno != 0:
            raise BaiduApiError(errno, json.dumps(out, ensure_ascii=False)[:200])
        return out

    def _refresh_access_token(self) -> None:
        """用 refresh_token 换取新的 access_token（长效续期）。"""
        if not self.refresh_token:
            raise ConnectionError("access_token 已过期且无 refresh_token，请重新授权")
        r = self.session.get(
            _OAUTH_TOKEN_URL,
            params={
                "grant_type": "refresh_token",
                "refresh_token": self.refresh_token,
                "client_id": self.app_key,
                "client_secret": self.secret_key,
            },
        )
        data = r.json()
        if "access_token" not in data:
            raise ConnectionError(f"刷新 token 失败：{data}")
        self.access_token = data["access_token"]
        self.refresh_token = data.get("refresh_token", self.refresh_token)
        self.expires_at = time.time() + int(data.get("expires_in", 0))
        # 回写加密存储（如有）
        if self._store is not None:
            saved = self._store.load() or {}
            saved.update(
                access_token=self.access_token,
                refresh_token=self.refresh_token,
                expires_at=self.expires_at,
            )
            self._store.save(saved)

    # ------------------------------------------------------------------
    # 条目查询
    # ------------------------------------------------------------------

    def _list_parent(self, path: str) -> dict[str, dict]:
        """列父目录，返回 {文件名: 条目dict}。"""
        abs_path = self._abs(path)
        parent = abs_path.rsplit("/", 1)[0] or "/"
        out = self._api(
            f"{_XPAN_BASE}/file", params={"method": "list", "dir": parent, "limit": 1000}
        )
        return {item["server_filename"]: item for item in out.get("list", [])}

    def _entry_of(self, path: str) -> dict:
        """取路径对应条目（含 fsid/size）；不存在时抛错。"""
        name = self._abs(path).rsplit("/", 1)[-1]
        siblings = self._list_parent(path)
        if name not in siblings:
            raise FileNotFoundError(f"百度网盘中不存在：{path}")
        return siblings[name]

    # ------------------------------------------------------------------
    # StorageBackend 实现
    # ------------------------------------------------------------------

    def list_dir(self, path: str) -> list[RemoteEntry]:
        """列出目录条目。"""
        out = self._api(
            f"{_XPAN_BASE}/file",
            params={"method": "list", "dir": self._abs(path), "limit": 1000},
        )
        return [
            RemoteEntry(
                name=item["server_filename"],
                is_dir=bool(item.get("isdir", 0)),
                size=int(item.get("size", 0)),
            )
            for item in out.get("list", [])
        ]

    def get_size(self, path: str) -> int:
        """取文件字节大小。"""
        return int(self._entry_of(path)["size"])

    def head(self, path: str) -> int:
        """远端文件大小（断点续传基准）。"""
        return self.get_size(path)

    def exists(self, path: str) -> bool:
        """判断路径是否存在（含目录）。"""
        try:
            self._entry_of(path)
            return True
        except FileNotFoundError:
            return False
        except BaiduApiError as e:
            if e.errno in (_ERR_FILE_NOT_EXIST, _ERR_ALREADY_EXIST):
                return False
            raise

    def download_range(self, path: str, start: int, end: int) -> bytes:
        """经 dlink 按字节范围下载（dlink 支持 HTTP Range）。"""
        if start < 0 or end < start:
            raise ValueError(f"非法范围：[{start}, {end}]")
        entry = self._entry_of(path)
        fsid = int(entry["fs_id"])
        dlink = self._get_dlink(fsid)
        # 百度要求携带 User-Agent，否则拒绝下载
        r = self.session.get(
            dlink,
            params={"access_token": self.access_token},
            headers={"User-Agent": "pan.baidu.com", "Range": f"bytes={start}-{end}"},
        )
        if r.status_code not in (200, 206):
            raise ConnectionError(f"dlink 下载失败：{r.status_code} {r.reason}")
        return r.content

    def _get_dlink(self, fsid: int) -> str:
        """取文件直链（带 6 小时缓存）。"""
        cached = self._dlink_cache.get(fsid)
        if cached and cached[1] > time.time():
            return cached[0]
        out = self._api(
            f"{_XPAN_BASE}/multimedia",
            params={"method": "filemetas", "fsids": json.dumps([fsid]), "dlink": 1},
        )
        metas = out.get("list", [])
        if not metas or "dlink" not in metas[0]:
            raise ConnectionError(f"获取 dlink 失败：{out}")
        dlink = metas[0]["dlink"]
        self._dlink_cache[fsid] = (dlink, time.time() + _DLINK_TTL)
        return dlink

    def upload_chunked(
        self,
        local_path: str,
        remote_path: str,
        chunk: int = 1 << 20,
    ) -> Iterator[float]:
        """加密容器上传。

        注意：百度分片大小固定 4MB（接口要求），忽略传入的 chunk 参数。
        支持断点续传：已完整存在（大小一致）时直接跳过。
        """
        total = os.path.getsize(local_path)
        abs_path = self._abs(remote_path)

        # 远端已存在且大小一致：视为已完成（断点续传命中）
        try:
            if self.get_size(remote_path) == total:
                yield 1.0
                return
        except (FileNotFoundError, BaiduApiError):
            pass

        if total <= SIMPLE_UPLOAD_MAX:
            yield from self._upload_simple(local_path, abs_path, total)
        else:
            yield from self._upload_superfile(local_path, abs_path, total)

    def _upload_simple(self, local_path: str, abs_path: str, total: int) -> Iterator[float]:
        """简单上传（<=4MB）。"""
        with open(local_path, "rb") as f:
            data = f.read()
        self._api(
            f"{_XPAN_BASE}/file",
            params={"method": "upload"},
            data={"path": abs_path, "ondup": "overwrite"},
            files={"file": (os.path.basename(abs_path), data)},
            use_post=True,
        )
        yield 1.0

    def _upload_superfile(self, local_path: str, abs_path: str, total: int) -> Iterator[float]:
        """三步分片上传：precreate -> superfile2 逐片 -> create。"""
        # 预读各 4MB 分片的 MD5（block_list 为分片 MD5 的 JSON 数组）
        md5_list: list[str] = []
        with open(local_path, "rb") as f:
            while True:
                blk = f.read(PART_SIZE)
                if not blk:
                    break
                md5_list.append(hashlib.md5(blk).hexdigest())

        # 1) 预创建，获取 uploadid
        out = self._api(
            f"{_XPAN_BASE}/file",
            params={"method": "precreate"},
            data={
                "path": abs_path,
                "size": total,
                "isdir": 0,
                "block_list": json.dumps(md5_list),
                "ondup": "newcopy",
            },
            use_post=True,
        )
        uploadid = out["uploadid"]

        # 2) 逐片上传，yield 进度（上传阶段占总进度 100%）
        uploaded = 0
        with open(local_path, "rb") as f:
            for seq in range(len(md5_list)):
                blk = f.read(PART_SIZE)
                self._api(
                    f"{_XPAN_BASE}/superfile2",
                    params={
                        "method": "upload",
                        "type": "tmpfile",
                        "path": abs_path,
                    },
                    data={"partseq": seq, "uploadid": uploadid},
                    files={"file": ("chunk", blk)},
                    use_post=True,
                )
                uploaded += len(blk)
                yield uploaded / total

        # 3) 合并创建最终文件
        self._api(
            f"{_XPAN_BASE}/file",
            params={"method": "create"},
            data={
                "path": abs_path,
                "size": total,
                "isdir": 0,
                "uploadid": uploadid,
                "block_list": json.dumps(md5_list),
            },
            use_post=True,
        )

    def mkdir(self, path: str) -> None:
        """创建目录（已存在视为成功）。"""
        try:
            self._api(
                f"{_XPAN_BASE}/file",
                params={"method": "create"},
                data={"path": self._abs(path), "isdir": 1, "size": 0},
                use_post=True,
            )
        except BaiduApiError as e:
            # -8：目录已存在，幂等处理
            if e.errno not in (_ERR_ALREADY_EXIST,):
                raise

    def rename(self, old: str, new: str) -> None:
        """重命名或移动。同目录走 rename 接口，跨目录走 filemanager move。"""
        old_abs = self._abs(old)
        new_abs = self._abs(new)
        old_parent = old_abs.rsplit("/", 1)[0] or "/"
        new_parent = new_abs.rsplit("/", 1)[0] or "/"
        new_name = new_abs.rsplit("/", 1)[-1]

        if old_parent == new_parent:
            self._api(
                f"{_XPAN_BASE}/file",
                params={"method": "rename"},
                data={"path": old_abs, "newname": new_name},
                use_post=True,
            )
        else:
            self._api(
                f"{_XPAN_BASE}/file",
                params={"method": "filemanager", "opera": "move"},
                data={
                    "async": 0,
                    "filelist": json.dumps(
                        [{"path": old_abs, "dest": new_parent, "newname": new_name}]
                    ),
                },
                use_post=True,
            )

    def delete(self, path: str) -> None:
        """删除文件或目录（不存在视为成功，幂等）。"""
        try:
            self._api(
                f"{_XPAN_BASE}/file",
                params={"method": "filemanager", "opera": "delete"},
                data={"async": 0, "filelist": json.dumps([self._abs(path)])},
                use_post=True,
            )
        except BaiduApiError as e:
            # -9 / 12：目标不存在，幂等处理
            if e.errno not in (_ERR_FILE_NOT_EXIST, 12):
                raise
