"""存储后端构造工厂与描述工具（非 GUI）。

供初始化向导与密库快速连接共用：按类型键与参数构造后端实例，
避免两处各自维护构造逻辑。主密码/密码参数由调用方即时传入，绝不落盘。
"""

from __future__ import annotations

from cloudprism.storage.backend import StorageBackend
from cloudprism.storage.baidu_backend import (
    BaiduCredentialStore,
    BaiduNetdiskBackend,
)
from cloudprism.storage.local_backend import LocalFolderBackend
from cloudprism.storage.webdav_backend import WebDavBackend


def build_backend_from_params(
    backend_type: str,
    *,
    local_dir: str = "",
    webdav_url: str = "",
    webdav_user: str = "",
    webdav_pass: str = "",
    baidu_store: BaiduCredentialStore | None = None,
) -> StorageBackend:
    """按类型键构造后端实例。

    - local  : 本地文件夹（目录须存在）
    - webdav : WebDAV（url + 账号密码）
    - baidu  : 百度网盘（读取 DPAPI 加密凭证，未授权时抛 ConnectionError）
    """
    if backend_type == "local":
        return LocalFolderBackend(local_dir)
    if backend_type == "webdav":
        return WebDavBackend(webdav_url, auth=(webdav_user, webdav_pass))
    if backend_type == "baidu":
        store = baidu_store or BaiduCredentialStore()
        creds = store.load()
        if not creds or not creds.get("access_token"):
            raise ConnectionError(
                "尚未完成百度网盘授权，请先在向导中完成授权，"
                "或按《Plan/百度网盘开放平台申请指南》申请凭证"
            )
        return BaiduNetdiskBackend(
            app_key=creds.get("app_key", ""),
            secret_key=creds.get("secret_key", ""),
            access_token=creds["access_token"],
            app_id=creds.get("app_id", ""),
            refresh_token=creds.get("refresh_token", ""),
            expires_at=float(creds.get("expires_at", 0.0)),
            credential_store=store,
        )
    raise ValueError(f"未知后端类型：{backend_type}")


def describe_backend(backend: StorageBackend) -> tuple[str, str, str]:
    """后端实例 -> (显示名, 路径/地址, 类型键)。"""
    if isinstance(backend, LocalFolderBackend):
        return "本地文件夹", str(backend.root), "local"
    if isinstance(backend, WebDavBackend):
        return "WebDAV", backend.base_url, "webdav"
    if isinstance(backend, BaiduNetdiskBackend):
        return "百度网盘", "百度网盘（开放平台授权）", "baidu"
    return type(backend).__name__, "-", "unknown"
